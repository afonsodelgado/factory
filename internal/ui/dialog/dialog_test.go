package dialog

import (
	"bytes"
	"errors"
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

type fakeRemoteActions struct {
	added  []string
	checks []CheckRow
}

func (f *fakeRemoteActions) Add(name, target, identity string) error {
	f.added = append(f.added, name, target, identity)
	return nil
}

func (f *fakeRemoteActions) Check(string) []CheckRow  { return f.checks }
func (f *fakeRemoteActions) SetDefault(string) error  { return nil }
func (f *fakeRemoteActions) Remove(string) error      { return nil }
func (f *fakeRemoteActions) SetPush(state.Push) error { return nil }
func (f *fakeRemoteActions) NewTopic() string         { return "factory-q7rk2m9xw4tzc3vb" }
func (f *fakeRemoteActions) TestPush() string         { return "" }

func remotesPopup(c RemotesContext, actions RemoteActions) *Remotes {
	r := NewRemotes(c, actions)
	r.Update(tea.WindowSizeMsg{Width: 86, Height: 24})
	return r
}

func pressRemotes(r *Remotes, keys ...tea.KeyPressMsg) tea.Cmd {
	var cmd tea.Cmd
	for _, k := range keys {
		_, cmd = r.Update(k)
	}
	return cmd
}

func typed(text string) []tea.KeyPressMsg {
	var keys []tea.KeyPressMsg
	for _, ch := range text {
		keys = append(keys, tea.KeyPressMsg{Code: ch, Text: string(ch)})
	}
	return keys
}

func TestRemotesPopupFirstSetupAddsTheRemoteEvenWhenItsCheckFails(t *testing.T) {
	actions := &fakeRemoteActions{checks: []CheckRow{
		{Name: "ssh", Mark: "ok", Detail: "afonso@homelab:22 in 0.4s"},
		{Name: "claude", Mark: "fail", Detail: "2.1.312, but not logged in", Fix: "ssh -t homelab claude  then /login, and quit"},
		{Name: "bundle", Mark: "skip", Detail: "skipped, needs claude"},
	}}
	r := remotesPopup(RemotesContext{Local: 3}, actions)
	pressRemotes(r, typed("a")...)
	pressRemotes(r, typed("homelab")...)
	pressRemotes(r, tea.KeyPressMsg{Code: tea.KeyTab})
	pressRemotes(r, typed("afonso@homelab:22")...)
	check := pressRemotes(r, tea.KeyPressMsg{Code: tea.KeyEnter})

	if want := []string{"homelab", "afonso@homelab:22", ""}; !slices.Equal(actions.added, want) {
		t.Fatalf("Add got %q; want %q, or the typed remote never reaches the config", actions.added, want)
	}
	if check == nil {
		t.Fatal("⏎ on a valid remote ran no check; the developer never learns what is missing on the server")
	}
	r.Update(check())
	golden.RequireEqual(t, r.View().Content)

	pressRemotes(r, tea.KeyPressMsg{Code: tea.KeyEnter})
	if list := r.View().Content; !strings.Contains(list, "this machine") || !strings.Contains(list, "afonso@homelab:22") {
		t.Errorf("after a failing check the list lacks the new remote:\n%s\nit is added either way, as the checked screen says", list)
	}
}

func TestRemotesListShowsLinkDefaultAndPush(t *testing.T) {
	r := remotesPopup(RemotesContext{
		Remotes: []RemoteRow{
			{Name: "homelab", Endpoint: "afonso@homelab:22", Link: "connected", Factories: 4, NeedsYou: 1},
			{Name: "build-box", Endpoint: "ci@build-box", Link: "unreachable", Factories: 2},
		},
		Default: "homelab",
		Local:   3,
		Push:    state.Push{Topic: "factory-q7rk2m9xw4tzc3vb"},
	}, nil)
	golden.RequireEqual(t, r.View().Content)
}

func TestNewFactoryChecksIDsOnRemotesWithoutWaitingForThem(t *testing.T) {
	context := NewContext{
		Repos:   []RepoOption{{Name: "cowork", Path: "/r/cowork", Display: "~/dev/cowork", Browser: true, GitURL: "git@github.com:acme/cowork.git"}},
		Remotes: []string{"homelab"},
		Elsewhere: []Existing{
			{Key: "remote:homelab/s-1/inc-1", Worktree: "csv-export", Label: "csv-export", Status: "needs you", Stage: "requirements", Remote: "homelab"},
			{Worktree: "rate-limits", Label: "rate-limits", Remote: "homelab"},
		},
	}
	typed := func(id string, keys ...string) *NewFactoryModel {
		m := NewFactory(context)
		m.Update(tea.WindowSizeMsg{Width: 86, Height: 26})
		m.Update(probedMsg{remote: "homelab", err: errors.New("ssh: connect to host homelab port 22: timed out")})
		for _, r := range id {
			m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
		}
		for _, k := range keys {
			code := map[string]rune{"left": tea.KeyLeft, "right": tea.KeyRight, "enter": tea.KeyEnter}[k]
			m.Update(tea.KeyPressMsg{Code: code})
		}
		return m
	}

	listed := typed("csv-export", "enter")
	golden.RequireEqual(t, listed.View().Content)
	if r, _ := listed.Result(); !reflect.DeepEqual(r, NewResult{Select: "remote:homelab/s-1/inc-1"}) {
		t.Errorf("an id that runs on homelab gave %+v; want that factory selected, never a second one here", r)
	}
	if cached := typed("rate-limits", "enter"); cached.step != 1 {
		t.Error("an id homelab reported earlier went on to a second factory while homelab is away")
	}
	if away := typed("tag-search", "right", "enter"); away.step != 1 {
		t.Error("a new factory went ahead on an unreachable remote")
	}
	if here := typed("tag-search", "right", "left", "enter"); here.step != 2 {
		t.Error("an unreachable remote blocked a factory on this machine")
	}
}

func TestKeysSheetListsRemotesOnlyOnceOneExists(t *testing.T) {
	sheet := func(remotes bool) string {
		k := &Keys{Context: KeysContext{Remotes: remotes}}
		k.Update(tea.WindowSizeMsg{Width: 124, Height: 17})
		return k.View().Content
	}
	if strings.Contains(sheet(false), "remotes") || !strings.Contains(sheet(true), "remotes") {
		t.Error("the keys sheet must stay as it was until a remote exists, then list R")
	}
}
