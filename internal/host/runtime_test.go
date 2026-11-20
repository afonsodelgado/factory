package host

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/afonsodelgado/factory/internal/catalog"
	"github.com/afonsodelgado/factory/internal/factory"
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
	t.Setenv("CODEX_HOME", filepath.Join(root, "codex"))
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
	f.pipeline = `{"schema_version":2,"id":"csv","stage":"implement","supervisor":{"tool":"codex"},"workflow":{"active_run":"engineer-1","pending":{"id":"engineer-1","kind":"stage","role":"engineer","run_id":"engineer-1"},"runs":[{"id":"engineer-1","stage":"implement","role":"engineer","round":1,"prompt":"stages/engineer-1.prompt.md","result":"stages/engineer-1.result.json"}]}}`
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
	snapshot, _ := state.ReadState(filepath.Join(f.dir, "state.json"))
	data, _ := json.Marshal(StageRequest{EffectID: snapshot.Workflow.ActiveRun, Action: "start", Token: f.supervisor.Token, Role: "engineer", Text: "Implement the plan."})
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
	if err := WriteFile(first.Result, `{"schema_version":2,"run_id":"engineer-1","status":"done"}`); err != nil {
		t.Fatal(err)
	}
	if err := WriteFile(filepath.Join(f.dir, "inbox", "ci.json"), `{"text":"CI passed"}`); err != nil {
		t.Fatal(err)
	}
	f.awaitTyped(t, calls, "stage engineer done: "+first.Result, "CI passed")
	data, _ := json.Marshal(StageRequest{EffectID: "engineer-1", Action: "send", Reask: true, Token: f.supervisor.Token, Role: "engineer", Text: "The engine rejected your result."})
	reasked := request(t, socket, Message{T: "stage", Data: data})
	if reasked.Result == first.Result {
		t.Fatal("a re-ask handed back the rejected result instead of asking the agent again")
	}
	if err := WriteFile(reasked.Result, `{"schema_version":2,"run_id":"engineer-1","status":"done"}`); err != nil {
		t.Fatal(err)
	}
	if message, _ := os.ReadFile(filepath.Join(f.dir, "stages", "engineer-1.message.md")); !strings.Contains(string(message), reasked.Result) {
		t.Fatal("the re-ask did not tell the agent where to write its corrected result")
	}
	f.awaitTyped(t, calls, "stage engineer done: "+reasked.Result)
	stop()

	f.pipeline = strings.ReplaceAll(f.pipeline, "engineer-1", "engineer-2")
	if err := WriteFile(filepath.Join(f.dir, "state.json"), f.pipeline); err != nil {
		t.Fatal(err)
	}
	restarted, stop := serve(t, f.runtime(t, recordingTypist{calls}))
	defer stop()
	second := request(t, restarted, f.startRequest(""))
	if !strings.HasSuffix(second.Result, "engineer-2.result.json") {
		t.Fatalf("a restarted host did not start the next stage: %+v", second)
	}
	if err := WriteFile(second.Result, `{"schema_version":2,"run_id":"engineer-2","status":"done"}`); err != nil {
		t.Fatal(err)
	}
	f.awaitTyped(t, calls, "stage engineer done: "+second.Result)
	if data, err := os.ReadFile(filepath.Join(f.dir, "state.json")); err != nil || string(data) != f.pipeline {
		t.Fatal("the host changed supervisor-owned pipeline state")
	}
}

type promptSubmittingTypist struct {
	t     *testing.T
	f     fixture
	typed []string
}

func (p *promptSubmittingTypist) Type(pane, text string) error {
	p.typed = append(p.typed, text)
	p.f.hook(p.t, "UserPromptSubmit")
	p.f.hook(p.t, "Stop")
	return nil
}

func TestASupervisorThatSubmitsBeforeTypingReturnsStillGetsItsNextMessage(t *testing.T) {
	f := newFixture(t)
	typist := &promptSubmittingTypist{t: t, f: f}
	r := f.runtime(t, typist)
	for _, name := range []string{"ci", "review"} {
		if err := WriteFile(filepath.Join(f.dir, "inbox", name+".json"), `{"text":"`+name+` passed"}`); err != nil {
			t.Fatal(err)
		}
	}
	r.Tick()
	r.Tick()
	if !slices.Equal(typist.typed, []string{"ci passed", "review passed"}) {
		t.Fatalf("typed %q; a prompt submitted while typing was still returning held the next message back for a minute", typist.typed)
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
	t.Run("a delete accepted before a crash answers uncertain instead of deleting again", func(t *testing.T) {
		dir, op, data := t.TempDir(), uuid.NewString(), json.RawMessage(`{"Repo":"/r/app","Slug":"csv"}`)
		if err := SavePrivate(filepath.Join(dir, "operations", op+".json"), Operation{ID: op, Action: "delete", Data: data, Status: "accepted"}); err != nil {
			t.Fatal(err)
		}
		if reply := New(Runtime{Config: &state.Config{StateDir: dir}}).Handle(Message{T: "delete", Op: op, Data: data}); reply.T != "uncertain" {
			t.Fatalf("a retried interrupted delete gave %+v; want uncertain, never a second removal", reply)
		}
	})
	recovered := func(t *testing.T, f fixture, entry QueuedType) (chan string, *Runtime) {
		t.Helper()
		calls := make(chan string, 8)
		r := f.runtime(t, recordingTypist{calls})
		if err := SavePrivate(r.queuePath(), []QueuedType{entry}); err != nil {
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

type paneTypist struct{ typed []string }

func (p *paneTypist) Type(pane, text string) error {
	p.typed = append(p.typed, pane+": "+text)
	return nil
}

var phoneNow = time.Date(2026, 11, 16, 9, 0, 0, 0, time.UTC)

func phoneSnapshot(event string, attention ...state.Attention) Snapshot {
	pane := tmux.Pane{ID: "%1", Command: "claude", Token: "sup"}
	f := factory.Factory{Repo: "/r/app", Slug: "csv", State: &state.State{SchemaVersion: 2, ID: "csv", Stage: "plan", Attention: attention}}
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
	stage := ""
	if kind == "gate" {
		stage = "plan"
	} else if kind == "cap" || kind == "failure" {
		stage = "qa"
	}
	return state.Attention{StageKey: stage, Loop: "qa_fix", LimitKey: "qa_fix_rounds", ID: id, Kind: kind, Questions: questions, CreatedAt: state.Time{Time: phoneNow.Add(-2 * time.Minute)}}
}

func TestPhoneActionsTypeExactlyWhatTheHubTypes(t *testing.T) {
	items := []state.Attention{asked("q1", "question", "Which region?", "Which tier?"), asked("gate-csv-plan", "gate"), asked("c1", "comment"), asked("cap-csv-qa", "cap"), asked("fail-csv-qa", "failure"), asked("handoff-csv-1", "handoff")}
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
		{"continue a cap", act(ActContinue, "cap-csv-qa", nil, ""), "%1: cap-csv-qa continue: raise qa_fix_rounds by one for this factory"},
		{"retry a failure", act(ActRetry, "fail-csv-qa", nil, ""), "%1: /factory resume"},
		{"skip a failure", act("skip", "fail-csv-qa", nil, ""), ""},
		{"handled", act(ActHandled, "c1", nil, ""), "%1: c1 handled: settled outside the panel"},
		{"mark a handoff done", act(ActHandled, "handoff-csv-1", nil, ""), "%1: handoff-csv-1 done: the developer marked this factory done"},
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
			if c.request.Action == "skip" {
				if err == nil || got != "" {
					t.Fatal("skip bypassed completion", receipt, err)
				}
				return
			}
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

type wakingEngine struct{ created, typed []string }

func (e *wakingEngine) NewFactoryPane(p NewPane) (string, error) {
	e.created = append(e.created, p.Slug)
	return "%9", nil
}
func (e *wakingEngine) Pause(string, string) error              { return nil }
func (e *wakingEngine) NewStagePane(StagePane) (string, error)  { return "", nil }
func (e *wakingEngine) KillStages(string, string, string) error { return nil }
func (e *wakingEngine) Type(pane, text string) error {
	e.typed = append(e.typed, pane+": "+text)
	return nil
}

func TestContinuingAFinishedFactoryFromThePhoneWakesItsGoneSupervisorAndTypesItOnce(t *testing.T) {
	handoff := asked("handoff-csv-1", "handoff")
	waitingWith := func(pane *tmux.Pane) Snapshot {
		snap := phoneSnapshot("Stop", handoff)
		it := &snap.Items[0]
		it.Factory.State.Stage = "handoff"
		it.Factory.State.Stages = map[string]state.Stage{"handoff": {Status: "running"}}
		it.Factory.Worktree = t.TempDir()
		it.Status, it.Pane = factory.Gone, pane
		snap.Panes = nil
		if pane != nil {
			snap.Panes = []tmux.Pane{*pane}
		}
		return snap
	}
	engine, typist := &wakingEngine{}, &paneTypist{}
	r := phoneRuntime(t.TempDir(), typist, waitingWith(nil))
	r.Engine = engine
	keepGoing := ActionRequest{Operation: uuid.NewString(), Factory: "/r/app:csv", Incarnation: "/r/app:csv", Action: ActContinue, Item: "handoff-csv-1", Since: handoff.CreatedAt.Time}
	if _, err := r.Act(keepGoing); err == nil || len(engine.created) != 0 {
		t.Fatalf("continuing with nothing to do woke %v, %v; want it refused until the developer says what to do next", engine.created, err)
	}
	keepGoing.Text = "Also cap the export at 10,000 rows."
	if receipt, err := r.Act(keepGoing); err != nil || receipt.Status != StatusQueued || len(engine.created) != 1 {
		t.Fatalf("continuing a finished factory with no supervisor running gave %+v, %v and woke %v; want a fresh supervisor and the message queued for it", receipt, err, engine.created)
	}
	r.Engine, r.Now = nil, func() time.Time { return phoneNow.Add(time.Minute) }
	r.SetSnapshot(waitingWith(&tmux.Pane{ID: "%9", Command: "claude"}))
	r.FlushTypeQueue()
	r.FlushTypeQueue()
	if want := "%9: handoff-csv-1 continue: Also cap the export at 10,000 rows."; strings.Join(typist.typed, "\n") != want {
		t.Fatalf("typed %q into the woken supervisor; want %q once", typist.typed, want)
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
	if receipt, err := restarted.LookupReceipt(ReceiptRequest{Operation: answer.Operation}); err != nil || receipt.Status != StatusQueued {
		t.Fatalf("after a restart the owner reads a paired phone's receipt as %+v, %v; want still queued", receipt, err)
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
	if receipt, _ := restarted.LookupReceipt(ReceiptRequest{Operation: answer.Operation}); receipt.Status != StatusDelivered {
		t.Fatalf("a received answer reads %+v; want delivered", receipt)
	}
}

func TestAnAnswerQueuedForASupervisorThatNeverLooksIdleIsGivenBack(t *testing.T) {
	handoff := asked("handoff-csv-1", "handoff")
	typist := &paneTypist{}
	r := phoneRuntime(t.TempDir(), typist, phoneSnapshot("PostToolUse", handoff))
	now := phoneNow
	r.Now = func() time.Time { return now }
	markDone := ActionRequest{Operation: uuid.NewString(), Device: "d1", Factory: "/r/app:csv", Incarnation: "/r/app:csv", Action: ActHandled, Item: handoff.ID, Since: handoff.CreatedAt.Time}
	if receipt, err := r.Act(markDone); err != nil || receipt.Status != StatusQueued {
		t.Fatalf("marking done while the supervisor looks busy gave %+v, %v; want it queued", receipt, err)
	}
	now = phoneNow.Add(AnswerWait - time.Minute)
	r.FlushTypeQueue()
	if receipt, _ := r.LookupReceipt(ReceiptRequest{Operation: markDone.Operation}); receipt.Status != StatusQueued {
		t.Fatalf("before the wait ran out the answer reads %+v; want still queued for the supervisor's next idle moment", receipt)
	}
	now = phoneNow.Add(AnswerWait + time.Minute)
	r.FlushTypeQueue()
	receipt, _ := r.LookupReceipt(ReceiptRequest{Operation: markDone.Operation})
	if receipt.Status != StatusRefused || !strings.Contains(receipt.Reason, "did not come back to its prompt") || len(typist.typed) != 0 {
		t.Fatalf("after the wait the answer reads %+v and typed %q; want it given back untyped with the reason, not left queued for hours behind a running status", receipt, typist.typed)
	}
}

func TestAPhoneMessageShowsOnlyTheSupervisorsReplyToItsOwnTurn(t *testing.T) {
	r := phoneRuntime(t.TempDir(), &paneTypist{}, phoneSnapshot("Stop"))
	send := func(text string) string {
		request := ActionRequest{Operation: uuid.NewString(), Factory: "/r/app:csv", Incarnation: "/r/app:csv", Action: ActMessage, Text: text}
		if _, err := r.Act(request); err != nil {
			t.Fatal(err)
		}
		return request.Operation
	}
	at := func(seconds int) time.Time { return phoneNow.Add(time.Duration(seconds) * time.Second) }
	submit := func(seconds int, head string) state.HookEvent {
		return state.HookEvent{Event: "UserPromptSubmit", PromptHead: head, At: at(seconds)}
	}
	settle := func(seconds int, submits []state.HookEvent, replies []state.Reply) {
		snap := phoneSnapshot("Stop")
		snap.At = at(seconds)
		hook := snap.Items[0].Hook
		hook.Submits, hook.Submit, hook.Replies = submits, &submits[len(submits)-1], replies
		hook.Main = &state.HookEvent{Event: "Stop", At: replies[len(replies)-1].At}
		hook.Last = *hook.Main
		r.SetSnapshot(snap)
		r.SettleReceipts(snap.Items, func(string) time.Time { return snap.At })
	}
	submits := []state.HookEvent{submit(1, "why is QA slow?")}
	replies := []state.Reply{{At: at(-30), Text: "an earlier turn"}, {At: at(2), Text: "QA waits on CI."}}
	question := send("why is QA slow?")
	settle(2, submits, replies)
	interrupted := send("and the plan?")
	r.FlushTypeQueue()
	submits = append(submits, submit(3, "and the plan?"), submit(4, "typed in the hub"))
	replies = append(replies, state.Reply{At: at(5), Text: "answer to the hub"})
	settle(6, submits, replies)

	if receipt, _ := r.LookupReceipt(ReceiptRequest{Operation: question}); receipt.Reply == nil || receipt.Reply.Text != "QA waits on CI." {
		t.Errorf("the question's receipt carries reply %+v; want the supervisor's answer to that turn, not an earlier one", receipt.Reply)
	}
	if receipt, _ := r.LookupReceipt(ReceiptRequest{Operation: interrupted}); receipt.Status != StatusDelivered || receipt.Reply != nil {
		t.Errorf("a message whose turn was cut short by a hub prompt reads %+v; want delivered with no reply, not the hub prompt's answer", receipt)
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

	clock := phoneNow
	promptOn := func(screen, after string) (*Runtime, *fakeConsole, func() []LocalItem) {
		unhooked := phoneSnapshot("Stop")
		unhooked.Items[0].Hook = nil
		r := phoneRuntime(t.TempDir(), &paneTypist{}, unhooked)
		console := &fakeConsole{screens: map[string]string{"%1": screen}, after: map[string]string{"%1": after}}
		r.Console, r.Now = console, func() time.Time { return clock }
		return r, console, func() []LocalItem {
			r.ReadScreens()
			return slices.DeleteFunc(r.Local().Factories[0].Items, func(li LocalItem) bool { return li.Kind != "prompt" })
		}
	}
	choose := func(item LocalItem, option string) ActionRequest {
		return ActionRequest{Operation: uuid.NewString(), Device: "d1", Factory: "/r/app:csv", Incarnation: "/r/app:csv", Action: ActPrompt, Item: item.ID, Since: item.Since, Option: option, Fingerprint: item.Menu.Fingerprint}
	}

	mcp := fixtureScreen(t, "mcp.txt")
	r, console, prompts := promptOn(mcp, "")
	first := prompts()
	clock = clock.Add(6 * time.Second)
	again := prompts()
	if len(first) != 1 || len(again) != 1 || first[0].Key != again[0].Key || first[0].Menu == nil || len(first[0].Menu.Choices) != 3 {
		t.Fatalf("a pane with no hooks showing a menu gave %+v, then %+v; want one prompt item with three choices and the same key on every tick, or the phone is notified again", first, again)
	}
	r.readings, r.promptsSeen = nil, nil
	clock = clock.Add(time.Minute)
	if restarted := prompts(); len(restarted) != 1 || restarted[0].Key != first[0].Key || !restarted[0].Since.Equal(first[0].Since) {
		t.Fatalf("after a node restart the unchanged menu became %+v; want the same key and wait time, or every update notifies the phone again", restarted)
	}
	console.screens["%1"] = strings.Replace(mcp, "playwright", "filesystem", 1)
	if receipt, _ := r.Act(choose(first[0], "2")); receipt.Status != StatusStale || len(console.pressed) != 0 {
		t.Fatalf("a menu that changed after it was loaded got %+v and presses %v; want nothing typed", receipt, console.pressed)
	}
	console.screens["%1"] = mcp
	if receipt, err := r.Act(choose(first[0], "2")); err != nil || receipt.Status == StatusStale || strings.Join(console.pressed, ",") != "%1: 2" {
		t.Fatalf("choosing 2 on the unchanged menu pressed %v with %+v, %v; want only 2 typed into the supervisor's pane", console.pressed, receipt, err)
	}

	codex := fixtureScreen(t, "codex-update.txt")
	cursorOnSkip := strings.Replace(strings.Replace(codex, "› 1.", "  1.", 1), "  2. Skip\n", "› 2. Skip\n", 1)
	r, console, prompts = promptOn(codex, cursorOnSkip)
	if _, err := r.Act(choose(prompts()[0], "2")); err != nil || strings.Join(console.pressed, ",") != "%1: 2,%1: Enter" {
		t.Fatalf("a menu whose digit only moves the cursor got presses %v, %v; want 2 then Enter, or the choice is never made", console.pressed, err)
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

func TestRuntimeActionsLeaveStateJSONToTheSupervisor(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	written := `{"schema_version":2,"id":"csv","attention":[{"id":"q-csv-1","kind":"question","resolved_at":null}]}`
	if err := os.WriteFile(path, []byte(written), 0o644); err != nil {
		t.Fatal(err)
	}
	question := asked("q-csv-1", "question", "Which region?")
	snap := phoneSnapshot("Stop", question)
	snap.Items[0].Factory.Dir = dir
	typist := &paneTypist{}
	r := phoneRuntime(t.TempDir(), typist, snap)
	call := func(kind string, payload any) Message {
		data, _ := json.Marshal(payload)
		return r.Handle(Message{T: kind, Op: uuid.NewString(), Data: data})
	}

	answer := ActionRequest{Operation: uuid.NewString(), Factory: "/r/app:csv", Incarnation: "/r/app:csv", Action: ActAnswer, Item: "q-csv-1", Since: question.CreatedAt.Time, Answers: []string{"eu"}}
	data, _ := json.Marshal(answer)
	acted := r.Handle(Message{T: "act", Op: answer.Operation, Data: data})
	resolved := call("resolve", ResolveRequest{Dir: dir, ID: "q-csv-1", WaitingOn: "agent"})
	var instruction string
	json.Unmarshal(resolved.Data, &instruction)
	if after, _ := os.ReadFile(path); acted.T != "done" || len(typist.typed) != 1 || string(after) != written {
		t.Fatalf("answer %+v typed %q and left state.json as %s; want the answer typed and state.json untouched", acted, typist.typed, after)
	}
	if resolved.T != "done" || !strings.Contains(instruction, path) || !strings.Contains(instruction, `"resolved_at"`) || !strings.Contains(instruction, `"waiting_on" to "agent"`) {
		t.Fatalf("resolve answered %+v; want an instruction naming the file and fields for the supervisor to write", resolved)
	}

	snap.Items[0].Factory.State.Attention[0].ResolvedAt = state.Time{Time: phoneNow}
	r.SetSnapshot(snap)
	if again := call("resolve", ResolveRequest{Dir: dir, ID: "q-csv-1"}); again.T != "error" {
		t.Fatalf("resolving an item the supervisor already closed gave %+v; want a refusal", again)
	}
}

func TestAnAnswerFromTheHubNeverRetypesAnItemAlreadyAnswered(t *testing.T) {
	question := asked("q-csv-1", "question", "Which region?")
	typist := &paneTypist{}
	r := phoneRuntime(t.TempDir(), typist, phoneSnapshot("Stop", question))
	answer := ActionRequest{Operation: uuid.NewString(), Factory: "/r/app:csv", Incarnation: "/r/app:csv", Action: ActAnswer, Item: "q-csv-1", Since: question.CreatedAt.Time, Answers: []string{"eu"}}
	data, _ := json.Marshal(answer)
	r.Handle(Message{T: "act", Op: answer.Operation, Data: data})
	again, _ := json.Marshal(QueuedAnswer{Answer: Answer{Factory: "/r/app:csv", ItemID: "q-csv-1", ItemSince: question.CreatedAt.Time, Source: SourceHub, Action: ActAnswer}, Text: "q-csv-1 answers: us"})
	r.Handle(Message{T: "queue-answer", Data: again})
	if len(typist.typed) != 1 {
		t.Fatalf("typed %q; a hub answer for an item the phone already answered must not be typed again", typist.typed)
	}
}

func TestPRCIFollowsTheLatestRunOfEachCheck(t *testing.T) {
	cases := []struct {
		name   string
		rollup string
		want   string
	}{
		{"cancelled run superseded by a pass", `[
			{"workflowName":"CI","name":"ci","status":"COMPLETED","conclusion":"CANCELLED","startedAt":"2026-11-04T14:41:24Z"},
			{"workflowName":"CI","name":"ci","status":"COMPLETED","conclusion":"SUCCESS","startedAt":"2026-11-04T15:04:48Z"},
			{"context":"deploy","state":"SUCCESS","createdAt":"2026-11-04T15:00:00Z"}]`, "pass"},
		{"pass superseded by a failure", `[
			{"workflowName":"CI","name":"ci","status":"COMPLETED","conclusion":"FAILURE","startedAt":"2026-11-04T15:04:48Z"},
			{"workflowName":"CI","name":"ci","status":"COMPLETED","conclusion":"SUCCESS","startedAt":"2026-11-04T14:41:24Z"}]`, "fail"},
		{"no checks at all", `[]`, "none"},
		{"only skipped checks", `[
			{"workflowName":"CI","name":"ci","status":"COMPLETED","conclusion":"SKIPPED","startedAt":"2026-11-04T15:04:48Z"}]`, "pass"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gh := &GitHub{Run: func(string, ...string) ([]byte, error) {
				return []byte(`{"number":7,"statusCheckRollup":` + c.rollup + `}`), nil
			}}
			pr := gh.Statuses("/repo", []int{7}, time.Now(), true)[0]
			if pr.CI != c.want {
				t.Fatalf("CI = %q, want %q", pr.CI, c.want)
			}
		})
	}
}

func TestAnOldFactoryGetsItsIncarnationOnceAndKeepsItAcrossRestarts(t *testing.T) {
	repo := t.TempDir()
	cfg := &state.Config{StateDir: t.TempDir(), Repos: []string{repo}}
	legacy, _ := state.LoadRegistry(cfg.RegistryPath())
	legacy.Upsert(state.Entry{Repo: repo, Slug: "csv", Created: true, CreatedAt: phoneNow})
	if err := legacy.Save(); err != nil {
		t.Fatal(err)
	}
	incarnation := func() string {
		registry, err := state.LoadRegistry(cfg.RegistryPath())
		if err != nil {
			t.Fatal(err)
		}
		it, _ := (&Loader{Config: cfg, Registry: registry, Now: time.Now}).Load().Find(state.Key(repo, "csv"))
		return Incarnation(it.Factory)
	}
	first := incarnation()
	if uuid.Validate(first) != nil {
		t.Fatalf("an old factory kept %q as its incarnation; want a UUID from the registry", first)
	}
	if again := incarnation(); again != first {
		t.Errorf("the incarnation changed from %s to %s on the next load", first, again)
	}
}

func TestACheckpointIsAskedOfTheSupervisorOnceAndWaitsForUnsettledAnswers(t *testing.T) {
	snap := phoneSnapshot("Stop", asked("q1", "question", "Which region?"))
	snap.Items[0].Hook.Token = "sup"
	snap.Hooks = []*state.HookFile{snap.Items[0].Hook}
	f := &snap.Items[0].Factory
	f.Worktree = t.TempDir()
	f.Dir = filepath.Join(f.Worktree, ".claude", "factory", "csv")
	typist := &paneTypist{}
	r := phoneRuntime(t.TempDir(), typist, snap)
	op := uuid.NewString()
	request := CheckpointRequest{Key: f.Key(), Operation: op}
	for range 2 {
		movable, err := r.Checkpoint(request)
		if err != nil || movable.Waiting == "" {
			t.Fatalf("asking for a checkpoint answered %+v, %v; want it to wait for the supervisor", movable, err)
		}
	}
	if want := []string{"%1: /factory checkpoint " + op}; !slices.Equal(typist.typed, want) {
		t.Fatalf("typed %q; want the checkpoint asked once", typist.typed)
	}
	f.State.Checkpoint = &state.Checkpoint{Operation: op, Status: state.CheckpointReady}
	r.SetSnapshot(snap)
	answer := Answer{Factory: f.Key(), ItemID: "q1", ItemSince: phoneNow, At: phoneNow}
	r.PutClaim(answer)
	if _, err := r.Checkpoint(request); err == nil || !strings.Contains(err.Error(), "q1") {
		t.Fatalf("a checkpoint with an unsettled answer answered %v; want it blocked until the answer settles", err)
	}
	r.DropClaim(answer.Key())
	if movable, err := r.Checkpoint(request); err != nil || movable.Waiting != "" || movable.Dir != f.Dir {
		t.Errorf("a ready checkpoint answered %+v, %v", movable, err)
	}
}

func TestAMovedFactoryStartsAFreshSupervisorAndItsOldHolderRefusesStaleOrUncheckedAnswers(t *testing.T) {
	repo := t.TempDir()
	cfg := &state.Config{StateDir: t.TempDir(), Repos: []string{repo}}
	registry, _ := state.LoadRegistry(cfg.RegistryPath())
	engine := &wakingEngine{}
	r := New(Runtime{Config: cfg, Loader: &Loader{Config: cfg, Registry: registry, Now: time.Now}, Engine: engine, Now: time.Now})
	incarnation, key := uuid.NewString(), state.Key(repo, "csv")
	if _, err := r.Adopt(AdoptRequest{Repo: repo, Slug: "csv", Incarnation: incarnation}); err != nil {
		t.Fatal(err)
	}
	stateFile := filepath.Join(factory.WorktreesDir(repo), "csv", ".claude", "factory", "csv", "state.json")
	if err := WriteFile(stateFile, `{"schema_version":2,"id":"csv","session_id":"old-session"}`); err != nil {
		t.Fatal(err)
	}
	if _, err := r.LaunchMoved(key); err != nil {
		t.Fatal(err)
	}
	launched := strings.Join(engine.typed, "\n")
	if !strings.Contains(launched, "--session-id") || !strings.HasSuffix(launched, "'/factory resume'") || strings.Contains(launched, "--resume") || strings.Contains(launched, "--continue") || strings.Contains(launched, "old-session") {
		t.Fatalf("a moved factory started with %q; want a fresh session typed /factory resume, never the old conversation", launched)
	}
	if local := r.Local().Factories; len(local) != 1 || local[0].Incarnation != incarnation {
		t.Fatalf("the moved factory lists as %+v; want its incarnation kept across the move", local)
	}
	answer := ActionRequest{Operation: uuid.NewString(), Factory: key, Incarnation: incarnation, Action: ActMessage, Text: "status?", Generation: 1}
	r.SetHolding(HoldingRequest{Key: key, Holding: Holding{Generation: 2}})
	if receipt, _ := r.Act(answer); receipt.Status != StatusStale {
		t.Errorf("an answer loaded at generation 1 after the holder moved to generation 2 gave %+v; want it refused as stale", receipt)
	}
	r.SetHolding(HoldingRequest{Key: key, Holding: Holding{Unverified: "cannot check the holder"}})
	answer.Operation, answer.Generation = uuid.NewString(), 0
	if receipt, _ := r.Act(answer); receipt.Status != StatusRefused || len(engine.typed) != 1 {
		t.Errorf("an answer while the holder is unchecked gave %+v and typed %q; want it refused", receipt, engine.typed)
	}
	if err := r.Release(ReleaseRequest{Repo: repo, Slug: "csv", To: "homelab"}); err != nil {
		t.Fatal(err)
	}
	reloaded, _ := state.LoadRegistry(cfg.RegistryPath())
	restarted := New(Runtime{Config: cfg, Loader: &Loader{Config: cfg, Registry: reloaded, Now: time.Now}, Engine: engine, Now: time.Now})
	restarted.Tick()
	if local := restarted.Local().Factories; len(local) != 0 || restarted.Wake(key) != nil || len(engine.created) != 1 {
		t.Errorf("after a restart the released factory lists as %+v and woke %v; want this node to stop offering it", local, engine.created)
	}
}

func TestANewFactoryTellsItsSupervisorEveryStageAndRoleAsTheAppResolvedThem(t *testing.T) {
	t.Setenv("CODEX_HOME", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	repo := t.TempDir()
	cfg := &state.Config{StateDir: t.TempDir(), Repos: []string{repo}, Agents: map[string]state.AgentSetting{
		"reviewer": {Model: "sonnet"},
		"ci":       {Tool: state.ToolCodex, Model: "gpt-6-astra", Effort: "medium"},
	}}
	registry, _ := state.LoadRegistry(cfg.RegistryPath())
	engine := &wakingEngine{}
	r := New(Runtime{Config: cfg, Loader: &Loader{Config: cfg, Registry: registry, Now: time.Now}, Engine: engine, Now: time.Now})
	if _, err := r.Create(CreateRequest{Repo: repo, Slug: "csv", Prompt: "Export contacts.", Agents: map[string]state.AgentSetting{"engineer": {Effort: "max"}}}); err != nil {
		t.Fatal(err)
	}
	launched := strings.Join(engine.typed, "\n")
	for _, flag := range []string{"--pipeline full --stages requirements,design,plan,implement,", "--agent reviewer=claude/sonnet:high", "--agent ci=codex/gpt-6-astra:medium", "--agent engineer=claude/opus:max", "--agent analyst=claude/opus:high"} {
		if !strings.Contains(launched, flag) {
			t.Errorf("the supervisor was started with %q; want %s, so it never has to resolve the config itself", launched, flag)
		}
	}
}

func TestCreatingAFactoryTrustsItsRepoAndWorktreeAndNoOtherFolder(t *testing.T) {
	claudeDir, codexHome := t.TempDir(), t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", claudeDir)
	t.Setenv("CODEX_HOME", codexHome)
	claudeFile, codexFile := filepath.Join(claudeDir, ".claude.json"), filepath.Join(codexHome, "config.toml")
	os.WriteFile(claudeFile, []byte(`{"oauthAccount":{"emailAddress":"alice@example.com"}}`), 0o600)
	os.WriteFile(codexFile, []byte(`model = "gpt-6-astra"`), 0o600)
	repo := t.TempDir()
	worktree := filepath.Join(factory.WorktreesDir(repo), "csv")
	cfg := &state.Config{StateDir: t.TempDir(), Repos: []string{repo}}
	registry, _ := state.LoadRegistry(cfg.RegistryPath())
	r := New(Runtime{Config: cfg, Loader: &Loader{Config: cfg, Registry: registry, Now: time.Now}, Engine: &wakingEngine{}, Now: time.Now})
	if _, err := r.Create(CreateRequest{Repo: repo, Slug: "csv", Prompt: "Export contacts."}); err != nil {
		t.Fatal(err)
	}

	var claude struct {
		OAuthAccount map[string]string `json:"oauthAccount"`
		Projects     map[string]struct {
			HasTrustDialogAccepted bool `json:"hasTrustDialogAccepted"`
		} `json:"projects"`
	}
	data, _ := os.ReadFile(claudeFile)
	if err := json.Unmarshal(data, &claude); err != nil || claude.OAuthAccount["emailAddress"] == "" {
		t.Fatalf("Claude's config lost its unrelated keys (%v):\n%s", err, data)
	}
	managed := map[string]bool{repo: true, worktree: true, resolved(repo): true, filepath.Join(resolved(repo), ".claude", "worktrees", "csv"): true}
	for _, dir := range []string{resolved(repo), filepath.Join(resolved(repo), ".claude", "worktrees", "csv")} {
		if !claude.Projects[dir].HasTrustDialogAccepted {
			t.Errorf("Claude Code would ask whether to trust %s:\n%s", dir, data)
		}
	}
	for dir := range claude.Projects {
		if !managed[dir] {
			t.Errorf("Claude Code now trusts %s, which factory does not manage", dir)
		}
	}
	codex, _ := os.ReadFile(codexFile)
	if !strings.Contains(string(codex), `model = "gpt-6-astra"`) || !catalog.CodexTrusts(codexHome, repo) || !catalog.CodexTrusts(codexHome, worktree) {
		t.Errorf("Codex's config should keep its model and trust the repo and worktree:\n%s", codex)
	}
	if strings.Count(string(codex), "[projects.") > len(managed) {
		t.Errorf("Codex now trusts a folder factory does not manage:\n%s", codex)
	}
}

func TestAGateWhoseCommentsTheDesignerIsWorkingOnDoesNotNeedYou(t *testing.T) {
	gateOpened := time.Now().Add(-time.Hour)
	sent := gateOpened.Add(30 * time.Minute)
	gate := state.Attention{ID: "gate-csv-design", Kind: "gate", StageKey: "design", CreatedAt: state.Time{Time: gateOpened}}
	question := state.Attention{ID: "q-csv-1", Kind: "question", CreatedAt: state.Time{Time: gateOpened}}
	designer := tmux.Pane{ID: "%2", Command: "claude", Repo: "/r/app", StageOf: "csv", Stage: "designer", Token: "t-des"}
	stopHook := func(at time.Time) *state.HookFile {
		event := state.HookEvent{Event: "Stop", At: at}
		return &state.HookFile{Token: "t-des", Main: &event, Last: event}
	}
	cases := []struct {
		name      string
		attention []state.Attention
		finished  bool
		pane      tmux.Pane
		hook      *state.HookFile
		want      factory.Status
	}{
		{"comments sent, the designer's last stop was before them", []state.Attention{gate}, false, designer, stopHook(sent.Add(-time.Minute)), factory.WaitingAgent},
		{"the designer wrote its result", []state.Attention{gate}, true, designer, nil, factory.NeedsYou},
		{"the designer stopped after the comments", []state.Attention{gate}, false, designer, stopHook(sent.Add(time.Minute)), factory.NeedsYou},
		{"the designer's pane died", []state.Attention{gate}, false, tmux.Pane{ID: "%2", Dead: true, Repo: "/r/app", StageOf: "csv", Stage: "designer", Token: "t-des"}, nil, factory.NeedsYou},
		{"a question is open too", []state.Attention{gate, question}, false, designer, nil, factory.NeedsYou},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			result := filepath.Join(dir, "designer-2.result.json")
			if c.finished {
				os.WriteFile(result, []byte(`{"schema_version":2,"run_id":"designer-2","status":"done"}`), 0o644)
			}
			f := factory.Factory{Repo: "/r/app", Slug: "csv", State: &state.State{ID: "csv", WaitingOn: "user", Attention: c.attention}}
			runs := &state.StageRuns{Runs: []state.StageRun{{ID: "designer-2", Role: "designer", Round: 2, Token: "t-des", Result: result, StartedAt: sent}}}
			var hooks []*state.HookFile
			if c.hook != nil {
				hooks = append(hooks, c.hook)
			}
			live := factory.Live{Pane: factory.Pane{Found: true}, Revising: RevisingGates(f, runs, []tmux.Pane{c.pane}, hooks)}
			if got := factory.StatusOf(f, live, time.Now(), time.Hour); got != c.want {
				t.Errorf("status %v; want %v", got, c.want)
			}
		})
	}
}

func TestOnlyACodexStageIsToldItsSandboxCannotStartChrome(t *testing.T) {
	for _, c := range []struct {
		setting state.AgentSetting
		told    bool
	}{
		{state.AgentSetting{Tool: state.ToolCodex, Model: "gpt-6-astra"}, true},
		{state.AgentSetting{Model: "opus"}, false},
		{state.AgentSetting{Tool: state.ToolOpenCode}, false},
	} {
		prompt, err := StagePrompt("engineer", "Implement layer 1.", "/r/engineer-1.result.json", "", c.setting)
		if err != nil {
			t.Fatal(err)
		}
		if told := strings.Contains(prompt, codexSandboxNote); told != c.told {
			t.Errorf("the %s engineer told it cannot start Chrome: %v; want %v, so Codex stops crashing Chrome in front of the developer and other tools keep running browser tests", c.setting.ToolName(), told, c.told)
		}
	}
}

func TestDiscoveryRunsBesideTheActiveStageWithoutTakingItsPlace(t *testing.T) {
	repo, stateDir := t.TempDir(), t.TempDir()
	worktree := filepath.Join(repo, "wt")
	f := factory.Factory{Repo: repo, Slug: "csv", Dir: filepath.Join(worktree, ".claude", "factory", "csv"), Worktree: worktree, State: &state.State{SchemaVersion: 2, ID: "csv"}}
	runs, _ := state.LoadStageRuns(state.StageRunsPath(stateDir, repo, "csv"))
	analyst := state.StageRun{ID: "csv-start-analyst-1", Role: "analyst", Token: uuid.NewString(), Result: filepath.Join(f.Dir, "stages", "csv-start-analyst-1.result.json")}
	runs.Runs = append(runs.Runs, analyst)
	if err := runs.Save(); err != nil {
		t.Fatal(err)
	}
	engine := &wakingEngine{}
	r := New(Runtime{Config: &state.Config{StateDir: stateDir}, Engine: engine})
	r.SetSnapshot(Snapshot{At: time.Now(), Items: []Item{{Factory: f}}})

	reply, err := r.Stage(StageRequest{Action: "discover", Factory: "csv", Text: "Discover build, lint and test."})
	if err != nil || !strings.HasSuffix(reply.Result, "stages/csv-discover-1.result.json") {
		t.Fatalf("discovery beside the analyst answered %+v, %v; want it started with its own result file", reply, err)
	}
	after, _ := state.LoadStageRuns(state.StageRunsPath(stateDir, repo, "csv"))
	running := RunningStage(f, after, nil, nil, 0)
	if _, discovering := after.Discovering(); running == nil || running.Run.Role != "analyst" || !discovering {
		t.Fatalf("with discovery running the factory shows %+v; want the analyst's stage, with discovery beside it", running)
	}
	if prompt, _ := os.ReadFile(filepath.Join(f.Dir, "stages", "csv-discover-1.prompt.md")); !bytes.Contains(prompt, []byte("factory workflow record")) {
		t.Fatalf("the discoverer's prompt is:\n%s\nwant the discoverer agent's instructions", prompt)
	}
	if again, err := r.Stage(StageRequest{Action: "discover", Factory: "csv", Text: "Discover again."}); err != nil || again.Result != reply.Result {
		t.Fatalf("a second discover while the first runs answered %+v, %v; want the running one, never two discoverers", again, err)
	}

	quiet := 20 * time.Minute
	discovery := DiscoveryStage(f, after, nil, []*state.HookFile{{Token: after.Runs[1].Token, Prompt: &state.PendingPrompt{At: time.Now()}}}, quiet)
	items := AttentionItems(Item{Factory: f, Stage: running, Discovery: discovery}, time.Now(), nil)
	if !slices.ContainsFunc(items, func(a AttentionItem) bool { return a.Kind == "permission" && a.Role == state.Discoverer }) {
		t.Fatalf("a discoverer waiting on a permission prompt shows %+v; want a permission item, or discovery stalls unseen", items)
	}
	later := time.Now().Add(2 * time.Minute)
	if messages := InboxMessages(stateDir, Item{Factory: f, Discovery: discovery}, nil, later); !slices.ContainsFunc(messages, func(m InboxMessage) bool { return strings.HasPrefix(m.Text, "stage discoverer lost:") }) {
		t.Fatalf("a discoverer whose pane is gone sent %+v; want the supervisor told to run discovery again", messages)
	}
	stageLost := func(activeRun string) bool {
		f.State.Workflow.ActiveRun = activeRun
		messages := InboxMessages(stateDir, Item{Factory: f, Stage: RunningStage(f, after, nil, nil, quiet)}, nil, later)
		return slices.ContainsFunc(messages, func(m InboxMessage) bool { return strings.HasPrefix(m.Text, "stage analyst lost:") })
	}
	if !stageLost(analyst.ID) {
		t.Fatal("a stage whose pane closed without a result never reached the supervisor, so the factory waited on it silently")
	}
	if stageLost("csv-other-run") {
		t.Fatal("a run the engine no longer waits on was reported lost")
	}
	f.State.Workflow.ActiveRun = ""
	analystPane := tmux.Pane{ID: "%8", Repo: repo, StageOf: "csv", Stage: "analyst", Token: analyst.Token, Command: "claude"}
	waiting := state.HookEvent{Event: "Stop", At: later.Add(-30 * time.Minute)}
	lostWhileWatching := func(heartbeat time.Time) bool {
		f.Heartbeat = heartbeat
		return RunningStage(f, after, []tmux.Pane{analystPane}, []*state.HookFile{{Token: analyst.Token, Main: &waiting, Last: waiting}}, quiet).Lost(later)
	}
	if lostWhileWatching(later.Add(-time.Minute)) {
		t.Fatal("a stage whose turn ended while its CI watch keeps beating was reported lost, so the supervisor would kill a healthy watch")
	}
	if !lostWhileWatching(time.Time{}) {
		t.Fatal("a stage that ended its turn without a result 30 minutes ago was not reported lost")
	}
	f.Heartbeat = time.Time{}

	run := after.Runs[1]
	pane := tmux.Pane{ID: "%7", Repo: repo, StageOf: "csv", Stage: state.Discoverer, Token: run.Token, Command: "claude"}
	working := DiscoveryStage(f, after, []tmux.Pane{pane}, []*state.HookFile{{Token: run.Token, Main: &state.HookEvent{Event: "PreToolUse", At: run.StartedAt.Add(time.Minute)}}}, quiet)
	if working.Lost(later) {
		t.Fatal("a discoverer still working in its live pane after the grace counts as lost; want long discoveries left alone, never a second discoverer")
	}
	stop := state.HookEvent{Event: "StopFailure", At: run.StartedAt.Add(time.Minute)}
	stuck := DiscoveryStage(f, after, []tmux.Pane{pane}, []*state.HookFile{{Token: run.Token, Main: &stop, Last: stop}}, quiet)
	switch {
	case stuck.Lost(run.StartedAt.Add(5 * time.Minute)):
		t.Fatal("a discoverer that stopped 4 minutes ago counts as lost; want it given stalled_after before it is replaced")
	case !stuck.Lost(run.StartedAt.Add(30 * time.Minute)):
		t.Fatal("a discoverer whose turn ended without a result and stayed quiet past stalled_after counts as running; want it reported lost")
	}
	busy := DiscoveryStage(f, after, []tmux.Pane{pane}, []*state.HookFile{{Token: run.Token, Main: &stop, Last: state.HookEvent{Event: "SubagentStop", At: run.StartedAt.Add(25 * time.Minute)}}}, quiet)
	if busy.Lost(run.StartedAt.Add(30 * time.Minute)) {
		t.Fatal("a discoverer whose background work was still reporting 5 minutes ago counts as lost; want later hook activity respected")
	}
}
