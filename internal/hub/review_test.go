package hub

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/afonsodelgado/factory/internal/factory"
	"github.com/afonsodelgado/factory/internal/review"
	"github.com/afonsodelgado/factory/internal/state"
	"github.com/afonsodelgado/factory/internal/tmux"
)

type reviewFixture struct {
	m          *Model
	typist     *fakeTypist
	f          factory.Factory
	store      *review.Store
	supervisor tmux.Pane
	designer   tmux.Pane
}

func newReviewFixture(t *testing.T, designerRan bool, supervisorHook *state.HookFile) reviewFixture {
	t.Helper()
	stateDir, worktree := t.TempDir(), t.TempDir()
	dir := filepath.Join(worktree, ".claude", "factory", "csv")
	if err := os.MkdirAll(filepath.Join(dir, "design"), 0o755); err != nil {
		t.Fatal(err)
	}
	page := filepath.Join(dir, "design", "variant-a.html")
	os.WriteFile(page, []byte("<body><button>Export</button></body>"), 0o644)
	st := &state.State{ID: "csv", Stages: map[string]state.Stage{"design": {Artifacts: []state.Artifact{{URL: "file://" + page}}}}}
	f := factory.Factory{Repo: "/repo", Slug: "csv", Dir: dir, Worktree: worktree, State: st}

	srv, err := review.Listen(0, "tok", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { srv.Close() })
	srv.SetFactories([]review.Factory{{Key: state.FileStem(f.Repo, f.Slug), Label: "csv", Dir: dir}})
	store, err := srv.Store(review.Factory{Key: state.FileStem(f.Repo, f.Slug), Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create(review.NewThread{Page: "design/variant-a.html", Intent: "fix", Text: "Make Export the primary button.", Anchor: review.Anchor{Selector: "button"}}); err != nil {
		t.Fatal(err)
	}

	supervisor := tmux.Pane{ID: "%1", Command: "claude", Token: "t-sup", Repo: f.Repo, Slug: f.Slug}
	designer := tmux.Pane{ID: "%2", Command: "codex", Token: "t-des", Repo: f.Repo, StageOf: f.Slug, Stage: "designer"}
	panes := []tmux.Pane{supervisor}
	at := time.Now().Add(-time.Minute)
	hooks := []*state.HookFile{withToken(supervisorHook, "t-sup")}
	if designerRan {
		runs, _ := state.LoadStageRuns(state.StageRunsPath(stateDir, f.Repo, f.Slug))
		runs.Runs = append(runs.Runs, state.StageRun{Role: "designer", Round: 1, Token: "t-des", Setting: state.AgentSetting{Tool: state.ToolCodex}, Result: filepath.Join(dir, "stages", "designer-1.result.json")})
		if err := runs.Save(); err != nil {
			t.Fatal(err)
		}
		panes = append(panes, designer)
		hooks = append(hooks, withToken(idleHook(at), "t-des"))
	}
	typist := &fakeTypist{}
	m := NewModel(Model{Config: &state.Config{StateDir: stateDir}, Typist: typist, Review: srv})
	m.snap = Snapshot{At: time.Now(), Panes: panes, Hooks: hooks, Items: []Item{{Factory: f, Pane: &supervisor, Hook: hooks[0]}}}
	return reviewFixture{m: m, typist: typist, f: f, store: store, supervisor: supervisor, designer: designer}
}

func idleHook(at time.Time) *state.HookFile {
	return &state.HookFile{Main: &state.HookEvent{Event: "Stop", At: at}, Last: state.HookEvent{Event: "Stop", At: at}}
}

func busyHook(at time.Time) *state.HookFile {
	return &state.HookFile{Main: &state.HookEvent{Event: "UserPromptSubmit", At: at}, Last: state.HookEvent{Event: "UserPromptSubmit", At: at}}
}

func withToken(h *state.HookFile, token string) *state.HookFile {
	h.Token = token
	return h
}

func TestSentCommentsReachTheAgentThatOwnsThePage(t *testing.T) {
	at := time.Now().Add(-time.Minute)

	t.Run("the designer's pane gets the batch, not the supervisor", func(t *testing.T) {
		fx := newReviewFixture(t, true, idleHook(at))
		reply, err := fx.m.review(ReviewRequest{Action: "send", Factory: state.FileStem(fx.f.Repo, fx.f.Slug), Page: "design/variant-a.html"})
		if err != nil {
			t.Fatal(err)
		}
		message := filepath.Join(fx.f.Dir, "stages", "designer-2.message.md")
		if reply.Target != "designer" || len(fx.typist.typed) != 1 || fx.typist.typed[0] != "%2: Read "+message+" and follow it." {
			t.Fatalf("target %q, typed %q; want the designer's pane told to read %s", reply.Target, fx.typist.typed, message)
		}
		body, _ := os.ReadFile(message)
		if !strings.HasPrefix(string(body), "review-csv-1 comments: design/variant-a.html") || !strings.Contains(string(body), "Make Export the primary button.") {
			t.Errorf("the designer's message is %q; want the batch with the comment", body)
		}
		runs, _ := state.LoadStageRuns(state.StageRunsPath(fx.m.Config.StateDir, fx.f.Repo, fx.f.Slug))
		if latest, _ := runs.Latest("designer"); !latest.Review {
			t.Error("the follow-up run is not marked as a review round, so its result would read as the stage finishing")
		}
		if got := fx.store.Threads("")[0].Status; got != review.Sent {
			t.Errorf("thread status %q; want sent", got)
		}
	})

	t.Run("without a designer pane the busy supervisor gets the batch once it is idle", func(t *testing.T) {
		fx := newReviewFixture(t, false, busyHook(at))
		reply, err := fx.m.review(ReviewRequest{Action: "send", Factory: state.FileStem(fx.f.Repo, fx.f.Slug), Page: "design/variant-a.html"})
		if err != nil {
			t.Fatal(err)
		}
		if reply.Target != "supervisor" || len(fx.typist.typed) != 0 {
			t.Fatalf("target %q, typed %q; want it held for the busy supervisor", reply.Target, fx.typist.typed)
		}
		fx.m.snap.Hooks[0] = withToken(idleHook(time.Now()), "t-sup")
		fx.m.flushTypeQueue()
		want := "%1: review-csv-1 comments: " + review.BatchPath(fx.f.Dir, 1)
		if len(fx.typist.typed) != 1 || fx.typist.typed[0] != want {
			t.Errorf("typed %q; want %q", fx.typist.typed, want)
		}
	})
}

func TestAnAgentAnswersAndResolvesAThreadFromItsPane(t *testing.T) {
	fx := newReviewFixture(t, true, idleHook(time.Now()))
	thread := fx.store.Threads("")[0]
	reply, err := fx.m.review(ReviewRequest{Action: "reply", Token: "t-des", Cwd: fx.f.Worktree, Thread: thread.ID, Text: "Export is now the primary button.", Resolve: true})
	if err != nil {
		t.Fatal(err)
	}
	got := reply.Threads[0]
	last := got.Messages[len(got.Messages)-1]
	if got.Status != review.Resolved || last.By != "designer" || last.Text != "Export is now the primary button." {
		t.Errorf("thread after the reply: %+v; want it resolved with the designer's answer", got)
	}
}

func TestAReviewRoundDoesNotTellTheSupervisorTheStageIsDone(t *testing.T) {
	stateDir, dir := t.TempDir(), t.TempDir()
	f := factory.Factory{Repo: "/repo", Slug: "csv", Dir: dir}
	runs, _ := state.LoadStageRuns(state.StageRunsPath(stateDir, f.Repo, f.Slug))
	for i, isReview := range []bool{false, true} {
		result := filepath.Join(dir, "stages", "designer-"+string(rune('1'+i))+".result.json")
		writeFile(result, "{}")
		runs.Runs = append(runs.Runs, state.StageRun{Role: "designer", Round: i + 1, Result: result, Review: isReview})
	}
	if err := runs.Save(); err != nil {
		t.Fatal(err)
	}

	messages := inboxMessages(stateDir, Item{Factory: f}, nil)

	if len(messages) != 1 || !strings.HasSuffix(messages[0].text, "designer-1.result.json") {
		t.Errorf("inbox %+v; want only the real stage result, never the review round's, which would move the supervisor past its gate", messages)
	}
}

func TestTheWalkthroughStackRunsInItsOwnWindowAndStopsWithTheFactory(t *testing.T) {
	e, l := hubFixture(t)
	worktree := t.TempDir()
	dir := filepath.Join(worktree, ".claude", "factory", "csv")
	script := filepath.Join(dir, "review", "stack.sh")
	writeFile(script, "exec sleep 600\n")
	writeFile(StackPath(dir), `{"url": "http://127.0.0.1:1/", "script": "`+script+`"}`)
	f := factory.Factory{Repo: "/repo", Slug: "csv", Dir: dir, Worktree: worktree, State: &state.State{ID: "csv"}}
	m := NewModel(Model{Config: &state.Config{StateDir: t.TempDir()}, Engine: e, Layout: l, Typist: e})
	m.snap = Snapshot{At: time.Now(), Items: []Item{{Factory: f}}}
	reply := make(chan Message, 1)

	m.startStack(Event{Reply: reply}, ReviewRequest{Action: "stack", Cwd: worktree})

	if got := <-reply; got.T != "done" {
		t.Fatalf("stack request: %+v", got)
	}
	stackPanes := func() int {
		panes, _ := e.TM.Panes()
		n := 0
		for _, p := range panes {
			if p.StageOf == "csv" && p.Stage == stackRole {
				n++
			}
		}
		return n
	}
	if stackPanes() != 1 {
		t.Fatalf("%d stack panes after the request; want one window running the stack", stackPanes())
	}
	m.snap.Items[0].Status = factory.Done
	m.snap.Panes, _ = e.TM.Panes()
	m.closeFinishedStages()
	if stackPanes() != 0 {
		t.Error("the stack still runs after the factory is done, leaving dev servers behind")
	}
}
