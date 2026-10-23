package dialog

import (
	"bytes"
	"reflect"
	"slices"
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
		RemoteControl: true,
		Repos: []RepoOption{
			{Name: "cowork", Path: "/r/cowork", Display: "~/dev/cowork", Browser: true, CodeRabbit: true, Existing: []Existing{{Key: "/r/cowork:eng-7450-ssrf-guard", Worktree: "eng-7450-ssrf-guard", Label: "ENG-7450", Status: "stalled", Stage: "review"}}},
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
	m.name, m.prompt, m.step = "csv", "Export contacts.", 2
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
		Agents:     map[string]state.AgentSetting{"engineer": {Tool: state.ToolCodex, Model: "gpt-5.6-sol"}},
		Supervisor: state.AgentSetting{Tool: state.ToolCodex, Model: "gpt-5.6-sol", Effort: "low"},
	}
	if !reflect.DeepEqual(r, want) {
		t.Errorf("switching the engineer and supervisor to codex and pressing t on qa started\n%+v\nwant\n%+v\nQA drives Chrome and must stay on claude, and a tool that is not installed must never be picked", r, want)
	}
}
