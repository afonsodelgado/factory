package hub

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/afonsodelgado/factory/internal/factory"
	"github.com/afonsodelgado/factory/internal/state"
	"github.com/afonsodelgado/factory/internal/tmux"
	"github.com/afonsodelgado/factory/internal/ui/frame"
)

func TestAStagePaneRunsItsToolWithTheRulesAndNeverStandsInForTheSupervisor(t *testing.T) {
	bin := t.TempDir()
	calls := filepath.Join(bin, "calls")
	stub := "#!/bin/sh\necho \"$@\" > " + calls + "\nexec sleep 600\n"
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("CODEX_HOME", t.TempDir())
	e, l := hubFixture(t)

	cfg := &state.Config{ConfigDir: t.TempDir(), StateDir: t.TempDir()}
	os.WriteFile(cfg.RulesPath(), []byte("Write no comments."), 0o644)
	repo, worktree := t.TempDir(), t.TempDir()
	cfg.Agents = map[string]state.AgentSetting{"engineer": {Model: "sonnet", Effort: "medium"}}
	atCreation := &state.Entry{Agents: state.ResolveAgents(cfg, state.Profile{}, nil)}
	cfg.Agents = map[string]state.AgentSetting{"engineer": {Tool: state.ToolCodex, Model: "gpt-5.6-sol"}}
	f := factory.Factory{Repo: repo, Slug: "csv", Dir: filepath.Join(worktree, ".claude", "factory", "csv"), Worktree: worktree, Entry: atCreation, State: &state.State{Agents: map[string]state.AgentSetting{"reviewer": {Effort: "low"}}}}
	supervisor, err := e.NewFactoryPane(l, NewPane{Repo: repo, Slug: "csv", Dir: worktree})
	if err != nil {
		t.Fatal(err)
	}
	m := NewModel(Model{Config: cfg, Engine: e, Layout: l, Typist: e})
	supervisorPane := pane(t, e.TM, supervisor)
	m.snap = Snapshot{At: time.Now(), Items: []Item{{Factory: f, Pane: &supervisorPane}}}

	reply, err := m.stage(StageRequest{Action: "start", Token: supervisorPane.Token, Role: "engineer", Text: "Implement layer 1 of plan.md."})
	if err != nil {
		t.Fatal(err)
	}

	prompt := filepath.Join(f.Dir, "stages", "engineer-1.prompt.md")
	want := "--model sonnet --effort medium Read " + prompt + " and follow it.\n"
	if args := waitForFile(t, calls, want); args != want {
		t.Errorf("the stage pane ran claude %q; want %q, the engineer as resolved when the factory was created, not the config changed to codex since", args, want)
	}
	body, _ := os.ReadFile(prompt)
	for _, want := range []string{"Write no comments.", "staff engineer", "Implement layer 1 of plan.md.", reply.Result} {
		if !strings.Contains(string(body), want) {
			t.Errorf("the stage prompt lacks %q, so the agent would miss the rules, its role, its task or where to write its result", want)
		}
	}
	panes, _ := e.TM.Panes()
	if p, _ := FactoryPane(panes, repo, "csv"); p.ID != supervisor {
		t.Errorf("FactoryPane picked %s; want the supervisor %s, or answers and wakes would go to the stage agent", p.ID, supervisor)
	}
	runs, _ := state.LoadStageRuns(state.StageRunsPath(cfg.StateDir, repo, "csv"))
	if run, ok := runs.Running(); !ok || run.Role != "engineer" || run.Result != reply.Result {
		t.Errorf("running stage = %+v; want the engineer writing to %s", run, reply.Result)
	}
}

func TestWakeResumesTheSupervisorNotTheStageAgentThatWorkedLast(t *testing.T) {
	stateDir := t.TempDir()
	worktree := filepath.Join(t.TempDir(), ".claude", "worktrees", "csv")
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	for _, h := range []struct{ session, token string }{{"supervisor", "t-sup"}, {"engineer", "t-eng"}} {
		payload := `{"session_id":"` + h.session + `","cwd":"` + worktree + `","hook_event_name":"Stop"}`
		if err := state.RecordHook(stateDir, []byte(payload), h.token, now); err != nil {
			t.Fatal(err)
		}
		now = now.Add(time.Minute)
	}
	m := NewModel(Model{Config: &state.Config{StateDir: stateDir}})
	f := factory.Factory{Repo: "/r", Slug: "csv", Worktree: worktree, State: &state.State{}}
	m.snap = Snapshot{Items: []Item{{Factory: f, Status: factory.Gone}}, StageTokens: map[string]bool{"t-eng": true}}

	m.planWakes()

	if got := m.wakes[f.Key()].Launch.SessionID; got != "supervisor" {
		t.Errorf("wake resumes %q; want the supervisor's session, not the engineer that wrote the newest hook in the worktree", got)
	}
}

func TestAStageSplitsBesideTheSupervisorInsideTheFrameAndParksAtItsSize(t *testing.T) {
	e, l := hubFixture(t)
	supervisor, err := e.NewFactoryPane(l, NewPane{Repo: "/repo", Slug: "csv", Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Select(&l, supervisor); err != nil {
		t.Fatal(err)
	}
	framedWidth := areaOf(t, e.TM, l.Slot).w
	_, stageWidth := SplitWidths(framedWidth)
	stage, err := e.NewStagePane(l, StagePane{Repo: "/repo", Slug: "csv", Role: "engineer", Token: "t-eng", Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if w := pane(t, e.TM, stage).Width; w != stageWidth {
		t.Errorf("the parked stage is %d wide; want the split's %d, or the agent's screen reflows when it appears", w, stageWidth)
	}

	if err := e.ShowStage(&l, stage); err != nil {
		t.Fatal(err)
	}
	left, divider, right, top := areaOf(t, e.TM, l.Slot), areaOf(t, e.TM, l.Split), areaOf(t, e.TM, stage), areaOf(t, e.TM, l.Frame.Top)
	underTheTee := divider.x == top.x+frame.SplitColumn(top.w) && divider.w == 1 && divider.h == left.h
	if right.y != left.y || divider.x != left.x+left.w+1 || right.x != divider.x+2 || right.w != stageWidth || top.w != framedWidth || !underTheTee {
		t.Errorf("supervisor %+v, divider %+v, stage %+v, top strip %+v; want them side by side under one %d-wide frame with the divider under its ┬", left, divider, right, top, framedWidth)
	}
	other, err := e.NewFactoryPane(l, NewPane{Repo: "/repo", Slug: "other", Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if w := pane(t, e.TM, other).Width; w != framedWidth {
		t.Errorf("another factory parked while the split shows is %d wide; want the whole frame's %d, or it reflows when selected", w, framedWidth)
	}

	width, height, _ := e.TM.WindowSize(l.Selector)
	rebuilt, err := e.Build(l.Selector, Shape{Width: width, Height: height})
	if err != nil {
		t.Fatal(err)
	}
	if rebuilt.Stage != stage || rebuilt.Split != l.Split {
		t.Errorf("after a hub restart the layout has stage %q and divider %q; want %s and %s, or the split could never close", rebuilt.Stage, rebuilt.Split, stage, l.Split)
	}

	if err := e.HideStage(&rebuilt); err != nil {
		t.Fatal(err)
	}
	parkedStage := pane(t, e.TM, stage)
	if parkedStage.WindowName != StageParkName("csv", "engineer") || parkedStage.Width != stageWidth {
		t.Errorf("hidden stage is %q at %d wide; want %q at %d", parkedStage.WindowName, parkedStage.Width, StageParkName("csv", "engineer"), stageWidth)
	}
	if panes, _ := e.TM.Panes(); slices.ContainsFunc(panes, func(p tmux.Pane) bool { return p.ID == l.Split }) {
		t.Errorf("the divider %s outlived the split it separated", l.Split)
	}
	framed(t, e, rebuilt, "after the split closed")
}

func TestACodexSupervisorHearsAboutAFinishedStageOnceAndOnlyWhenIdle(t *testing.T) {
	stateDir := t.TempDir()
	dir := filepath.Join(t.TempDir(), ".claude", "factory", "csv")
	result := filepath.Join(dir, "stages", "engineer-1.result.json")
	runs, _ := state.LoadStageRuns(state.StageRunsPath(stateDir, "/r", "csv"))
	runs.Runs = append(runs.Runs, state.StageRun{Role: "engineer", Round: 1, Token: "t-eng", Result: result})
	runs.Save()
	writeFile(result, `{"status":"done"}`)

	at := time.Now()
	busy := &state.HookFile{Token: "t-sup", Main: &state.HookEvent{Event: "UserPromptSubmit", At: at}, Last: state.HookEvent{Event: "UserPromptSubmit", At: at}}
	idle := &state.HookFile{Token: "t-sup", Main: &state.HookEvent{Event: "Stop", At: at}, Last: state.HookEvent{Event: "Stop", At: at}}
	typist := &fakeTypist{}
	m := NewModel(Model{Config: &state.Config{StateDir: stateDir}, Typist: typist})
	supervisor := tmux.Pane{ID: "%3", Command: "codex", Token: "t-sup"}
	f := factory.Factory{Repo: "/r", Slug: "csv", Dir: dir, Entry: &state.Entry{Supervisor: state.AgentSetting{Tool: state.ToolCodex}}}
	snapshot := func(hook *state.HookFile, at time.Time) Snapshot {
		return Snapshot{At: at, Items: []Item{{Factory: f, Pane: &supervisor, Hook: hook}}, Panes: []tmux.Pane{supervisor}, Hooks: []*state.HookFile{hook}}
	}

	m.apply(snapshot(busy, at))
	if len(typist.typed) != 0 {
		t.Fatalf("typed %q into a busy supervisor; want it held until the supervisor is at its prompt", typist.typed)
	}
	m.apply(snapshot(idle, at.Add(time.Second)))
	m.apply(snapshot(idle, at.Add(2*time.Second)))
	restarted := NewModel(Model{Config: &state.Config{StateDir: stateDir}, Typist: typist})
	restarted.apply(snapshot(idle, at.Add(3*time.Second)))

	want := []string{"%3: stage engineer done: " + result}
	if !slices.Equal(typist.typed, want) {
		t.Errorf("typed %q; want exactly %q, once, even after a hub restart forgets what it queued", typist.typed, want)
	}
}
