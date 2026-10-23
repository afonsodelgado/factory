package hub

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/afonsodelgado/factory/internal/factory"
	"github.com/afonsodelgado/factory/internal/state"
)

func TestAStagePaneRunsItsToolWithTheRulesAndNeverStandsInForTheSupervisor(t *testing.T) {
	bin := t.TempDir()
	calls := filepath.Join(bin, "calls")
	stub := "#!/bin/sh\necho \"$@\" > " + calls + "\nexec sleep 600\n"
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	e, l := hubFixture(t)

	cfg := &state.Config{ConfigDir: t.TempDir(), StateDir: t.TempDir()}
	os.WriteFile(cfg.RulesPath(), []byte("Write no comments."), 0o644)
	repo, worktree := t.TempDir(), t.TempDir()
	f := factory.Factory{Repo: repo, Slug: "csv", Dir: filepath.Join(worktree, ".claude", "factory", "csv"), Worktree: worktree}
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
	want := "--model opus --effort xhigh Read " + prompt + " and follow it.\n"
	if args := waitForFile(t, calls, want); args != want {
		t.Errorf("the stage pane ran claude %q; want %q", args, want)
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
