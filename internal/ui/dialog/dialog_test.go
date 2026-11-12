package dialog

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/exp/golden"
	"github.com/charmbracelet/x/exp/teatest/v2"

	"github.com/afonsodelgado/factory/internal/catalog"
	"github.com/afonsodelgado/factory/internal/state"
)

func TestNewFactorySelectsTheFactoryWithTheTypedID(t *testing.T) {
	context := NewContext{
		Repos: []RepoOption{
			{Name: "cowork", Path: "/r/cowork", Display: "~/dev/cowork", Browser: true, CodeRabbit: true, RemoteControl: true, Existing: []Existing{{Key: "/r/cowork:eng-7450-ssrf-guard", Worktree: "eng-7450-ssrf-guard", Label: "ENG-7450", Status: "stalled", Stage: "review"}}},
			{Name: "factory", Path: "/r/factory", Display: "~/dev/factory"},
		},
	}
	tm := teatest.NewTestModel(t, NewFactory(context), teatest.WithInitialTermSize(84, 22), teatest.WithProgramOptions(tea.WithColorProfile(colorprofile.TrueColor)))
	tm.Type("eng-7450")
	teatest.WaitFor(t, tm.Output(), func(b []byte) bool { return bytes.Contains(b, []byte("select it")) }, teatest.WithDuration(5*time.Second))
	tm.Send(tea.KeyPressMsg{Code: tea.KeyEnter})
	final := tm.FinalModel(t, teatest.WithFinalTimeout(5*time.Second)).(*NewFactoryModel)
	golden.RequireEqual(t, final.View().Content)
	if r, _ := final.Result(); !reflect.DeepEqual(r, NewResult{Select: "/r/cowork:eng-7450-ssrf-guard"}) {
		t.Errorf("typing an existing factory's id gave %+v; want that factory selected, never a second one", r)
	}
}

func TestNewFactoryStartsWithThePickedPipeline(t *testing.T) {
	off := false
	pipelines, err := state.ResolvePipelines(&state.Config{}, state.Profile{CodeRabbit: &off}, "")
	if err != nil {
		t.Fatal(err)
	}
	context := NewContext{Repos: []RepoOption{{Name: "factory", Path: "/r/factory", Display: "~/dev/factory", Pipelines: pipelines.List, DefaultPipeline: pipelines.Default}}}
	tm := teatest.NewTestModel(t, NewFactory(context), teatest.WithInitialTermSize(88, 30), teatest.WithProgramOptions(tea.WithColorProfile(colorprofile.TrueColor)))
	tm.Type("ssrf-fix")
	tm.Send(tea.KeyPressMsg{Code: tea.KeyEnter})
	teatest.WaitFor(t, tm.Output(), func(b []byte) bool { return bytes.Contains(b, []byte("2 of 3")) }, teatest.WithDuration(5*time.Second))
	tm.Send(tea.KeyPressMsg{Code: tea.KeyTab})
	tm.Send(tea.KeyPressMsg{Code: tea.KeyEnter})
	tm.Send(tea.PasteMsg{Content: "Block private addresses\x1b[106;5uin the webhook fetcher."})
	tm.Send(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	final := tm.FinalModel(t, teatest.WithFinalTimeout(5*time.Second)).(*NewFactoryModel)
	golden.RequireEqual(t, final.View().Content)
	r, _ := final.Result()
	if r.(NewResult).Pipeline != "quick" || r.(NewResult).Stages != nil {
		t.Errorf("picking quick on the pipeline step started %+v; want the quick pipeline, or a small fix runs every stage", r)
	}
	if prompt := r.(NewResult).Prompt; prompt != "Block private addresses\nin the webhook fetcher." {
		t.Errorf("prompt = %q; want ctrl+j in CSI u form as a newline, or escape bytes reach brief.md and the title", prompt)
	}
}

func TestNewFactoryStartsWithItsOwnStagesAndAgents(t *testing.T) {
	pipelines, err := state.ResolvePipelines(&state.Config{}, state.Profile{}, "")
	if err != nil {
		t.Fatal(err)
	}
	agents := state.ResolveAgents(&state.Config{}, state.Profile{Agents: map[string]state.AgentSetting{"reviewer": {Effort: "low"}}}, nil)
	repo := RepoOption{Name: "cowork", Path: "/r/cowork", Display: "~/dev/cowork", CodeRabbit: true, Pipelines: pipelines.List, DefaultPipeline: pipelines.Default}
	for _, role := range state.AgentRoles {
		repo.Agents = append(repo.Agents, AgentOption{Role: role, AgentSetting: agents[role]})
	}
	tm := teatest.NewTestModel(t, NewFactory(NewContext{Repos: []RepoOption{repo}}), teatest.WithInitialTermSize(88, 30), teatest.WithProgramOptions(tea.WithColorProfile(colorprofile.TrueColor)))
	tm.Type("csv-export")
	tm.Send(tea.KeyPressMsg{Code: tea.KeyEnter})
	teatest.WaitFor(t, tm.Output(), func(b []byte) bool { return bytes.Contains(b, []byte("Stages & agents")) }, teatest.WithDuration(5*time.Second))
	tm.Send(tea.KeyPressMsg{Code: tea.KeyDown})
	tm.Send(tea.KeyPressMsg{Code: tea.KeySpace})
	tm.Send(tea.KeyPressMsg{Code: tea.KeyDown})
	tm.Send(tea.KeyPressMsg{Code: tea.KeyDown})
	tm.Type("m")
	tm.Send(tea.KeyPressMsg{Code: tea.KeyEnter})
	tm.Type("Export contacts as CSV.")
	tm.Send(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	final := tm.FinalModel(t, teatest.WithFinalTimeout(5*time.Second)).(*NewFactoryModel)
	golden.RequireEqual(t, final.View().Content)
	r, _ := final.Result()
	want := NewResult{
		Repo: "/r/cowork", Slug: "csv-export", Prompt: "Export contacts as CSV.", Pipeline: state.CustomPipeline,
		Stages: []string{"requirements", "plan", "implement", "review", "coderabbit", "qa", "qa_fixes", "test_gaps", "handoff"},
		Agents: map[string]state.AgentSetting{"engineer": {Model: "fable"}},
	}
	if !reflect.DeepEqual(r, want) {
		t.Errorf("unchecking design and changing the engineer's model started\n%+v\nwant\n%+v\nonly this factory's choices may travel, never the repo's own defaults", r, want)
	}
}

func TestNewFactorySwitchesRolesToCodexButKeepsQAOnClaude(t *testing.T) {
	codex := ToolOption{Name: state.ToolCodex, Models: []catalog.Model{{ID: "gpt-5.6-sol", Efforts: []string{"low", "high"}}}}
	notInstalled := ToolOption{Name: state.ToolOpenCode, Problem: "opencode is not installed."}
	m := NewFactory(NewContext{Repos: []RepoOption{{Name: "cowork", Path: "/r/cowork"}}, Tools: []ToolOption{{Name: state.ToolClaude, Models: catalog.Claude()}, codex, notInstalled}})
	m.name, m.prompt.text, m.step = "csv", "Export contacts.", 2
	press := func(row int, key string) {
		m.row = row
		m.Update(tea.KeyPressMsg{Code: rune(key[0]), Text: key})
	}
	rowOf := func(stage string) int { return firstStageRow + slices.Index(state.StageKeys, stage) }

	press(rowOf("implement"), "t")
	press(rowOf("qa"), "t")
	press(0, "t")
	press(0, "e")
	m.start()

	r, _ := m.Result()
	want := NewResult{Repo: "/r/cowork", Slug: "csv", Prompt: "Export contacts.", Pipeline: m.pipelineName(),
		Agents:     map[string]state.AgentSetting{"engineer": {Tool: state.ToolCodex, Model: "gpt-5.6-sol", Effort: "high"}},
		Supervisor: state.AgentSetting{Tool: state.ToolCodex, Model: "gpt-5.6-sol", Effort: "low"},
	}
	if !reflect.DeepEqual(r, want) {
		t.Errorf("switching the engineer and supervisor to codex and pressing t on qa started\n%+v\nwant\n%+v\nQA drives Chrome and must stay on claude, and a tool that is not installed must never be picked", r, want)
	}
}

func TestCyclingARolesToolPicksThatToolsDefaultModelAndEffort(t *testing.T) {
	codex := ToolOption{Name: state.ToolCodex, Default: state.AgentSetting{Model: "gpt-6-luna", Effort: "xhigh"}, Models: []catalog.Model{
		{ID: "gpt-5.6-sol", Efforts: []string{"low", "high"}},
		{ID: "gpt-6-luna", Efforts: []string{"low", "medium", "max"}},
	}}
	openCode := ToolOption{Name: state.ToolOpenCode, Models: []catalog.Model{{ID: "flint-40b-free", Efforts: []string{"low", "high", "max"}}}}
	agents := state.ResolveAgents(&state.Config{Agents: map[string]state.AgentSetting{"engineer": {Tool: state.ToolCodex}}}, state.Profile{}, nil)
	repo := RepoOption{Name: "cowork", Path: "/r/cowork"}
	for _, role := range state.AgentRoles {
		repo.Agents = append(repo.Agents, AgentOption{Role: role, AgentSetting: agents[role]})
	}
	m := NewFactory(NewContext{Repos: []RepoOption{repo}, Tools: []ToolOption{{Name: state.ToolClaude, Models: catalog.Claude()}, codex, openCode}})
	m.step = 2
	tool := func(stage string) state.AgentSetting {
		m.row = firstStageRow + slices.Index(state.StageKeys, stage)
		m.Update(tea.KeyPressMsg{Code: 't', Text: "t"})
		return m.agent(state.RoleOf(stage))
	}

	steps := []struct {
		stage, why string
		want       state.AgentSetting
	}{
		{"review", "codex's configured model, at its highest effort up to high", state.AgentSetting{Tool: state.ToolCodex, Model: "gpt-6-luna", Effort: "medium"}},
		{"review", "the first free model, at its highest variant up to high", state.AgentSetting{Tool: state.ToolOpenCode, Model: "flint-40b-free", Effort: "high"}},
		{"implement", "the same, from a codex default", state.AgentSetting{Tool: state.ToolOpenCode, Model: "flint-40b-free", Effort: "high"}},
		{"implement", "the engineer's frontmatter, not the first claude model", state.AgentSetting{Tool: state.ToolClaude, Model: "opus", Effort: "xhigh"}},
	}
	for _, s := range steps {
		if got := tool(s.stage); got != s.want {
			t.Errorf("t on %s gave %+v; want %+v: %s", s.stage, got, s.want, s.why)
		}
	}
}

func TestNewFactoryPromptEditsWhereTheCursorIs(t *testing.T) {
	m := NewFactory(NewContext{Repos: []RepoOption{{Name: "cowork", Path: "/r/cowork"}}})
	m.name, m.step = "ssrf", 3
	m.Update(tea.WindowSizeMsg{Width: 40, Height: 20})
	press := func(keys ...tea.KeyPressMsg) {
		for _, k := range keys {
			m.Update(k)
			m.View()
		}
	}
	up := tea.KeyPressMsg{Code: tea.KeyUp}
	m.Update(tea.PasteMsg{Content: "fix it\nreject  private addresses in the webhook fetcher"})
	press(up)
	m.Update(tea.PasteMsg{Content: "X"})
	press(up, tea.KeyPressMsg{Code: tea.KeyBackspace, Mod: tea.ModAlt})
	m.Update(tea.PasteMsg{Content: "the bug"})
	m.start()

	r, _ := m.Result()
	if got, want := r.(NewResult).Prompt, "fix the bug\nreject  private addXresses in the webhook fetcher"; got != want {
		t.Errorf("editing above a wrapped line gave %q; want %q: the cursor must land where it is drawn, or edits go to the wrong place", got, want)
	}
}

func TestRepoSetupFailureStaysInTheDialogUntilRetrySucceeds(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("FACTORY_CONFIG_DIR", t.TempDir())
	t.Setenv("FACTORY_STATE_DIR", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	for _, route := range []string{"add", "ctrl+e"} {
		for _, config := range []string{"missing", "legacy", "current", "partial failure", "malformed"} {
			t.Run(route+"/"+config, func(t *testing.T) {
				repo := t.TempDir()
				git := func(args ...string) {
					t.Helper()
					if out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput(); err != nil {
						t.Fatalf("git: %s: %v", out, err)
					}
				}
				git("init", "-q")
				if config == "legacy" {
					os.MkdirAll(filepath.Dir(state.LegacyProfilePath(repo)), 0o755)
					os.WriteFile(state.LegacyProfilePath(repo), []byte("commands: {build: legacy}\n"), 0o644)
				}
				if config == "current" || config == "malformed" {
					os.MkdirAll(state.RepoDir(repo), 0o755)
					data := "commands: {build: current}\n"
					if config == "malformed" {
						data = "commands: [\n"
					}
					os.WriteFile(state.RepoConfigPath(repo), []byte(data), 0o644)
				}
				context := NewContext{Repos: []RepoOption{{Path: repo, Name: "repo"}}, StateDir: t.TempDir()}
				m := NewFactory(context)
				if route == "add" {
					m.repo, m.repoPath = len(context.Repos), repo
				}
				press := func() {
					if route == "add" {
						m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
					} else {
						m.Update(tea.KeyPressMsg{Code: 'e', Mod: tea.ModCtrl})
					}
				}
				exclude := filepath.Join(repo, ".git", "info", "exclude")
				before, _ := os.ReadFile(exclude)
				if config == "partial failure" {
					os.Remove(exclude)
					os.Mkdir(exclude, 0o755)
				}
				press()
				if config == "partial failure" {
					if _, ok := m.Result(); ok || m.problem == "" {
						t.Fatal("setup failure closed the dialog")
					}
					if _, err := os.Stat(state.RepoConfigPath(repo)); err != nil {
						t.Fatal("failure did not exercise partial setup")
					}
					if data, _ := os.ReadFile(state.KnownReposPath(context.StateDir)); len(data) != 0 {
						t.Fatal("failed setup remembered the repo")
					}
					os.Remove(exclude)
					press()
				}
				result, ok := m.Result()
				if !ok || !sameRepo(result.(NewResult).Settings.Repo, repo) {
					t.Fatalf("no settings intent: %v, %s", result, m.problem)
				}
				if config == "legacy" {
					data, _ := os.ReadFile(state.RepoConfigPath(repo))
					if !strings.Contains(string(data), "legacy") {
						t.Fatal("legacy config not copied")
					}
				}
				if config == "current" || config == "malformed" {
					after, _ := os.ReadFile(exclude)
					if !bytes.Equal(before, after) {
						t.Fatal("listed current repo ran setup")
					}
				}
			})
		}
	}
	t.Run("unlisted worktree and invalid paths", func(t *testing.T) {
		repo := t.TempDir()
		run := func(args ...string) {
			t.Helper()
			if out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput(); err != nil {
				t.Fatalf("git: %s: %v", out, err)
			}
		}
		run("init", "-q")
		run("-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-qm", "initial")
		worktree := filepath.Join(t.TempDir(), "worktree")
		run("worktree", "add", "-qb", "work", worktree)
		m := NewFactory(NewContext{StateDir: t.TempDir()})
		for _, path := range []string{filepath.Join(t.TempDir(), "missing"), t.TempDir()} {
			m.repoPath = path
			m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
			if _, ok := m.Result(); ok || m.problem == "" || m.repoPath != path {
				t.Fatal("invalid path escaped the dialog")
			}
		}
		m.repoPath = worktree
		m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		result, ok := m.Result()
		if !ok {
			t.Fatal(m.problem)
		}
		settings := result.(NewResult).Settings
		if !sameRepo(settings.Repo, repo) || !strings.Contains(settings.Banner, "worktree") {
			t.Fatalf("worktree: %+v", settings)
		}
		nested := filepath.Join(repo, "nested")
		os.Mkdir(nested, 0o755)
		m = NewFactory(NewContext{Repos: []RepoOption{{Path: repo}}, StateDir: t.TempDir()})
		m.repo, m.repoPath = 1, nested
		m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		result, ok = m.Result()
		if !ok || result.(NewResult).Settings.Repo != repo || strings.Contains(result.(NewResult).Settings.Banner, "worktree") {
			t.Fatalf("nested duplicate: %+v, %s", result, m.problem)
		}
	})
}

func TestRepoSettingsRoundTripKeepsInputAndUnsavedFiles(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, name := range []string{"répo one", "répo two"} {
		os.Mkdir(filepath.Join(home, name), 0o755)
	}
	empty := NewFactory(NewContext{})
	if !empty.addingRepo() || empty.repoPath != "" {
		t.Fatal("empty hub did not focus empty path")
	}
	m := NewFactory(NewContext{Repos: []RepoOption{{Path: filepath.Join(home, "répo one")}, {Path: filepath.Join(home, "répo two")}}, Here: true, Notice: "saved"})
	m.Update(tea.PasteMsg{Content: "factory-id"})
	m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if m.Context.Here || m.repo != 1 || m.Context.Notice != "" {
		t.Fatal("Here navigation or notice did not reset")
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	m.Update(tea.KeyPressMsg{Code: 'u', Mod: tea.ModCtrl})
	m.Update(tea.PasteMsg{Content: "~/ré"})
	m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	if m.repoPath != "~/répo " || m.name != "factory-id" {
		t.Fatalf("buffers/completion: %q, %q", m.repoPath, m.name)
	}
	m.Update(tea.PasteMsg{Content: "o"})
	m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	if m.repoPath != "~/répo one/" {
		t.Fatal(m.repoPath)
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if m.repo != 0 {
		t.Fatal("add row did not wrap")
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	if m.repo != 0 {
		t.Fatal("up wrapped")
	}
	if got := completeDirectory("~/no-match"); got.suffix != "" || got.problem == "" {
		t.Fatalf("no match: %+v", got)
	}
	if got := completeDirectory("~/missing/path"); got.problem == "" {
		t.Fatal("read error hidden")
	}
	if got := completeDirectory("~"); got.suffix != "/répo " {
		t.Fatalf("home completion: %+v", got)
	}
	m.Update(tea.WindowSizeMsg{Width: 86, Height: 28})
	m.Context.Repos[0].Existing = []Existing{{Label: "factory-id", Key: "existing"}}
	if !strings.Contains(m.View().Content, "ctrl+e settings") {
		t.Fatal("existing id hides settings")
	}
	m.repo = len(m.Context.Repos)
	if strings.Contains(m.View().Content, "ctrl+e settings") {
		t.Fatal("add offers settings key")
	}
	repo := t.TempDir()
	os.MkdirAll(state.RepoDir(repo), 0o755)
	original := "commands: {build: old, lint: lint, test: test}\nqa: {mode: browser, start: serve, health: ready}\ncoderabbit: false\n"
	os.WriteFile(state.RepoConfigPath(repo), []byte(original), 0o644)
	for _, outcome := range []string{"cancel", "save", "failed write", "malformed", "missing file"} {
		t.Run(outcome, func(t *testing.T) {
			os.WriteFile(state.RepoConfigPath(repo), []byte(original), 0o644)
			if outcome == "malformed" {
				os.WriteFile(state.RepoConfigPath(repo), []byte("commands: [\n"), 0o644)
			}
			if outcome == "missing file" {
				os.Remove(state.RepoConfigPath(repo))
			}
			settings := NewSettings(SettingsContext{Repo: repo})
			settings.Update(tea.WindowSizeMsg{Width: 86, Height: 28})
			if outcome == "malformed" || outcome == "missing file" {
				if settings.settings != nil || !strings.Contains(settings.View().Content, "esc close") || strings.Contains(settings.View().Content, "ctrl+s") {
					t.Fatal("read refusal showed editable fields")
				}
				settings.Update(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
				if _, ok := settings.Result(); ok {
					t.Fatal("bad file saved")
				}
				settings.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
				if r, ok := settings.Result(); !ok || r.(SettingsResult).Saved {
					t.Fatal("bad file cannot return")
				}
				return
			}
			if settings.values["commands.build"] != "old" || len(settings.fields()) != 7 {
				t.Fatal("file not loaded")
			}
			settings.Update(tea.KeyPressMsg{Code: 'u', Mod: tea.ModCtrl})
			settings.Update(tea.PasteMsg{Content: "new"})
			settings.field = 4
			settings.Update(tea.PasteMsg{Content: " edited"})
			settings.field = 3
			settings.Update(tea.KeyPressMsg{Code: tea.KeySpace})
			if len(settings.fields()) != 5 || settings.values["qa.start"] != "serve edited" {
				t.Fatal("terminal fields lost edits")
			}
			settings.Update(tea.KeyPressMsg{Code: tea.KeySpace})
			if len(settings.fields()) != 7 {
				t.Fatal("browser fields not restored")
			}
			settings.Update(tea.KeyPressMsg{Code: tea.KeySpace})
			settings.Update(tea.WindowSizeMsg{Width: 60, Height: 14})
			if !strings.Contains(settings.View().Content, "QA mode") || !strings.Contains(settings.View().Content, "ctrl+s save") {
				t.Fatal("short terminal hides focused field or action")
			}
			if outcome == "cancel" {
				settings.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
			} else {
				if outcome == "failed write" {
					os.Chmod(state.RepoDir(repo), 0o500)
				}
				settings.Update(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
				os.Chmod(state.RepoDir(repo), 0o755)
			}
			data, _ := os.ReadFile(state.RepoConfigPath(repo))
			if outcome != "save" && string(data) != original {
				t.Fatal("unsaved file changed")
			}
			if outcome == "failed write" {
				if _, ok := settings.Result(); ok || settings.problem == "" || settings.values["commands.build"] != "new" {
					t.Fatal("write failure lost edits or exited")
				}
				settings.Update(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
			}
			result, ok := settings.Result()
			if !ok || result.(SettingsResult).Saved != (outcome != "cancel") {
				t.Fatalf("wrong return: %+v", result)
			}
			if outcome == "save" && (!strings.Contains(string(data), "build: new") || !strings.Contains(string(data), "start: serve,")) {
				t.Fatalf("save lost values: %s", data)
			}
		})
	}
}
