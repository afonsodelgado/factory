package hub

import (
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/exp/golden"
	"github.com/charmbracelet/x/exp/teatest/v2"

	"github.com/afonsodelgado/factory/internal/factory"
	"github.com/afonsodelgado/factory/internal/state"
	"github.com/afonsodelgado/factory/internal/tmux"
	"github.com/afonsodelgado/factory/internal/ui/theme"
)

func questionFactory(hookEvent string, at time.Time) Snapshot {
	batch := state.Attention{
		ID:    "q-7452-1",
		Kind:  "question",
		Title: "3 questions from factory-analyst",
		Body:  "Three answers before the analyst can write requirements.md.",
		Questions: []string{
			"How many failed auth attempts before a client is refused?",
			"What does a rate-limited request get back?",
			"How big may the negative cache grow, and how long is a miss kept?",
		},
		Options:     state.OptionLists{{"10 per minute per IP", "30 per minute per IP"}, {}, {"10,000 entries, 60 s TTL, LRU", "1,000 entries, 300 s TTL"}},
		Recommended: state.Strings{"10 per minute per IP", "429 with Retry-After, and no detail in the body", "10,000 entries, 60 s TTL, LRU"},
		CreatedAt:   state.Time{Time: at.Add(-4 * time.Minute)},
	}
	f := factory.Factory{Repo: "/r/cowork", Slug: "eng-7452-auth-limits", State: &state.State{Ticket: "ENG-7452", Stage: "requirements", WaitingOn: "user", Attention: []state.Attention{batch}}}
	hook := &state.HookFile{Main: &state.HookEvent{Event: hookEvent, At: at.Add(-time.Minute)}, Last: state.HookEvent{Event: hookEvent, At: at.Add(-time.Minute)}}
	return Snapshot{At: at, Items: []Item{{Factory: f, Status: factory.NeedsYou, Hook: hook, Pane: &tmux.Pane{ID: "%9", Command: "claude"}}}}
}

func panelKeys(tm *teatest.TestModel, keys ...string) {
	for _, k := range keys {
		text := ""
		if len([]rune(k)) == 1 {
			text = k
		}
		tm.Send(eventMsg(Event{Message: Message{T: "key", Pane: "panel", K: k, Text: text}}))
	}
}

func typeText(tm *teatest.TestModel, s string) {
	for _, r := range s {
		k := string(r)
		if r == ' ' {
			k = "space"
		}
		tm.Send(eventMsg(Event{Message: Message{T: "key", Pane: "panel", K: k, Text: string(r)}}))
	}
}

func TestQuestionBatchPanel(t *testing.T) {
	local := time.Local
	time.Local = time.UTC
	t.Cleanup(func() { time.Local = local })
	now := time.Date(2026, 10, 22, 14, 42, 0, 0, time.UTC)

	for _, c := range []struct {
		name string
		hook string
		send bool
	}{
		{"answering while claude works", "PreToolUse", false},
		{"refused while busy", "UserPromptSubmit", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			typist := &fakeTypist{}
			model := NewModel(Model{Config: &state.Config{Repos: []string{"/r/cowork"}, StateDir: t.TempDir()}, Typist: typist, Now: func() time.Time { return now }, Icons: theme.Nerd})
			model.SetSnapshot(questionFactory(c.hook, now))
			tm := teatest.NewTestModel(t, model, teatest.WithInitialTermSize(34, 44), teatest.WithProgramOptions(tea.WithColorProfile(colorprofile.TrueColor)))
			panelKeys(tm, "right", "down", "e")
			typeText(tm, "429 with Retry-After.")
			panelKeys(tm, "esc")
			if c.send {
				panelKeys(tm, "ctrl+s")
			}
			if err := tm.Quit(); err != nil {
				t.Fatal(err)
			}
			final := tm.FinalModel(t, teatest.WithFinalTimeout(5*time.Second)).(*Model)
			if c.send && len(typist.typed) != 0 {
				t.Errorf("a busy session received %q", typist.typed)
			}
			golden.RequireEqual(t, final.PanelView(109, 28))
		})
	}
}
