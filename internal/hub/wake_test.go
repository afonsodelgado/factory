package hub

import (
	"encoding/json"
	"errors"
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
			if got := PlanWake(c.item, false, hooks, nil).Command; got != c.want {
				t.Errorf("wake runs %q, want %q", got, c.want)
			}
		})
	}

	nothing := PlanWake(gone(factory.Factory{State: &state.State{}}), false, nil, nil)
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
		t.Fatalf("hookFor read session %s; want the pane's own 06c7ff87, or status and delivery read a file frozen since the session moved to a new id", hook.SessionID)
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
	for name, reason := range map[string]string{"!!!": "letters", "ENG-9": "archived", "doctor": "`doctor` is a factory command"} {
		if err := model.open(OpenRequest{Name: name}); err == nil || !strings.Contains(err.Error(), reason) {
			t.Errorf("open %q = %v; want it refused, naming %q", name, err, reason)
		}
	}
	if registry.Find(repo, "doctor") != nil {
		t.Error("open doctor registered a factory: a command word became a worktree, as on cowork")
	}

	model.selectorKey("H")
	model.choose(archived.Factory.Key())
	model.selectorKey("a")
	if registry.Find(repo, "eng-9-clean").Archived {
		t.Error("a on the archived ENG-9 row left it archived; the id could never get its factory back")
	}
}

func TestDeleteSelectsTheNextRowAndFreesTheId(t *testing.T) {
	repo := t.TempDir()
	cfg := &state.Config{Repos: []string{repo}, StateDir: t.TempDir()}
	registry, err := state.LoadRegistry(cfg.RegistryPath())
	if err != nil {
		t.Fatal(err)
	}
	failed := registry.Upsert(state.Entry{Repo: repo, Slug: "eng-1-x", Created: true})
	next := registry.Upsert(state.Entry{Repo: repo, Slug: "eng-2-y", Created: true})
	model := NewModel(Model{Config: cfg, Loader: &Loader{Config: cfg, Registry: registry, Now: time.Now}})
	model.SetSnapshot(Snapshot{At: time.Now(), Items: []Item{
		{Status: factory.FailedToStart, Factory: factory.Factory{Repo: repo, Slug: "eng-1-x", Entry: failed}},
		{Status: factory.FailedToStart, Factory: factory.Factory{Repo: repo, Slug: "eng-2-y", Entry: next}},
	}})
	model.choose(state.Key(repo, "eng-1-x"))

	if _, err := model.delete(state.Key(repo, "eng-1-x")); err != nil {
		t.Fatal(err)
	}
	if model.selected != state.Key(repo, "eng-2-y") {
		t.Errorf("after deleting the selected row, %q is selected; want the next row", model.selected)
	}
	if registry.Find(repo, "eng-1-x") != nil {
		t.Error("the registry entry survived delete, so a new factory with the id would get eng-1-x-2")
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

func TestOpenRefusesARepoTheRunningHubDoesNotListUntilFactoryRunsThere(t *testing.T) {
	t.Setenv("CODEX_HOME", t.TempDir())
	model := NewModel(Model{Config: &state.Config{Repos: []string{"/r/cowork"}, StateDir: t.TempDir()}})
	err := model.open(OpenRequest{Name: "ENG-1", Repo: "/r/added-later"})
	if err == nil || !strings.Contains(err.Error(), "does not list /r/added-later") {
		t.Errorf("open in a repo the hub never discovers = %v; the factory would get no row", err)
	}
	model.addRepo(RepoRequest{Repo: "/r/added-later", Here: true})
	c := model.newContext()
	if len(c.Repos) != 2 || c.Repos[c.Repo].Path != "/r/added-later" || !c.Here {
		t.Errorf("after factory ran in /r/added-later the n dialog offers %d repos with %q picked (here %v); want it listed without a restart and picked, so no list to choose from", len(c.Repos), c.Repos[c.Repo].Path, c.Here)
	}
}

func TestCreatingAFactoryKeepsTheConfiguredToolUnderAModelPickedInTheDialog(t *testing.T) {
	t.Setenv("CODEX_HOME", t.TempDir())
	repo := t.TempDir()
	cfg := &state.Config{Repos: []string{repo}, StateDir: t.TempDir(), Agents: map[string]state.AgentSetting{"engineer": {Tool: state.ToolCodex, Model: "gpt-6-astra", Effort: "high"}}}
	registry, err := state.LoadRegistry(cfg.RegistryPath())
	if err != nil {
		t.Fatal(err)
	}
	model := NewModel(Model{Config: cfg, Loader: &Loader{Config: cfg, Registry: registry, Now: time.Now}})

	err = model.create(dialog.NewResult{Repo: repo, Slug: "csv", Prompt: "Export contacts.", Agents: map[string]state.AgentSetting{"engineer": {Model: "gpt-6.1-sol"}}})
	if err == nil || !strings.Contains(err.Error(), "not attached to tmux") {
		t.Fatalf("create = %v; want it to get as far as tmux, not refuse gpt-6.1-sol as a claude model", err)
	}
	want := state.AgentSetting{Tool: state.ToolCodex, Model: "gpt-6.1-sol", Effort: "high"}
	if got := registry.Find(repo, "csv").Agents["engineer"]; got != want {
		t.Errorf("the engineer was registered as %+v; want %+v", got, want)
	}
}

func TestRepoSettingsWaitsForPopupCloseAndRefreshesTheSelectedRepo(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CODEX_HOME", t.TempDir())
	t.Setenv("FACTORY_CONFIG_DIR", t.TempDir())
	t.Setenv("FACTORY_STATE_DIR", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	repo := t.TempDir()
	os.MkdirAll(state.RepoDir(repo), 0o755)
	os.WriteFile(state.RepoConfigPath(repo), []byte("qa: {mode: terminal}\ncoderabbit: false\n"), 0o644)
	cfg := &state.Config{StateDir: t.TempDir(), Repos: []string{t.TempDir()}}
	loader := &Loader{Config: cfg}
	model := NewModel(Model{Config: cfg, Loader: loader, Engine: &Engine{}, DialogCommand: func(kind string) string { return kind }})
	model.window = [2]int{100, 40}
	model.here = cfg.Repos[0]
	deliver := func(kind string, result any) {
		t.Helper()
		data, err := json.Marshal(result)
		if err != nil {
			t.Fatal(err)
		}
		response := make(chan Message, 1)
		model.dialogResult(Event{Message: Message{T: "result", Pane: "dialog:" + kind, Data: data}, Reply: response})
		if reply := <-response; reply.T != "done" {
			t.Fatalf("not acknowledged: %+v", reply)
		}
	}
	if model.openDialog("new", model.newContext()) == nil {
		t.Fatal("new did not open")
	}
	if model.openDialog("settings", dialog.SettingsContext{Repo: repo}) != nil || model.dialog != "new" {
		t.Fatal("overlapping popup opened")
	}
	deliver("new", dialog.NewResult{Settings: &dialog.SettingsContext{Repo: repo, Banner: "added"}})
	if model.dialog != "new" || model.dialogs["settings"] != nil || model.dialogNext == nil {
		t.Fatal("result opened settings before popup closure")
	}
	if len(cfg.Repos) != 2 || !slices.Contains(loader.Config.Repos, repo) {
		t.Fatal("repo not shared with loader")
	}
	if model.dialogClosed(dialogClosedMsg{kind: "new"}) == nil || model.dialog != "settings" || model.dialogNext != nil {
		t.Fatal("closure did not consume settings transition")
	}
	if model.dialogClosed(dialogClosedMsg{kind: "new"}) != nil || model.dialog != "settings" {
		t.Fatal("duplicate close consumed the next popup")
	}
	for _, saved := range []bool{true, false} {
		os.WriteFile(state.RepoConfigPath(repo), []byte("qa: {mode: browser}\ncoderabbit: true\ndefault_pipeline: quick\nagents: {engineer: {model: sonnet}}\n"), 0o644)
		deliver("settings", dialog.SettingsResult{Repo: repo, Saved: saved})
		if model.dialog != "settings" || model.dialogs["new"] != nil {
			t.Fatal("settings result reopened n too early")
		}
		if model.dialogClosed(dialogClosedMsg{kind: "settings"}) == nil {
			t.Fatal("n did not reopen")
		}
		var context dialog.NewContext
		if err := json.Unmarshal(model.dialogs["new"], &context); err != nil {
			t.Fatal(err)
		}
		picked := context.Repos[context.Repo]
		if picked.Path != repo || context.Here || !picked.Browser || !picked.CodeRabbit || picked.DefaultPipeline != "quick" || context.StateDir != cfg.StateDir {
			t.Fatalf("stale return context: %+v", context)
		}
		engineer := slices.IndexFunc(picked.Agents, func(a dialog.AgentOption) bool { return a.Role == "engineer" })
		if engineer < 0 || picked.Agents[engineer].Model != "sonnet" || (context.Notice != "") != saved {
			t.Fatal("agents or notice stale")
		}
		if cmd := model.dialogClosed(dialogClosedMsg{kind: "new"}); cmd != nil || model.dialogNext != nil {
			t.Fatal("plain cancel chained")
		}
		model.openDialog("settings", dialog.SettingsContext{Repo: repo})
	}
	deliver("settings", dialog.SettingsResult{Repo: repo, Saved: true})
	if model.dialogClosed(dialogClosedMsg{kind: "settings", err: errors.New("popup failed")}) != nil || model.dialogNext != nil || model.dialog != "" {
		t.Fatal("failed popup chained")
	}
	model.openDialog("new", model.newContext())
	if model.dialogClosed(dialogClosedMsg{kind: "new"}) != nil {
		t.Fatal("ordinary cancellation reused a failed transition")
	}
	model.here = repo
	model.openDialog("settings", dialog.SettingsContext{Repo: repo})
	deliver("settings", dialog.SettingsResult{Repo: repo})
	model.dialogClosed(dialogClosedMsg{kind: "settings"})
	var context dialog.NewContext
	json.Unmarshal(model.dialogs["new"], &context)
	if !context.Here || context.Repos[context.Repo].Path != repo {
		t.Fatal("current repo lost Here on return")
	}
}
