package hub

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/afonsodelgado/factory/internal/factory"
	"github.com/afonsodelgado/factory/internal/host"
	"github.com/afonsodelgado/factory/internal/review"
	"github.com/afonsodelgado/factory/internal/state"
	"github.com/afonsodelgado/factory/internal/tmux"
	"github.com/afonsodelgado/factory/internal/ui/panel"
)

type reviewFixture struct {
	m          *Model
	n          *testNode
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

	srv, err := review.New("tok", nil)
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
	m, n := withNode(Model{Config: &state.Config{StateDir: stateDir}}, host.Runtime{Typist: typist, Pages: srv})
	n.show(m, Snapshot{At: time.Now(), Panes: panes, Hooks: hooks, Items: []Item{{Factory: f, Pane: &supervisor, Hook: hooks[0]}}})
	return reviewFixture{m: m, n: n, typist: typist, f: f, store: store, supervisor: supervisor, designer: designer}
}

func (fx reviewFixture) review(r ReviewRequest) (ReviewReply, error) {
	fx.n.mu.Lock()
	defer fx.n.mu.Unlock()
	return fx.n.r.Review(r)
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

func TestThePanelCountsAPagesOpenThreadsFromTheNode(t *testing.T) {
	fx := newReviewFixture(t, false, idleHook(time.Now()))
	fx.m.choose(fx.f.Key())
	artifacts := fx.m.panelView().Artifacts
	if len(artifacts) != 1 || artifacts[0].OpenThreads == nil || *artifacts[0].OpenThreads != 1 {
		t.Fatalf("the panel lists %+v; want the variant with its one open thread, counted by the node's review server", artifacts)
	}
}

func TestSentCommentsReachTheAgentThatOwnsThePage(t *testing.T) {
	at := time.Now().Add(-time.Minute)

	t.Run("the designer's pane gets the batch, not the supervisor", func(t *testing.T) {
		fx := newReviewFixture(t, true, idleHook(at))
		reply, err := fx.review(ReviewRequest{Action: "send", Factory: state.FileStem(fx.f.Repo, fx.f.Slug), Page: "design/variant-a.html"})
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

	t.Run("a prototype on the app's dev server is the designer's, not the walkthrough fixer's", func(t *testing.T) {
		fx := newReviewFixture(t, true, idleHook(at))
		fx.store.Create(review.NewThread{Page: review.Prototype, Intent: "change", Text: "Use the compact table.", Anchor: review.Anchor{URL: "http://localhost:3001/prototypes/csv/variant-a.html", Selector: "table"}})
		reply, err := fx.review(ReviewRequest{Action: "send", Factory: state.FileStem(fx.f.Repo, fx.f.Slug), Page: review.Prototype})
		if err != nil {
			t.Fatal(err)
		}
		if reply.Target != "designer" {
			t.Errorf("prototype comments went to %q; want the designer", reply.Target)
		}
	})

	t.Run("without a designer pane the busy supervisor gets the batch once it is idle", func(t *testing.T) {
		fx := newReviewFixture(t, false, busyHook(at))
		reply, err := fx.review(ReviewRequest{Action: "send", Factory: state.FileStem(fx.f.Repo, fx.f.Slug), Page: "design/variant-a.html"})
		if err != nil {
			t.Fatal(err)
		}
		if reply.Target != "supervisor" || len(fx.typist.typed) != 0 {
			t.Fatalf("target %q, typed %q; want it held for the busy supervisor", reply.Target, fx.typist.typed)
		}
		snap := fx.m.snap
		snap.Hooks = []*state.HookFile{withToken(idleHook(time.Now()), "t-sup")}
		snap.Items[0].Hook = snap.Hooks[0]
		fx.n.show(fx.m, snap)
		want := "%1: review-csv-1 comments: " + review.BatchPath(fx.f.Dir, 1)
		if len(fx.typist.typed) != 1 || fx.typist.typed[0] != want {
			t.Errorf("typed %q; want %q", fx.typist.typed, want)
		}
	})
}

func TestAnApprovalFromTheReviewPageClosesThePanelItemOnceReceived(t *testing.T) {
	now := time.Now()
	fx := newReviewFixture(t, false, busyHook(now.Add(-time.Minute)))
	fx.f.State.Attention = []state.Attention{{ID: "gate-csv-design", Kind: "gate", Title: "design approval", CreatedAt: state.Time{Time: now.Add(-time.Hour)}}}
	clock := now
	fx.m.Now = func() time.Time { return clock }
	fx.n.r.Now = fx.m.Now
	if _, err := fx.review(ReviewRequest{Action: "approve", Factory: state.FileStem(fx.f.Repo, fx.f.Slug), Page: "design/variant-a.html", Gate: "gate-csv-design-earlier"}); err == nil {
		t.Fatal("a page that loaded before the gate was replaced approved the new gate")
	}
	if _, err := fx.review(ReviewRequest{Action: "approve", Factory: state.FileStem(fx.f.Repo, fx.f.Slug), Page: "design/variant-a.html", Gate: "gate-csv-design"}); err != nil {
		t.Fatal(err)
	}

	later := func(after time.Duration, hook *state.HookFile) {
		clock = now.Add(after)
		snap := fx.m.snap
		snap.At = clock
		snap.Hooks = []*state.HookFile{withToken(hook, "t-sup")}
		snap.Items = []Item{{Factory: fx.f, Pane: &fx.supervisor, Hook: snap.Hooks[0]}}
		fx.n.show(fx.m, snap)
	}
	later(30*time.Second, idleHook(now.Add(29*time.Second)))
	received := idleHook(now.Add(29 * time.Second))
	received.Submit = &state.HookEvent{Event: "UserPromptSubmit", PromptHead: "/factory approve design gate-csv-design", At: now.Add(31 * time.Second)}
	later(32*time.Second, received)

	shown, answered := fx.m.shownItems(fx.m.snap.Items[0])
	refused := fx.m.panel.refused[draftKey(fx.f, "gate-csv-design")]
	if len(fx.typist.typed) != 1 || len(shown) != 0 || len(answered) != 1 || refused != "" {
		t.Errorf("typed %q, panel shows %+v, answered %+v, refused %q; want the approval typed once the supervisor was idle and the gate closed in the panel", fx.typist.typed, shown, answered, refused)
	}
}

func TestAnAgentAnswersAndResolvesAThreadFromItsPane(t *testing.T) {
	fx := newReviewFixture(t, true, idleHook(time.Now()))
	thread := fx.store.Threads("")[0]
	reply, err := fx.review(ReviewRequest{Action: "reply", Token: "t-des", Cwd: fx.f.Worktree, Thread: thread.ID, Text: "Export is now the primary button.", Resolve: true})
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
		host.WriteFile(result, "{}")
		runs.Runs = append(runs.Runs, state.StageRun{Role: "designer", Round: i + 1, Result: result, Review: isReview})
	}
	if err := runs.Save(); err != nil {
		t.Fatal(err)
	}

	messages := host.InboxMessages(stateDir, Item{Factory: f}, nil)

	if len(messages) != 1 || !strings.HasSuffix(messages[0].Text, "designer-1.result.json") {
		t.Errorf("inbox %+v; want only the real stage result, never the review round's, which would move the supervisor past its gate", messages)
	}
}

func TestCtrlOOnADesignGateOpensTheNodesReviewPage(t *testing.T) {
	fx := newReviewFixture(t, false, idleHook(time.Now()))
	fx.f.State.Attention = []state.Attention{{ID: "gate-design", Kind: "gate", Title: "Approve the design"}}
	fx.m.Config.Node.Hostname = "mac.tail.ts.net"
	fx.n.show(fx.m, Snapshot{At: time.Now(), Panes: []tmux.Pane{fx.supervisor}, Items: []Item{{Factory: fx.f, Pane: &fx.supervisor}}})
	var opened []string
	fx.m.Open = func(url string) { opened = append(opened, url) }
	fx.m.choose(fx.f.Key())
	fx.m.panel.tab = panel.Attention
	if _, cmd := fx.m.Update(eventMsg(Event{Message: Message{T: "key", Pane: "panel", K: "ctrl+o"}})); cmd != nil {
		cmd()
	}
	want := "https://mac.tail.ts.net/r/" + state.FileStem(fx.f.Repo, fx.f.Slug) + "/design/variant-a.html"
	if len(opened) != 1 || opened[0] != want {
		t.Fatalf("ctrl+o on the gate opened %q; want %s, the review page with comments", opened, want)
	}
}
