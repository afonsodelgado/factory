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
	if held, err := pusher.Configure(settings); err != nil || held.Revision != 2 {
		t.Fatalf("configure: %+v %v", held, err)
	}
	if held, _ := pusher.Configure(remote.PushSettings{Server: ntfy.URL, Label: "homelab", Revision: 1}); held.Revision != 2 || held.Topic != settings.Topic {
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

func TestHoldingQAGivesUpOnAnOriginThatNeverAnswers(t *testing.T) {
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not installed")
	}
	bin, repo := t.TempDir(), t.TempDir()
	shim := "#!/bin/sh\ncase \"$*\" in *ls-remote*) exec sleep 30;; esac\nexec " + realGit + " \"$@\"\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(shim), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("HOME", t.TempDir())
	for _, args := range [][]string{
		{"init", "-q", "-b", "csv"},
		{"-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "--allow-empty", "-m", "start"},
		{"remote", "add", "origin", "https://git.example/acme/app.git"},
	} {
		if out, err := exec.Command(realGit, append([]string{"-C", repo}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	publishCheckWait = 200 * time.Millisecond
	t.Cleanup(func() { publishCheckWait = 20 * time.Second })
	start := time.Now()
	_, _, _, err = publishedHead(repo)
	if err == nil || !strings.Contains(err.Error(), "did not answer") || time.Since(start) > 5*time.Second {
		t.Fatalf("publishedHead returned %v after %s; want it to give up, since the host serves every request in one loop", err, time.Since(start))
	}
}

func TestPushNamesAFactoryByAPlainIDOnly(t *testing.T) {
	got := pushText("Paste the prod token sk-live-123?", "homelab", "question")
	if strings.Contains(got, "sk-live") || got != "a factory on homelab: question" {
		t.Fatalf("push text %q; an id an agent wrote into state.json must not carry text to a third-party server", got)
	}
	if got := pushText("csv-export", "homelab", "gate"); got != "csv-export on homelab: gate" {
		t.Fatalf("push text %q for a plain id", got)
	}
}

func TestAnUnreadStateFileNeitherResolvesNorReopensAnItem(t *testing.T) {
	question := state.Attention{ID: "q-csv-1", Kind: "question", CreatedAt: state.Time{Time: time.Date(2026, 11, 12, 14, 0, 0, 0, time.UTC)}}
	read := Item{Factory: factory.Factory{Repo: "/srv/app", Slug: "csv", ID: "csv", State: &state.State{ID: "csv", Attention: []state.Attention{question}}}}
	unread := Item{Factory: factory.Factory{Repo: "/srv/app", Slug: "csv", ID: "csv", StateErr: errors.New("unexpected end of JSON input")}}
	log := LoadEventLog(filepath.Join(t.TempDir(), "events.json"))

	if opened := log.Record([]Item{read}); len(opened) != 1 {
		t.Fatalf("first scan opened %d items; want 1", len(opened))
	}
	log.Record([]Item{unread})
	if opened := log.Record([]Item{read}); len(opened) != 0 || log.Seq != 1 {
		t.Fatalf("after a scan that caught state.json mid-write, %d items opened again and the journal is at %d; want none, or the phone and laptop alert again for a question already seen", len(opened), log.Seq)
	}
}

type paneTypist struct{ typed []string }

func (p *paneTypist) Type(pane, text string) error {
	p.typed = append(p.typed, pane+": "+text)
	return nil
}

var phoneNow = time.Date(2026, 11, 16, 9, 0, 0, 0, time.UTC)

func phoneSnapshot(event string, attention ...state.Attention) Snapshot {
	pane := tmux.Pane{ID: "%1", Command: "claude", Token: "sup"}
	f := factory.Factory{Repo: "/r/app", Slug: "csv", State: &state.State{ID: "csv", Stage: "plan", Attention: attention}}
	hook := &state.HookFile{Main: &state.HookEvent{Event: event, At: phoneNow.Add(-time.Minute)}, Last: state.HookEvent{Event: event, At: phoneNow.Add(-time.Minute)}}
	return Snapshot{At: phoneNow, Panes: []tmux.Pane{pane}, Items: []Item{{Factory: f, Status: factory.NeedsYou, Hook: hook, Pane: &pane}}}
}

func phoneRuntime(stateDir string, typist Typist, snap Snapshot) *Runtime {
	r := New(Runtime{Config: &state.Config{StateDir: stateDir}, TM: tmux.Client{Socket: "factory-test-" + uuid.NewString(), ConfigFile: "/dev/null"}, Typist: typist, Now: func() time.Time { return phoneNow }})
	r.Recover()
	r.SetSnapshot(snap)
	return r
}

func asked(id, kind string, questions ...string) state.Attention {
	return state.Attention{ID: id, Kind: kind, Questions: questions, CreatedAt: state.Time{Time: phoneNow.Add(-2 * time.Minute)}}
}

func TestPhoneActionsTypeExactlyWhatTheHubTypes(t *testing.T) {
	items := []state.Attention{asked("q1", "question", "Which region?", "Which tier?"), asked("gate-csv-plan", "gate"), asked("c1", "comment"), asked("cap-csv-qa", "cap"), asked("fail-csv-qa", "failure")}
	since := phoneNow.Add(-2 * time.Minute)
	act := func(action, item string, answers []string, text string) ActionRequest {
		return ActionRequest{Operation: uuid.NewString(), Device: "d1", Factory: "/r/app:csv", Incarnation: "/r/app:csv", Action: action, Item: item, Since: since, Answers: answers, Text: text}
	}
	cases := []struct {
		name    string
		request ActionRequest
		typed   string
	}{
		{"answers keep their order and fold to one line each", act(ActAnswer, "q1", []string{"eu-west", "pro\nwith SSO"}, ""), "%1: q1 answers:\n1) eu-west\n2) pro with SSO"},
		{"approval carries the notes on one line", act(ActApprove, "gate-csv-plan", nil, "ship it,\nkeep retries"), "%1: /factory approve plan gate-csv-plan ship it, keep retries"},
		{"changes", act(ActChanges, "gate-csv-plan", nil, "darker header"), "%1: gate-csv-plan changes: darker header"},
		{"comment reply", act(ActReply, "c1", nil, "done"), "%1: c1 reply: done"},
		{"continue a cap", act(ActContinue, "cap-csv-qa", nil, ""), "%1: cap-csv-qa continue: raise qa_rounds by one for this factory"},
		{"retry a failure", act(ActRetry, "fail-csv-qa", nil, ""), "%1: /factory resume"},
		{"skip a failure", act(ActSkip, "fail-csv-qa", nil, ""), "%1: /factory skip qa"},
		{"handled", act(ActHandled, "c1", nil, ""), "%1: c1 handled: settled outside the panel"},
		{"a message is typed as written", act(ActMessage, "", nil, "/factory status\n  and why?"), "%1: /factory status\n  and why?"},
		{"another incarnation is stale", func() ActionRequest { r := act(ActRetry, "fail-csv-qa", nil, ""); r.Incarnation = "old"; return r }(), ""},
		{"a replaced item is stale", func() ActionRequest {
			r := act(ActRetry, "fail-csv-qa", nil, "")
			r.Since = since.Add(time.Second)
			return r
		}(), ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			typist := &paneTypist{}
			r := phoneRuntime(t.TempDir(), typist, phoneSnapshot("Stop", items...))
			receipt, err := r.Act(c.request)
			got := strings.Join(typist.typed, "\n")
			if err != nil || got != c.typed || (c.typed == "") != (receipt.Status == StatusStale) {
				t.Errorf("typed %q with %+v, %v; want %q: the phone must send the hub's exact text or nothing", got, receipt, err, c.typed)
			}
		})
	}
	tooLong := act(ActMessage, "", nil, strings.Repeat("é", MaxMessageRunes+1))
	if _, err := phoneRuntime(t.TempDir(), &paneTypist{}, phoneSnapshot("Stop")).Act(tooLong); err == nil {
		t.Error("a message over the limit was accepted")
	}
}

func TestAQueuedPhoneAnswerSurvivesARestartAndIsTypedOnce(t *testing.T) {
	dir := t.TempDir()
	question := asked("q1", "question", "Which region?")
	busy := phoneRuntime(dir, &paneTypist{}, phoneSnapshot("UserPromptSubmit", question))
	answer := ActionRequest{Operation: uuid.NewString(), Device: "phone-a", Factory: "/r/app:csv", Incarnation: "/r/app:csv", Action: ActAnswer, Item: "q1", Since: question.CreatedAt.Time, Answers: []string{"eu-west"}}
	if receipt, err := busy.Act(answer); err != nil || receipt.Status != StatusQueued {
		t.Fatalf("an answer to a busy supervisor gave %+v, %v; want it queued", receipt, err)
	}
	second := answer
	second.Operation, second.Device = uuid.NewString(), "phone-b"
	if receipt, _ := busy.Act(second); receipt.Status != StatusStale {
		t.Fatalf("a second device answering a reserved item got %+v; want stale, or the supervisor reads two answers", receipt)
	}
	if claim, ok := busy.Claim(ClaimKey("/r/app:csv", "q1")); !ok || claim.Source != SourcePhone {
		t.Fatal("the hub does not see the phone's claim on the item, so it would offer the form again")
	}

	typist := &paneTypist{}
	restarted := phoneRuntime(dir, typist, phoneSnapshot("UserPromptSubmit", question))
	if receipt, err := restarted.LookupReceipt(ReceiptRequest{Device: "phone-a", Operation: answer.Operation}); err != nil || receipt.Status != StatusQueued {
		t.Fatalf("after a restart the receipt is %+v, %v; want still queued", receipt, err)
	}
	if _, err := restarted.LookupReceipt(ReceiptRequest{Device: "phone-b", Operation: answer.Operation}); err == nil {
		t.Fatal("another device read this phone's receipt")
	}
	idle := phoneSnapshot("Stop", question)
	restarted.SetSnapshot(idle)
	restarted.FlushTypeQueue()
	restarted.FlushTypeQueue()
	if len(typist.typed) != 1 {
		t.Fatalf("typed %q after the restart; want the queued answer exactly once", typist.typed)
	}
	idle.At = phoneNow.Add(2 * time.Second)
	idle.Items[0].Hook.Submit = &state.HookEvent{Event: "UserPromptSubmit", PromptHead: "q1 answers:", At: phoneNow.Add(time.Second)}
	restarted.SetSnapshot(idle)
	restarted.SettleReceipts(idle.Items, func(string) time.Time { return idle.At })
	if receipt, _ := restarted.LookupReceipt(ReceiptRequest{Device: "phone-a", Operation: answer.Operation}); receipt.Status != StatusDelivered {
		t.Fatalf("a received answer reads %+v; want delivered", receipt)
	}
}

type fakeConsole struct {
	screens map[string]string
	after   map[string]string
	pressed []string
}

func (c *fakeConsole) Capture(pane string) (string, error) { return c.screens[pane], nil }

func (c *fakeConsole) Press(pane, key string) error {
	c.pressed = append(c.pressed, pane+": "+key)
	c.screens[pane] = c.after[pane]
	return nil
}

func fixtureScreen(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "permission", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestAPermissionAnswerTypesOnlyIntoTheUnchangedPromptItWasPickedFrom(t *testing.T) {
	bash, refusedScreen := fixtureScreen(t, "bash.txt"), fixtureScreen(t, "refused.txt")
	if _, ok := ParsePermission(fixtureScreen(t, "edit.txt"), "Edit"); ok {
		t.Error("parsed an edit prompt; unknown menus must fall back to answering in the session")
	}
	if menu, ok := ParsePermission(fixtureScreen(t, "bash-box.txt"), "Bash"); !ok || len(menu.Options) != 2 || !menu.Options[1].Tells || menu.Command[0] != "rm -rf build" {
		t.Errorf("boxed Bash prompt parsed to %+v, %v", menu, ok)
	}
	setup := func(screen string) (*Runtime, *fakeConsole, *paneTypist, LocalItem) {
		snap := phoneSnapshot("Stop")
		designer := tmux.Pane{ID: "%5", Command: "claude", Token: "des", Repo: "/r/app", StageOf: "csv", Stage: "designer"}
		hook := &state.HookFile{Prompt: &state.PendingPrompt{At: phoneNow, Tool: "Bash"}, Last: state.HookEvent{At: phoneNow}}
		snap.Items[0].Stage = &StageView{Run: state.StageRun{Role: "designer", Token: "des"}, Pane: &designer, Hook: hook}
		console := &fakeConsole{screens: map[string]string{"%1": "", "%5": screen}, after: map[string]string{"%5": refusedScreen}}
		typist := &paneTypist{}
		r := phoneRuntime(t.TempDir(), typist, snap)
		r.Console = console
		items := r.Local().Factories[0].Items
		return r, console, typist, items[len(items)-1]
	}
	pick := func(item LocalItem, option, text string) ActionRequest {
		fp := ""
		if item.Permission != nil {
			fp = item.Permission.Fingerprint
		}
		return ActionRequest{Operation: uuid.NewString(), Device: "d1", Factory: "/r/app:csv", Incarnation: "/r/app:csv", Action: ActPermission, Item: item.ID, Since: item.Since, Option: option, Fingerprint: fp, Text: text}
	}

	r, console, _, item := setup(bash)
	if item.ID != "permission-designer" || item.Permission == nil || len(item.Permission.Options) != 3 || strings.Contains(item.Body, "cannot") {
		t.Fatalf("the stage's prompt projects as %+v; want its three options, without copy saying the phone cannot answer them", item)
	}
	if receipt, err := r.Act(pick(item, "2", "")); err != nil || receipt.Status != StatusSending || strings.Join(console.pressed, ",") != "%5: 2" {
		t.Fatalf("picking option 2 pressed %v with %+v, %v; want 2 typed into the designer's pane only", console.pressed, receipt, err)
	}

	r, console, _, item = setup(bash)
	console.screens["%5"] = strings.Replace(bash, "npm test -- --run src/export", "rm -rf ~", 1)
	if receipt, _ := r.Act(pick(item, "1", "")); receipt.Status != StatusStale || len(console.pressed) != 0 {
		t.Fatalf("a changed prompt got %+v and presses %v; want nothing typed", receipt, console.pressed)
	}

	r, console, typist, item := setup(bash)
	if _, err := r.Act(pick(item, "3", "")); err == nil || len(console.pressed) != 0 {
		t.Fatal("a refusal without an instruction was typed")
	}
	if _, err := r.Act(pick(item, "3", "use the staging data")); err != nil || strings.Join(typist.typed, ",") != "%5: use the staging data" {
		t.Fatalf("the refusal typed %q; want the instruction once Claude shows its prompt", typist.typed)
	}
}

func TestPauseMarksTheFactoryOnlyWhenThePauseWorked(t *testing.T) {
	f := newFixture(t)
	r := f.runtime(t, recordingTypist{make(chan string, 8)})
	r.Tick()
	it, ok := r.Snapshot().Named("csv")
	if !ok {
		t.Fatal("the fixture factory is not listed")
	}
	paused := func() bool {
		registry, _ := state.LoadRegistry(f.cfg.RegistryPath())
		e := registry.Find(it.Factory.Repo, "csv")
		return e != nil && e.Paused
	}
	working := r.Engine
	r.Engine = stuckPanes{working}
	if err := r.Pause(it.Factory.Key()); err == nil || paused() {
		t.Fatalf("a failed pause returned %v and left the marker %v; want it restored, or a crash reads as a pause and its alerts go silent", err, paused())
	}
	r.Engine = working
	if err := r.Pause(it.Factory.Key()); err != nil || !paused() {
		t.Fatalf("pause returned %v with the marker %v; want it set", err, paused())
	}
	r.Tick()
	if err := r.Wake(it.Factory.Key()); err != nil || paused() {
		t.Fatalf("wake returned %v with the marker %v; want it cleared", err, paused())
	}
}
