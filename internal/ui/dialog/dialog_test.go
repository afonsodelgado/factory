package dialog

import (
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/exp/golden"
	"github.com/charmbracelet/x/exp/teatest/v2"
)

func TestNewFactoryNamesTheExactTicketMatch(t *testing.T) {
	context := NewContext{
		RemoteControl: true,
		Repos: []RepoOption{
			{Name: "cowork", Path: "/r/cowork", Display: "~/dev/cowork", Browser: true, CodeRabbit: true, Existing: []Existing{{Key: "/r/cowork:eng-7450-ssrf-guard", Worktree: "eng-7450-ssrf-guard", Label: "ENG-7450", Status: "stalled", Stage: "review"}}},
			{Name: "factory", Path: "/r/factory", Display: "~/dev/factory"},
		},
	}
	tm := teatest.NewTestModel(t, NewFactory(context), teatest.WithInitialTermSize(84, 22), teatest.WithProgramOptions(tea.WithColorProfile(colorprofile.TrueColor)))
	tm.Type("ENG-745")
	if err := tm.Quit(); err != nil {
		t.Fatal(err)
	}
	final := tm.FinalModel(t, teatest.WithFinalTimeout(5*time.Second)).(*NewFactoryModel)
	if _, ok := final.Result(); ok {
		t.Fatal("typing a name must not start a factory")
	}
	golden.RequireEqual(t, final.View().Content)
}
