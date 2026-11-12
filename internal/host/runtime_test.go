package host

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/afonsodelgado/factory/internal/factory"
	"github.com/afonsodelgado/factory/internal/remote"
	"github.com/afonsodelgado/factory/internal/review"
	"github.com/afonsodelgado/factory/internal/state"
	"github.com/afonsodelgado/factory/internal/tmux"
)

type recordingTypist struct{ calls chan string }

func (r recordingTypist) Type(pane, text string) error { r.calls <- text; return nil }

type fixture struct {
	cfg        *state.Config
	tm         tmux.Client
	engine     *Panes
	dir        string
	worktree   string
	supervisor tmux.Pane
	pipeline   string
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		if os.Getenv("CI") == "" {
			t.Skip("tmux not installed")
		}
		t.Fatal(err)
	}
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(root, "claude"))
	t.Setenv("FACTORY_CONFIG_DIR", filepath.Join(root, "config"))
	t.Setenv("FACTORY_STATE_DIR", filepath.Join(root, "state"))
	t.Setenv("FACTORY_TMUX_SOCKET", "host-test-"+uuid.NewString())
	shell, err := exec.LookPath("bash")
	if err != nil {
		shell = "/bin/sh"
	}
	t.Setenv("SHELL", shell)
	t.Setenv("TMUX", "")
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte("#!/bin/sh\nexec sleep 600\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	tm := tmux.Client{Socket: os.Getenv("FACTORY_TMUX_SOCKET"), ConfigFile: "/dev/null"}
	t.Cleanup(func() { tm.Run("kill-server") })
	if _, err := tm.NewSession("factory", "host", 120, 40, "exec sleep 600"); err != nil {
		t.Fatal(err)
	}
	repo := t.TempDir()
	cfg := &state.Config{StateDir: os.Getenv("FACTORY_STATE_DIR"), ConfigDir: os.Getenv("FACTORY_CONFIG_DIR"), Repos: []string{repo}}
	f := fixture{cfg: cfg, tm: tm, engine: &Panes{TM: tm, Session: "factory"}, worktree: filepath.Join(factory.WorktreesDir(repo), "csv")}
	f.dir = filepath.Join(f.worktree, ".claude", "factory", "csv")
	f.pipeline = `{"id":"csv","stage":"implement","supervisor":{"tool":"codex"}}`
	if err := WriteFile(filepath.Join(f.dir, "state.json"), f.pipeline); err != nil {
		t.Fatal(err)
	}
	pane, err := f.engine.NewFactoryPane(NewPane{Repo: repo, Slug: "csv", Dir: f.worktree})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.engine.Type(pane, "exec sleep 600"); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(10 * time.Second); f.supervisor.Command != "sleep"; time.Sleep(50 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("the supervisor pane never left its shell: %+v", f.supervisor)
		}
		panes, err := tm.Panes()
		if err != nil {
			t.Fatal(err)
		}
		f.supervisor, _ = FactoryPane(panes, repo, "csv")
	}
	f.hook(t, "Stop")
	return f
}

func (f fixture) hook(t *testing.T, event string) {
	t.Helper()
	body := `{"session_id":"supervisor","cwd":"` + f.worktree + `","hook_event_name":"` + event + `"}`
	if err := state.RecordHook(f.cfg.StateDir, []byte(body), f.supervisor.Token, time.Now()); err != nil {
		t.Fatal(err)
	}
}

func (f fixture) runtime(t *testing.T, typist Typist) *Runtime {
	t.Helper()
	registry, err := state.LoadRegistry(f.cfg.RegistryPath())
	if err != nil {
		t.Fatal(err)
	}
	return New(Runtime{Config: f.cfg, Loader: &Loader{Config: f.cfg, Registry: registry, TM: &f.tm, Now: time.Now}, TM: f.tm, Engine: f.engine, Session: "factory", Typist: typist})
}

func (f fixture) startRequest(op string) Message {
	data, _ := json.Marshal(StageRequest{Action: "start", Token: f.supervisor.Token, Role: "engineer", Text: "Implement the plan."})
	return Message{T: "stage", Op: op, Data: data}
}

func serve(t *testing.T, r *Runtime) (string, func()) {
	t.Helper()
	socketDir, err := os.MkdirTemp("/tmp", "fhost-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(socketDir) })
	socket := filepath.Join(socketDir, "host.sock")
	events := make(chan Event, 16)
	ctx, cancel := context.WithCancel(context.Background())
	server, err := Listen(socket, func(e Event) {
		select {
		case events <- e:
		case <-ctx.Done():
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	stopped := make(chan error, 1)
	go func() { stopped <- r.Run(ctx, events) }()
	stop := func() {
		server.Close()
		cancel()
		if err := <-stopped; err != nil {
			t.Error(err)
		}
	}
	return socket, stop
}

func request(t *testing.T, socket string, m Message) StageReply {
	t.Helper()
	conn, err := net.Dial("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	if err := json.NewEncoder(conn).Encode(m); err != nil {
		t.Fatal(err)
	}
	var reply Message
	if err := json.NewDecoder(conn).Decode(&reply); err != nil {
		t.Fatal(err)
	}
	var stage StageReply
	if reply.T != "done" || json.Unmarshal(reply.Data, &stage) != nil {
		t.Fatalf("stage: %+v", reply)
	}
	return stage
}

func (f fixture) awaitTyped(t *testing.T, calls chan string, want ...string) {
	t.Helper()
	received := map[string]bool{}
	for len(received) < len(want) {
		select {
		case text := <-calls:
			received[text] = true
			f.hook(t, "UserPromptSubmit")
			f.hook(t, "Stop")
		case <-time.After(5 * time.Second):
			t.Fatalf("the detached host stopped delivering: got %v, want %v", received, want)
		}
	}
	for _, text := range want {
		if !received[text] {
			t.Fatalf("typed %v, want %v", received, want)
		}
	}
	select {
	case text := <-calls:
		t.Fatalf("typed %q again", text)
	case <-time.After(1500 * time.Millisecond):
	}
}

func TestHostContinuesAfterClientDisconnect(t *testing.T) {
	f := newFixture(t)
	calls := make(chan string, 8)
	socket, stop := serve(t, f.runtime(t, recordingTypist{calls}))
	first := request(t, socket, f.startRequest(""))
	if err := WriteFile(first.Result, `{"status":"done"}`); err != nil {
		t.Fatal(err)
	}
	if err := WriteFile(filepath.Join(f.dir, "inbox", "ci.json"), `{"text":"CI passed"}`); err != nil {
		t.Fatal(err)
	}
	f.awaitTyped(t, calls, "stage engineer done: "+first.Result, "CI passed")
	stop()

	restarted, stop := serve(t, f.runtime(t, recordingTypist{calls}))
	defer stop()
	second := request(t, restarted, f.startRequest(""))
	if !strings.HasSuffix(second.Result, "engineer-2.result.json") {
		t.Fatalf("a restarted host did not start the next stage: %+v", second)
	}
	if err := WriteFile(second.Result, `{"status":"done"}`); err != nil {
		t.Fatal(err)
	}
	f.awaitTyped(t, calls, "stage engineer done: "+second.Result)
	if data, err := os.ReadFile(filepath.Join(f.dir, "state.json")); err != nil || string(data) != f.pipeline {
		t.Fatal("the host changed supervisor-owned pipeline state")
	}
}

func TestOperationRetryDoesNotRepeatEffects(t *testing.T) {
	t.Run("a retried request returns its receipt instead of starting a second stage", func(t *testing.T) {
		f := newFixture(t)
		r := f.runtime(t, recordingTypist{make(chan string, 8)})
		r.Tick()
		op := uuid.NewString()
		first := r.Handle(f.startRequest(op))
		again := f.startRequest(op)
		var spaced bytes.Buffer
		json.Indent(&spaced, again.Data, "", "  ")
		again.Data = spaced.Bytes()
		retry := r.Handle(again)
		runs, err := state.LoadStageRuns(state.StageRunsPath(f.cfg.StateDir, f.cfg.Repos[0], "csv"))
		if err != nil || len(runs.Runs) != 1 || first.T != "done" || string(retry.Data) != string(first.Data) {
			t.Fatalf("first %+v, retry %+v, runs %+v", first, retry, runs)
		}
	})
	t.Run("a create cut short resumes on retry and a finished one is not run again", func(t *testing.T) {
		dir, op, data := t.TempDir(), uuid.NewString(), json.RawMessage(`{"id":"csv"}`)
		if err := savePrivate(filepath.Join(dir, "operations", op+".json"), Operation{ID: op, Action: "create", Data: data, Status: "accepted"}); err != nil {
			t.Fatal(err)
		}
		runs := 0
		effect := func() Message { runs++; return Message{T: "done"} }
		first, retry := Journal(dir, op, "create", data, true, effect), Journal(dir, op, "create", data, true, effect)
		if runs != 1 || first.T != "done" || retry.T != "done" {
			t.Fatalf("ran the create %d times, replies %+v %+v", runs, first, retry)
		}
	})
	recovered := func(t *testing.T, f fixture, entry QueuedType) (chan string, *Runtime) {
		t.Helper()
		calls := make(chan string, 8)
		r := f.runtime(t, recordingTypist{calls})
		if err := savePrivate(r.queuePath(), []QueuedType{entry}); err != nil {
			t.Fatal(err)
		}
		if err := r.Recover(); err != nil {
			t.Fatal(err)
		}
		r.Tick()
		r.Tick()
		return calls, r
	}
	t.Run("typing cut short by a restart is not typed again, even after the next restart", func(t *testing.T) {
		f := newFixture(t)
		message := filepath.Join(f.dir, "inbox", "ci.json")
		if err := WriteFile(message, `{"text":"CI passed"}`); err != nil {
			t.Fatal(err)
		}
		entry := QueuedType{TypingAt: time.Now(), Pane: f.supervisor.ID, Token: f.supervisor.Token, Label: "csv", Text: "CI passed", Delivers: message, Record: DeliveredPath(f.cfg.StateDir, f.cfg.Repos[0], "csv")}
		calls, r := recovered(t, f, entry)
		if len(calls) != 0 || r.TakeNotice() == "" {
			t.Fatalf("typed %d messages after an interrupted delivery", len(calls))
		}
		calls = make(chan string, 8)
		again := f.runtime(t, recordingTypist{calls})
		if err := again.Recover(); err != nil {
			t.Fatal(err)
		}
		again.Tick()
		again.Tick()
		if len(calls) != 0 {
			t.Fatal("a second restart typed the interrupted message again")
		}
	})
	t.Run("a recovered message is not typed into a new pane that reuses its pane id", func(t *testing.T) {
		f := newFixture(t)
		answer := &Answer{Factory: state.Key(f.cfg.Repos[0], "csv"), ItemID: "q1"}
		calls, _ := recovered(t, f, QueuedType{Pane: f.supervisor.ID, Token: "before-reboot", Label: "csv", Text: "approve", Answers: answer})
		if len(calls) != 0 {
			t.Fatal("typed a recovered message into a pane that belongs to someone else")
		}
	})
}

func TestARemoteAnswerIsTypedOnceAndOnlyIntoTheItemItAnswers(t *testing.T) {
	f := newFixture(t)
	since := time.Date(2026, 11, 12, 14, 2, 0, 123456789, time.UTC)
	withQuestion := `{"id":"csv","stage":"requirements","attention":[{"id":"q1","kind":"question","title":"Scope","created_at":"` + since.Format(time.RFC3339Nano) + `"}]}`
	if err := WriteFile(filepath.Join(f.dir, "state.json"), withQuestion); err != nil {
		t.Fatal(err)
	}
	calls := make(chan string, 8)
	r := f.runtime(t, recordingTypist{calls})
	r.Tick()
	answer := func(item string, at time.Time) Message {
		data, _ := json.Marshal(map[string]any{"Key": state.Key(f.cfg.Repos[0], "csv"), "ItemID": item, "ItemSince": at, "Text": "q1 answers: yes"})
		return r.Handle(Message{T: "answer", Data: data})
	}
	if reply := answer("q1", since); reply.T != "done" || len(calls) != 1 {
		t.Fatalf("an answer from a laptop gave %+v and typed %d times; want it typed once", reply, len(calls))
	}
	for name, reply := range map[string]Message{"a stale item": answer("q1", since.Add(time.Second)), "an unknown item": answer("q9", since)} {
		if reply.T != "error" || len(calls) != 1 {
			t.Errorf("%s gave %+v; want a refusal with nothing typed", name, reply)
		}
	}
}

type stuckPanes struct{ StageEngine }

func (stuckPanes) Pause(string, string) error { return errors.New("tmux could not kill the window") }

func TestADeleteThatCouldNotClosePanesKeepsItsRecordAndFinishesOnRetry(t *testing.T) {
	f := newFixture(t)
	if out, err := exec.Command("git", "init", "-q", f.cfg.Repos[0]).CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	r := f.runtime(t, recordingTypist{make(chan string, 8)})
	r.Tick()
	it, ok := r.Snapshot().Named("csv")
	if !ok {
		t.Fatal("the fixture factory is not listed")
	}
	working := r.Engine
	r.Engine = stuckPanes{working}
	steps, err := r.DeleteFactory(it.Factory)
	activity := state.ActivityPath(f.cfg.ActivityDir(), it.Factory.Repo, "csv")
	if err == nil || !r.Loader.Known(it.Factory.Repo, "csv") || len(steps) < 2 {
		t.Fatalf("a delete whose panes stayed returned %v with %d steps; want the failure, the other cleanup attempted and the record kept for d again", err, len(steps))
	}
	r.Engine = working
	if _, err := r.DeleteFactory(it.Factory); err != nil || r.Loader.Known(it.Factory.Repo, "csv") {
		t.Fatalf("the retry left %v; want the factory gone", err)
	}
	if _, err := os.Stat(activity); err == nil {
		t.Fatal("the retry left the activity log behind")
	}
}

func TestServerNeverStartsBrowserQAForALaptopFactory(t *testing.T) {
	stateDir := t.TempDir()
	clone := filepath.Join(stateDir, "repos", "0123456789abcdef")
	if err := WriteFile(state.RepoConfigPath(clone), "qa:\n  mode: browser\n"); err != nil {
		t.Fatal(err)
	}
	r := New(Runtime{Config: &state.Config{StateDir: stateDir}})
	it := Item{Factory: factory.Factory{Repo: clone, Slug: "csv", Dir: filepath.Join(clone, "f"), Worktree: clone}}
	if _, err := r.StartStage(it, &state.StageRuns{}, StageRequest{Role: "qa"}); err == nil {
		t.Fatal("started browser QA on the server; it belongs to the laptop and must stay held")
	}
	if profile, _ := state.ReadProfile(clone); r.chrome(clone, profile) {
		t.Fatal("a server launch would pass --chrome for a factory created from a laptop")
	}
}

func TestPushWithoutClientExcludesPrivateContent(t *testing.T) {
	var mu sync.Mutex
	var received []string
	answers := []int{http.StatusBadGateway}
	ntfy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		defer mu.Unlock()
		received = append(received, r.URL.Path+" "+r.Header.Get("Title")+" "+string(body))
		if len(answers) > 0 {
			w.WriteHeader(answers[0])
			answers = answers[1:]
		}
	}))
	t.Cleanup(ntfy.Close)
	dir := t.TempDir()
	now := time.Date(2026, 11, 12, 14, 0, 0, 0, time.UTC)
	pusher := &Pusher{StateDir: dir, Now: func() time.Time { return now }}
	settings := remote.PushSettings{Server: ntfy.URL, Topic: "factory-q7rk2m9x", Label: "homelab", Revision: 2}
	if acked, err := pusher.Configure(settings); err != nil || acked != 2 {
		t.Fatalf("configure: %d %v", acked, err)
	}
	if acked, _ := pusher.Configure(remote.PushSettings{Server: ntfy.URL, Label: "homelab", Revision: 1}); acked != 2 {
		t.Fatal("an older settings revision turned push off after a newer one")
	}
	secret := state.Attention{ID: "q-csv-1", Kind: "question", Title: "Paste the prod token sk-live-123?", Body: "sk-live-123", CreatedAt: state.Time{Time: now}}
	m := New(Runtime{Config: &state.Config{StateDir: dir}, Log: LoadEventLog(EventLogPath(dir)), Push: pusher, Now: func() time.Time { return now }})
	m.SetSnapshot(Snapshot{Items: []Item{{Factory: factory.Factory{Repo: "/srv/app", Slug: "csv-export", ID: "csv-export", State: &state.State{ID: "csv-export", Attention: []state.Attention{secret}}}}}})

	m.watch()
	pusher.Flush()
	now = now.Add(pushFirstGap)
	pusher.Flush()
	pusher.Flush()

	mu.Lock()
	defer mu.Unlock()
	want := "/factory-q7rk2m9x factory csv-export on homelab: question"
	if len(received) != 2 || received[0] != want || received[1] != want {
		t.Fatalf("ntfy received %q; want the id, remote and kind only, retried once after the failure and then never again", received)
	}
	stored, _ := os.ReadFile(filepath.Join(dir, "push.json"))
	journal, _ := os.ReadFile(EventLogPath(dir))
	if bytes.Contains(stored, []byte("sk-live")) || bytes.Contains(journal, []byte("sk-live")) {
		t.Fatal("the question's text reached the push outbox or the event journal")
	}
}

func TestARestartedHostStillAcceptsTheLaptopsTunnelPorts(t *testing.T) {
	cfg := &state.Config{StateDir: t.TempDir()}
	listen := func() *review.Server {
		pages, err := review.Listen(0, "token", nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { pages.Close() })
		return pages
	}
	if _, err := New(Runtime{Config: cfg, Pages: listen()}).Link(remote.LinkRequest{Port: 52817}); err != nil {
		t.Fatal(err)
	}
	restarted := listen()
	if err := New(Runtime{Config: cfg, Pages: restarted}).Recover(); err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest("GET", restarted.Origin()+"/favicon.ico", nil)
	req.Host = "127.0.0.1:52817"
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode == http.StatusForbidden {
		t.Fatal("a restarted host refused the laptop's forwarded port, so every open review tab would fail until a new link is opened")
	}
}
