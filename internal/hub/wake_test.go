package hub

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/afonsodelgado/factory/internal/factory"
	"github.com/afonsodelgado/factory/internal/state"
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

func TestOpenSaysWhyWhenItCannotSelectAnything(t *testing.T) {
	repo := t.TempDir()
	if err := os.MkdirAll(factory.WorktreesDir(repo), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := &state.Config{Repos: []string{repo}, StateDir: t.TempDir()}
	registry, err := state.LoadRegistry(cfg.RegistryPath())
	if err != nil {
		t.Fatal(err)
	}
	entry := registry.Upsert(state.Entry{Repo: repo, Slug: "eng-9-clean", Archived: true})
	archived := Item{Status: factory.Done, Factory: factory.Factory{Repo: repo, Slug: "eng-9-clean", State: &state.State{Ticket: "ENG-9"}, Entry: entry}}
	model := NewModel(Model{Config: cfg, Loader: &Loader{Config: cfg, Registry: registry, Now: time.Now}})
	model.SetSnapshot(Snapshot{At: time.Now(), Items: []Item{archived}})
	for _, name := range []string{"!!!", "ENG-9"} {
		if err := model.open(OpenRequest{Name: name}); err == nil {
			t.Errorf("open %q succeeded with nothing created or selected; want the reason", name)
		}
	}

	model.selectorKey("H")
	model.choose(archived.Factory.Key())
	model.selectorKey("a")
	if registry.Find(repo, "eng-9-clean").Archived {
		t.Error("a on the archived ENG-9 row left it archived; the ticket could never get its factory back")
	}
}

func TestOpenRefusesARepoTheRunningHubDoesNotList(t *testing.T) {
	model := NewModel(Model{Config: &state.Config{Repos: []string{"/r/cowork"}}})
	err := model.open(OpenRequest{Name: "ENG-1", Repo: "/r/added-later"})
	if err == nil || !strings.Contains(err.Error(), "does not list /r/added-later") {
		t.Errorf("open in a repo the hub never discovers = %v; the factory would get no row", err)
	}
}
