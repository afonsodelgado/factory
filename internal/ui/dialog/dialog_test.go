package dialog

import (
	"bytes"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/exp/golden"
	"github.com/charmbracelet/x/exp/teatest/v2"

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
	if r, _ := final.Result(); r != (NewResult{Select: "/r/cowork:eng-7450-ssrf-guard"}) {
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
	tm := teatest.NewTestModel(t, NewFactory(context), teatest.WithInitialTermSize(84, 22), teatest.WithProgramOptions(tea.WithColorProfile(colorprofile.TrueColor)))
	tm.Type("ssrf-fix")
	tm.Send(tea.KeyPressMsg{Code: tea.KeyEnter})
	teatest.WaitFor(t, tm.Output(), func(b []byte) bool { return bytes.Contains(b, []byte("2 of 3")) }, teatest.WithDuration(5*time.Second))
	tm.Send(tea.KeyPressMsg{Code: tea.KeyDown})
	tm.Send(tea.KeyPressMsg{Code: tea.KeyEnter})
	tm.Send(tea.PasteMsg{Content: "Block private addresses\x1b[106;5uin the webhook fetcher."})
	tm.Send(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	final := tm.FinalModel(t, teatest.WithFinalTimeout(5*time.Second)).(*NewFactoryModel)
	golden.RequireEqual(t, final.View().Content)
	r, _ := final.Result()
	if r.(NewResult).Pipeline != "quick" {
		t.Errorf("picking quick on the pipeline step started %+v; want the quick pipeline, or a small fix runs every stage", r)
	}
	if prompt := r.(NewResult).Prompt; prompt != "Block private addresses\nin the webhook fetcher." {
		t.Errorf("prompt = %q; want ctrl+j in CSI u form as a newline, or escape bytes reach brief.md and the title", prompt)
	}
}
