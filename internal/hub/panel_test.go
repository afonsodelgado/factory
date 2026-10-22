package hub

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/exp/golden"
	"github.com/charmbracelet/x/exp/teatest/v2"

	"github.com/afonsodelgado/factory/internal/factory"
	"github.com/afonsodelgado/factory/internal/state"
	"github.com/afonsodelgado/factory/internal/tmux"
	"github.com/afonsodelgado/factory/internal/ui/panel"
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

func TestQuestionWithoutAQuestionListIsAnsweredAsText(t *testing.T) {
	now := time.Date(2026, 10, 22, 14, 42, 0, 0, time.UTC)
	snap := questionFactory("Stop", now)
	snap.Items[0].Factory.State.Attention[0].Questions = nil
	typist := &fakeTypist{}
	model := NewModel(Model{Config: &state.Config{Repos: []string{"/r/cowork"}, StateDir: t.TempDir()}, Typist: typist, Now: func() time.Time { return now }, Icons: theme.Nerd})
	model.SetSnapshot(snap)
	tm := teatest.NewTestModel(t, model, teatest.WithInitialTermSize(34, 44))
	panelKeys(tm, "right", "left", "e")
	typeText(tm, "yes")
	panelKeys(tm, "ctrl+s")
	if err := tm.Quit(); err != nil {
		t.Fatal(err)
	}
	tm.FinalModel(t, teatest.WithFinalTimeout(5*time.Second))
	if len(typist.typed) != 1 || typist.typed[0] != "%9: q-7452-1 yes" {
		t.Errorf("typed %q, want the answer as text for the item: a question item without its questions list must not crash the hub", typist.typed)
	}
}

var escapes = regexp.MustCompile("\x1b\\[[0-9;:]*m")

func TestAnswerIsSentOnceAndThePanelSaysWhatHappened(t *testing.T) {
	now := time.Date(2026, 10, 22, 14, 42, 0, 0, time.UTC)
	gate := state.Attention{ID: "gate-7452-plan", Kind: "gate", Title: "plan approval", CreatedAt: state.Time{Time: now.Add(-time.Minute)}}
	unrecommended := state.Attention{ID: "q-7452-2", Kind: "question", Title: "1 question", Questions: []string{"Which region?"}, CreatedAt: state.Time{Time: now.Add(-time.Minute)}}

	cases := []struct {
		name  string
		item  *state.Attention
		hook  string
		keys  []string
		typed int
		shows string
	}{
		{"gate approve while busy is refused out loud", &gate, "UserPromptSubmit", []string{"a"}, 0, "Not sent."},
		{"gate approve pressed twice sends once", &gate, "Stop", []string{"a", "a"}, 1, "Sent at"},
		{"question batch sent twice sends once", nil, "Stop", []string{"ctrl+s", "ctrl+s"}, 1, "Sent at"},
		{"a question with no recommendation needs an answer", &unrecommended, "Stop", []string{"ctrl+s"}, 0, "Question 1 has no recommended answer"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			snap := questionFactory(c.hook, now)
			if c.item != nil {
				snap.Items[0].Factory.State.Attention[0] = *c.item
			}
			typist := &fakeTypist{}
			model := NewModel(Model{Config: &state.Config{Repos: []string{"/r/cowork"}, StateDir: t.TempDir()}, Typist: typist, Now: func() time.Time { return now }, Icons: theme.Nerd})
			model.SetSnapshot(snap)
			tm := teatest.NewTestModel(t, model, teatest.WithInitialTermSize(34, 44))
			panelKeys(tm, c.keys...)
			if err := tm.Quit(); err != nil {
				t.Fatal(err)
			}
			final := tm.FinalModel(t, teatest.WithFinalTimeout(5*time.Second)).(*Model)
			view := escapes.ReplaceAllString(final.PanelView(109, 28), "")
			if len(typist.typed) != c.typed || !strings.Contains(view, c.shows) {
				t.Errorf("typed %q, panel:\n%s\nwant %d message(s) and %q shown", typist.typed, view, c.typed, c.shows)
			}
		})
	}
}

func TestPanelOpensOnAttentionOnlyWhenSomethingIsOpen(t *testing.T) {
	now := time.Date(2026, 10, 22, 14, 42, 0, 0, time.UTC)
	asking := questionFactory("Stop", now).Items[0]
	quiet := Item{Status: factory.WaitingAgent, Factory: factory.Factory{Repo: "/r/cowork", Slug: "eng-7449-redact", State: &state.State{Ticket: "ENG-7449", Stage: "implement", Attention: []state.Attention{}, PRs: []state.PR{{Number: 1076}, {Number: 1077}}}}}
	model := NewModel(Model{Config: &state.Config{Repos: []string{"/r/cowork"}}, Now: func() time.Time { return now }, Icons: theme.Nerd})
	model.SetSnapshot(Snapshot{At: now, Items: []Item{quiet, asking}})

	opened := []panel.Tab{model.panel.tab}
	model.choose(asking.Factory.Key())
	opened = append(opened, model.panel.tab)
	model.choose(quiet.Factory.Key())
	opened = append(opened, model.panel.tab)
	if want := []panel.Tab{panel.PRs, panel.Attention, panel.PRs}; !slices.Equal(opened, want) {
		t.Errorf("tabs on start, then choosing the asking and the quiet factory = %v, want %v: a factory with PRs and nothing open opens on PRs (A1)", opened, want)
	}
}

func TestUnreadableStateIsSaidNotHidden(t *testing.T) {
	repo := t.TempDir()
	dir := filepath.Join(factory.WorktreesDir(repo), "eng-5-x", ".claude", "factory", "ENG-5")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	broken := `{"ticket":"ENG-5","attention":[{"id":"q-5-1","kind":"question","options":"zz"}]}`
	if err := os.WriteFile(filepath.Join(dir, "state.json"), []byte(broken), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := &state.Config{Repos: []string{repo}, StateDir: t.TempDir()}
	loader := &Loader{Config: cfg, Registry: &state.Registry{}, Now: time.Now}
	model := NewModel(Model{Config: cfg, Now: time.Now, Icons: theme.Nerd})
	model.SetSnapshot(loader.Load())

	view := escapes.ReplaceAllString(model.PanelView(109, 20), "")
	if !strings.Contains(view, "state.json cannot be read") {
		t.Errorf("panel:\n%s\nwant it to say the state cannot be read: its open question is otherwise invisible", view)
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
