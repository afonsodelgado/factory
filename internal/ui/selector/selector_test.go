package selector_test

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/exp/golden"
	"github.com/charmbracelet/x/exp/teatest/v2"

	"github.com/afonsodelgado/factory/internal/factory"
	"github.com/afonsodelgado/factory/internal/hub"
	"github.com/afonsodelgado/factory/internal/state"
	"github.com/afonsodelgado/factory/internal/ui/selector"
	"github.com/afonsodelgado/factory/internal/ui/theme"
)

var now = time.Date(2026, 10, 22, 15, 0, 0, 0, time.UTC)

func fleet() hub.Snapshot {
	at := func(d time.Duration) state.Time { return state.Time{Time: now.Add(-d)} }
	item := func(repo, label, stage string, status factory.Status, since time.Duration) hub.Item {
		st := &state.State{SchemaVersion: 2, ID: label, Stage: stage, Stages: map[string]state.Stage{"design": {Status: "skipped"}}}
		for _, key := range state.StageKeys {
			if key == stage {
				break
			}
			if key != "design" {
				st.Stages[key] = state.Stage{Status: "done"}
			}
		}
		if status == factory.Done {
			st.Stages["handoff"] = state.Stage{Status: "done"}
		}
		st.History = []state.HistoryEntry{{At: at(since), Stage: stage, Event: "started"}}
		f := factory.Factory{Repo: repo, Slug: strings.ToLower(label), State: st, Entry: &state.Entry{CreatedAt: now.Add(-since)}}
		return hub.Item{Factory: f, Status: status}
	}
	cowork, factoryRepo := "/r/cowork", "/r/factory"
	needs := item(cowork, "ENG-7452", "requirements", factory.NeedsYou, 6*time.Minute)
	needs.Factory.State.Attention = []state.Attention{{ID: "q-1"}, {ID: "q-2"}}
	starting := hub.Item{Factory: factory.Factory{Repo: cowork, Slug: "csv-dedupe-preview", Entry: &state.Entry{Created: true, CreatedAt: now.Add(-40 * time.Second)}}, Status: factory.Starting}
	failed := hub.Item{Factory: factory.Factory{Repo: cowork, Slug: "never-started", Entry: &state.Entry{Created: true, CreatedAt: now.Add(-7 * time.Minute)}}, Status: factory.NeedsYou, Unreported: true}
	archived := item(cowork, "ENG-7341", "handoff", factory.Done, 72*time.Hour)
	archived.Factory.Entry.Archived = true
	return hub.Snapshot{At: now, Items: []hub.Item{
		item(cowork, "ENG-7445", "qa", factory.Gone, 9*time.Hour),
		item(cowork, "ENG-7447", "pr", factory.WaitingCI, 12*time.Minute),
		item(cowork, "ENG-7449", "implement", factory.Running, 27*time.Minute),
		item(cowork, "ENG-7450", "review", factory.Stalled, 3*time.Hour),
		needs,
		starting,
		failed,
		item(cowork, "ENG-7422", "handoff", factory.Done, 30*time.Hour),
		archived,
		item(factoryRepo, "factory-tui", "design", factory.WaitingAgent, 18*time.Minute),
		item(factoryRepo, "doctor-json-output", "plan", factory.Exited, 22*time.Minute),
	}}
}

func render(t *testing.T, width, height int, keys ...string) string {
	t.Helper()
	model := hub.NewModel(hub.Model{Config: &state.Config{Repos: []string{"/r/cowork", "/r/factory"}}, Now: func() time.Time { return now }, Icons: theme.Nerd})
	model.SetSnapshot(fleet())
	tm := teatest.NewTestModel(t, model, teatest.WithInitialTermSize(width, height), teatest.WithProgramOptions(tea.WithColorProfile(colorprofile.TrueColor)))
	for _, key := range keys {
		tm.Send(tea.KeyPressMsg{Code: rune(key[0]), Text: key})
	}
	if err := tm.Quit(); err != nil {
		t.Fatal(err)
	}
	return tm.FinalModel(t, teatest.WithFinalTimeout(5*time.Second)).View().Content
}

func TestSelectorEveryStatusInTwoRepos(t *testing.T) {
	golden.RequireEqual(t, render(t, hub.SelectorWidth, 44, "j", "j"))
}

func TestSelectorNarrowFold(t *testing.T) {
	golden.RequireEqual(t, render(t, hub.NarrowSelectorWidth, 44))
}

func TestHighlightedIconKeepsItsPadCell(t *testing.T) {
	selection := "48;2;41;46;66"
	for status := factory.Running; status <= factory.Done; status++ {
		row := selector.Row{Key: "k", Label: "ENG-1", Status: status, Track: make([]factory.Step, len(state.StageKeys)), Time: "1m"}
		d := selector.Data{Groups: selector.Arrange([]selector.Group{{Name: "repo", Rows: []selector.Row{row}}}), Selected: "k", Focused: true, Icons: theme.Nerd}
		out := selector.Render(d, hub.SelectorWidth, 6)
		glyph := theme.Nerd.Glyph(theme.StatusIcon(status))
		i := strings.Index(out, glyph)
		if i < 0 {
			t.Fatalf("%s: icon %q not rendered", status, glyph)
		}
		if !strings.HasPrefix(out[i+len(glyph):], " ") {
			t.Errorf("%s: the cell after the icon is not a pad in the icon's own style: %q", status, out[i:i+len(glyph)+12])
		}
		sgr := out[strings.LastIndex(out[:i], "\x1b["):i]
		if !strings.Contains(sgr, selection) {
			t.Errorf("%s: the icon is drawn without the selection background (%q)", status, sgr)
		}
	}
}
