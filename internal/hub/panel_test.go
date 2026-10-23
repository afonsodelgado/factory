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
	f := factory.Factory{Repo: "/r/cowork", Slug: "eng-7452-auth-limits", State: &state.State{ID: "ENG-7452", Stage: "requirements", WaitingOn: "user", Attention: []state.Attention{batch}}}
	hook := &state.HookFile{Main: &state.HookEvent{Event: hookEvent, At: at.Add(-time.Minute)}, Last: state.HookEvent{Event: hookEvent, At: at.Add(-time.Minute)}}
	return Snapshot{At: at, Items: []Item{{Factory: f, Status: factory.NeedsYou, Hook: hook, Pane: &tmux.Pane{ID: "%9", Command: "claude"}}}}
}

func panelKeys(tm *teatest.TestModel, keys ...string) {
	for _, k := range keys {
		text := ""
		switch {
		case k == "space":
			text = " "
		case len([]rune(k)) == 1:
			text = k
		}
		tm.Send(eventMsg(Event{Message: Message{T: "key", Pane: "panel", K: k, Text: text}}))
	}
}

func keystrokes(s string) []string {
	var keys []string
	for _, r := range s {
		k := string(r)
		if r == ' ' {
			k = "space"
		}
		keys = append(keys, k)
	}
	return keys
}

func typeText(tm *teatest.TestModel, s string) {
	panelKeys(tm, keystrokes(s)...)
}

func TestQuestionWithoutAQuestionListIsAnsweredAsText(t *testing.T) {
	now := time.Date(2026, 10, 22, 14, 42, 0, 0, time.UTC)
	snap := questionFactory("Stop", now)
	snap.Items[0].Factory.State.Attention[0].Questions = nil
	typist := &fakeTypist{}
	model := NewModel(Model{Config: &state.Config{Repos: []string{"/r/cowork"}, StateDir: t.TempDir()}, Typist: typist, Now: func() time.Time { return now }, Icons: theme.Nerd})
	model.SetSnapshot(snap)
	tm := teatest.NewTestModel(t, model, teatest.WithInitialTermSize(34, 44))
	panelKeys(tm, "right", "left")
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

	type later struct {
		after  time.Duration
		submit *state.HookEvent
		keys   []string
		main   *state.HookEvent
	}
	received := &state.HookEvent{Event: "UserPromptSubmit", PromptHead: "q-7452-1 answers:", At: now.Add(time.Second)}
	turnEnded := &state.HookEvent{Event: "Stop", At: now.Add(3 * time.Second)}
	misrouted := &state.HookEvent{Event: "UserPromptSubmit", AgentID: "a1", PromptHead: "q-7452-1 answers:", At: now.Add(time.Second)}

	cases := []struct {
		name  string
		item  *state.Attention
		hook  string
		keys  []string
		later []later
		typed int
		shows string
		hides string
		also  *state.Attention
		sends string
	}{
		{"gate approve while busy is refused out loud", &gate, "UserPromptSubmit", []string{"ctrl+a"}, nil, 0, "Not sent.", "", nil, ""},
		{"gate approve pressed twice sends once", &gate, "Stop", []string{"ctrl+a", "ctrl+a"}, nil, 1, "Sent at", "", nil, ""},
		{"prose typed at an unsent gate goes into its notes", &gate, "Stop", keystrokes("change the colour"), nil, 0, "change the colour", "", nil, ""},
		{"question batch sent twice sends once", nil, "Stop", []string{"ctrl+s", "ctrl+s"}, nil, 1, "Sent at", "", nil, ""},
		{"a sent answer cannot be edited under its Sent line", nil, "Stop", []string{"ctrl+s", "x", "x", "x", "ctrl+s"}, nil, 1, "Sent at", "xxx", nil, ""},
		{"a question with no recommendation needs an answer", &unrecommended, "Stop", []string{"ctrl+s"}, nil, 0, "Question 1 has no recommended answer", "", nil, ""},
		{"a received answer leaves the panel before the supervisor resolves it", nil, "Stop", []string{"ctrl+s"}, []later{{2 * time.Second, received, nil, nil}, {4 * time.Second, received, nil, nil}}, 1, "Answered q-7452-1", "How many failed auth", nil, ""},
		{"a received answer the supervisor left open comes back to send again", nil, "Stop", []string{"ctrl+s"}, []later{{2 * time.Second, received, nil, nil}, {4 * time.Second, received, []string{"ctrl+s"}, turnEnded}}, 2, "waiting for the supervisor", "", nil, ""},
		{"an answer a stage agent took is not received", nil, "Stop", []string{"ctrl+s"}, []later{{2 * time.Second, misrouted, nil, nil}}, 1, "Not received by the supervisor", "Answered", nil, ""},
		{"an answer never received can be sent again", nil, "Stop", []string{"ctrl+s"}, []later{{11 * time.Second, nil, []string{"ctrl+s"}, nil}}, 2, "waiting for the supervisor", "", nil, ""},
		{"enter on the Approve row approves with the notes", &gate, "Stop", append(keystrokes("ship it"), "esc", "down", "down", "enter"), nil, 1, "Sent at", "", nil, "/factory approve plan gate-7452-plan ship it"},
		{"a bracket typed at a gate goes into its notes", &gate, "Stop", []string{"["}, nil, 0, "[█", "", nil, ""},
		{"ctrl+n moves to the next item from inside a form", nil, "Stop", []string{"ctrl+n"}, nil, 0, "gate-7452-plan", "q-7452-1", &gate, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			snap := questionFactory(c.hook, now)
			st := snap.Items[0].Factory.State
			if c.item != nil {
				st.Attention[0] = *c.item
			}
			if c.also != nil {
				st.Attention = append(st.Attention, *c.also)
			}
			st.Stages = map[string]state.Stage{"plan": {Artifacts: []state.Artifact{{URL: "https://claude.ai/artifact/p1", Title: "Plan"}, {URL: "https://claude.ai/artifact/s1", Title: "Schematic"}}}}
			typist := &fakeTypist{}
			model := NewModel(Model{Config: &state.Config{Repos: []string{"/r/cowork"}, StateDir: t.TempDir()}, Typist: typist, Now: func() time.Time { return now }, Icons: theme.Nerd})
			model.SetSnapshot(snap)
			tm := teatest.NewTestModel(t, model, teatest.WithInitialTermSize(34, 44))
			panelKeys(tm, c.keys...)
			for _, l := range c.later {
				next := questionFactory(c.hook, now)
				next.At = now.Add(l.after)
				next.Items[0].Hook.Submit = l.submit
				if l.main != nil {
					next.Items[0].Hook.Main = l.main
				}
				tm.Send(snapshotMsg(next))
				panelKeys(tm, l.keys...)
			}
			if err := tm.Quit(); err != nil {
				t.Fatal(err)
			}
			final := tm.FinalModel(t, teatest.WithFinalTimeout(5*time.Second)).(*Model)
			view := escapes.ReplaceAllString(final.PanelView(109, 28), "")
			hidden := c.hides == "" || !strings.Contains(view, c.hides)
			sent := strings.Contains(strings.Join(typist.typed, "\n"), c.sends)
			if len(typist.typed) != c.typed || !strings.Contains(view, c.shows) || !hidden || !sent {
				t.Errorf("typed %q, panel:\n%s\nwant %d message(s) with %q, %q shown and %q not shown", typist.typed, view, c.typed, c.sends, c.shows, c.hides)
			}
		})
	}
}

func TestAReRaisedItemStartsUnsent(t *testing.T) {
	now := time.Date(2026, 10, 22, 14, 42, 0, 0, time.UTC)
	withCap := func(created time.Time, at time.Time) Snapshot {
		snap := questionFactory("Stop", at)
		snap.Items[0].Factory.State.Attention = []state.Attention{{ID: "cap-7452-qa_fixes", Kind: "cap", Title: "QA rounds 3 of 3", CreatedAt: state.Time{Time: created}}}
		return snap
	}
	resolved := questionFactory("Stop", now.Add(time.Second))
	resolved.Items[0].Factory.State.Attention = []state.Attention{}
	typist := &fakeTypist{}
	model := NewModel(Model{Config: &state.Config{Repos: []string{"/r/cowork"}, StateDir: t.TempDir()}, Typist: typist, Now: func() time.Time { return now }, Icons: theme.Nerd})
	model.SetSnapshot(withCap(now.Add(-time.Hour), now))
	tm := teatest.NewTestModel(t, model, teatest.WithInitialTermSize(34, 44))
	panelKeys(tm, "ctrl+r")
	tm.Send(snapshotMsg(resolved))
	tm.Send(snapshotMsg(withCap(now.Add(2*time.Second), now.Add(2*time.Second))))
	panelKeys(tm, "ctrl+r")
	if err := tm.Quit(); err != nil {
		t.Fatal(err)
	}
	tm.FinalModel(t, teatest.WithFinalTimeout(5*time.Second))
	if len(typist.typed) != 2 {
		t.Errorf("typed %q; the cap tripped again under the same id, so continue must send a second time", typist.typed)
	}
}

func TestPanelOpensOnAttentionOnlyWhenSomethingIsOpen(t *testing.T) {
	now := time.Date(2026, 10, 22, 14, 42, 0, 0, time.UTC)
	asking := questionFactory("Stop", now).Items[0]
	quiet := Item{Status: factory.WaitingAgent, Factory: factory.Factory{Repo: "/r/cowork", Slug: "eng-7449-redact", State: &state.State{ID: "ENG-7449", Stage: "implement", Attention: []state.Attention{}, PRs: []state.PR{{Number: 1076}, {Number: 1077}}}}}
	model := NewModel(Model{Config: &state.Config{Repos: []string{"/r/cowork"}}, Now: func() time.Time { return now }, Icons: theme.Nerd})
	model.SetSnapshot(Snapshot{At: now, Items: []Item{quiet, asking}})

	opened := []panel.Tab{model.panel.tab}
	model.choose(asking.Factory.Key())
	opened = append(opened, model.panel.tab)
	model.choose(quiet.Factory.Key())
	opened = append(opened, model.panel.tab)

	nowAsking := quiet
	nowAsking.Factory.State = &state.State{ID: "ENG-7449", Stage: "implement", Attention: []state.Attention{{ID: "q-7449-1", Kind: "question", Title: "1 question", CreatedAt: state.Time{Time: now}}}, PRs: quiet.Factory.State.PRs}
	model.SetSnapshot(Snapshot{At: now.Add(time.Second), Items: []Item{nowAsking, asking}})
	opened = append(opened, model.panel.tab)
	model.switchTab(1)
	model.SetSnapshot(Snapshot{At: now.Add(2 * time.Second), Items: []Item{nowAsking, asking}})
	opened = append(opened, model.panel.tab)

	if want := []panel.Tab{panel.PRs, panel.Attention, panel.PRs, panel.Attention, panel.PRs}; !slices.Equal(opened, want) {
		t.Errorf("tabs on start, choosing the asking and the quiet factory, a question arriving, then the next poll after tab = %v, want %v: nothing open opens on PRs (A1), an arriving item opens Attention once (R24)", opened, want)
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
	registry, err := state.LoadRegistry(cfg.RegistryPath())
	if err != nil {
		t.Fatal(err)
	}
	loader := &Loader{Config: cfg, Registry: registry, Now: time.Now}
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
			panelKeys(tm, "right", "down")
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
