package hub

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/exp/teatest/v2"

	"github.com/afonsodelgado/factory/internal/factory"
	"github.com/afonsodelgado/factory/internal/state"
	"github.com/afonsodelgado/factory/internal/tmux"
	"github.com/afonsodelgado/factory/internal/ui/dialog"
)

func TestWakeResumesTheRecordedConversation(t *testing.T) {
	worktree := "/r/cowork/.claude/worktrees/eng-7445-x"
	at := time.Date(2026, 10, 22, 1, 12, 0, 0, time.UTC)
	hooks := []*state.HookFile{
		{SessionID: "older", CWD: worktree, Last: state.HookEvent{At: at.Add(-time.Hour)}},
		{SessionID: "from-hook", CWD: worktree + "/apps/web", Last: state.HookEvent{At: at}},
		{SessionID: "elsewhere", CWD: "/r/cowork/.claude/worktrees/other", Last: state.HookEvent{At: at.Add(time.Hour)}},
	}
	gone := func(f factory.Factory) Item {
		f.Repo, f.Slug, f.Worktree = "/r/cowork", "eng-7445-x", worktree
		return Item{Factory: f, Status: factory.Gone}
	}

	cases := []struct {
		name string
		item Item
		want string
	}{
		{"registry", gone(factory.Factory{Entry: &state.Entry{SessionID: "from-registry"}, State: &state.State{SessionID: "from-state"}}), "claude --resume from-registry '/factory resume'"},
		{"state.json", gone(factory.Factory{State: &state.State{SessionID: "from-state"}}), "claude --resume from-state '/factory resume'"},
		{"newest hook file in the worktree", gone(factory.Factory{State: &state.State{}}), "claude --resume from-hook '/factory resume'"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := PlanWake(c.item, state.Profile{}, false, hooks).Command; got != c.want {
				t.Errorf("wake runs %q, want %q", got, c.want)
			}
		})
	}

	nothing := PlanWake(gone(factory.Factory{State: &state.State{}}), state.Profile{}, false, nil)
	if nothing.Command != "claude --continue '/factory resume'" || nothing.Note() != "woke with --continue: no session id in the registry, state.json or the hook files" {
		t.Errorf("without any session id: %q, activity %q", nothing.Command, nothing.Note())
	}
}

func TestThePanesOwnSessionIsLiveAfterTheBridgeContinuesIt(t *testing.T) {
	worktree := "/r/factory/.claude/worktrees/release-0-2"
	at := time.Date(2026, 10, 23, 10, 22, 0, 0, time.UTC)
	frozen := &state.HookFile{SessionID: "57bcd935", CWD: worktree, Main: &state.HookEvent{Event: "Stop", At: at.Add(-10 * time.Minute)}, Last: state.HookEvent{Event: "Stop", At: at.Add(-10 * time.Minute)}}
	live := &state.HookFile{SessionID: "06c7ff87", CWD: worktree, Token: "t1", Main: &state.HookEvent{Event: "UserPromptSubmit", At: at}, Last: state.HookEvent{Event: "UserPromptSubmit", At: at}}
	hooks := []*state.HookFile{frozen, live}
	pane := &tmux.Pane{ID: "%5", Command: "claude", Token: "t1"}
	f := factory.Factory{Repo: "/r/factory", Slug: "release-0-2", Worktree: worktree, Entry: &state.Entry{SessionID: "57bcd935"}, State: &state.State{SessionID: "57bcd935"}}

	hook := hookFor(f, pane.Token, hooks)
	if hook != live {
		t.Fatalf("hookFor read session %s; want the pane's own 06c7ff87, or status and delivery read a file frozen since remote control continued the session", hook.SessionID)
	}
	if reason := Refusal(Target{Label: "release-0-2", Pane: pane, Hook: hook}); !strings.Contains(reason, "busy") {
		t.Errorf("refusal = %q; want busy, because the live session is working on a prompt", reason)
	}
	if id, _ := SessionFor(f, pane.Token, hooks); id != "06c7ff87" {
		t.Errorf("wake resumes %s; want the live 06c7ff87, not the transcript from before the bridge continued it", id)
	}
}

func TestOpenSaysWhyWhenItCannotSelectAnything(t *testing.T) {
	repo := t.TempDir()
	worktree := filepath.Join(factory.WorktreesDir(repo), "eng-9-clean")
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := &state.Config{Repos: []string{repo}, StateDir: t.TempDir()}
	registry, err := state.LoadRegistry(cfg.RegistryPath())
	if err != nil {
		t.Fatal(err)
	}
	entry := registry.Upsert(state.Entry{Repo: repo, Slug: "eng-9-clean", Archived: true})
	archived := Item{Status: factory.Done, Factory: factory.Factory{Repo: repo, Slug: "eng-9-clean", Worktree: worktree, State: &state.State{ID: "ENG-9"}, Entry: entry}}
	model := NewModel(Model{Config: cfg, Loader: &Loader{Config: cfg, Registry: registry, Now: time.Now}})
	model.SetSnapshot(Snapshot{At: time.Now(), Items: []Item{archived}})
	for name, reason := range map[string]string{"!!!": "letters", "ENG-9": "archived"} {
		if err := model.open(OpenRequest{Name: name}); err == nil || !strings.Contains(err.Error(), reason) {
			t.Errorf("open %q = %v; want it refused, naming %q", name, err, reason)
		}
	}

	model.selectorKey("H")
	model.choose(archived.Factory.Key())
	model.selectorKey("a")
	if registry.Find(repo, "eng-9-clean").Archived {
		t.Error("a on the archived ENG-9 row left it archived; the id could never get its factory back")
	}
}

func TestOpeningAnIdSelectsItsFactoryOrCreatesOne(t *testing.T) {
	repo := t.TempDir()
	legacy := filepath.Join(repo, ".claude", "factory", "ENG-9")
	if err := os.MkdirAll(legacy, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "state.json"), []byte(`{"ticket":"ENG-9","worktree":"","stages":{"handoff":{"status":"done"}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := &state.Config{Repos: []string{repo}, StateDir: t.TempDir()}
	registry, err := state.LoadRegistry(cfg.RegistryPath())
	if err != nil {
		t.Fatal(err)
	}
	loader := &Loader{Config: cfg, Registry: registry, Now: time.Now}
	model := NewModel(Model{Config: cfg, Loader: loader})
	model.SetSnapshot(loader.Load())

	tm := teatest.NewTestModel(t, dialog.NewFactory(model.newContext()), teatest.WithInitialTermSize(84, 22))
	tm.Type("ENG-9")
	tm.Send(tea.KeyPressMsg{Code: tea.KeyEnter})
	tm.Type("Retry the export.")
	tm.Send(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	result, _ := tm.FinalModel(t, teatest.WithFinalTimeout(5*time.Second)).(*dialog.NewFactoryModel).Result()
	if r, _ := result.(dialog.NewResult); r.Select != "" || r.Slug != "eng-9-2" || r.Prompt != "Retry the export." {
		t.Errorf("n ENG-9 gave %+v; want a new factory with its prompt, under an id the done row does not hold", result)
	}

	model.open(OpenRequest{Name: "ENG-9", Prompt: "Retry the export."})
	if err := os.MkdirAll(filepath.Join(factory.WorktreesDir(repo), "eng-9-2"), 0o755); err != nil {
		t.Fatal(err)
	}
	model.SetSnapshot(loader.Load())
	model.open(OpenRequest{Name: "eng-9-2"})
	var keys []string
	for _, it := range loader.Load().Items {
		keys = append(keys, it.Factory.Slug)
	}
	if !slices.Equal(keys, []string{"eng-9", "eng-9-2"}) {
		t.Errorf("after open ENG-9 and then open eng-9-2, the rows are %v; want the done eng-9 kept as history and eng-9-2 created once, never duplicated", keys)
	}
	if want := state.Key(repo, "eng-9-2"); model.selected != want {
		t.Errorf("open eng-9-2 selected %q, want the existing %q", model.selected, want)
	}
}

func TestOpenRefusesARepoTheRunningHubDoesNotList(t *testing.T) {
	model := NewModel(Model{Config: &state.Config{Repos: []string{"/r/cowork"}}})
	err := model.open(OpenRequest{Name: "ENG-1", Repo: "/r/added-later"})
	if err == nil || !strings.Contains(err.Error(), "does not list /r/added-later") {
		t.Errorf("open in a repo the hub never discovers = %v; the factory would get no row", err)
	}
}
