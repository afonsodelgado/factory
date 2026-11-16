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
	"github.com/afonsodelgado/factory/internal/host"
	"github.com/afonsodelgado/factory/internal/state"
	"github.com/afonsodelgado/factory/internal/tmux"
	"github.com/afonsodelgado/factory/internal/ui/footer"
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
	pane := tmux.Pane{ID: "%9", Command: "claude"}
	return Snapshot{At: at, Panes: []tmux.Pane{pane}, Items: []Item{{Factory: f, Status: factory.NeedsYou, Hook: hook, Pane: &pane}}}
}

type sender interface{ Send(tea.Msg) }

type direct struct{ m *Model }

func (d direct) Send(msg tea.Msg) { d.m.Update(msg) }

func drive(m *Model, width, height int) direct {
	m.Update(tea.WindowSizeMsg{Width: width, Height: height})
	return direct{m}
}

func panelKeys(tm sender, keys ...string) {
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

func typeText(tm sender, s string) {
	panelKeys(tm, keystrokes(s)...)
}

func TestQuestionWithoutAQuestionListIsAnsweredAsText(t *testing.T) {
	now := time.Date(2026, 10, 22, 14, 42, 0, 0, time.UTC)
	snap := questionFactory("Stop", now)
	snap.Items[0].Factory.State.Attention[0].Questions = nil
	typist := &fakeTypist{}
	model, node := withNode(Model{Config: &state.Config{Repos: []string{"/r/cowork"}, StateDir: t.TempDir()}, Now: func() time.Time { return now }, Icons: theme.Nerd}, host.Runtime{Typist: typist})
	node.show(model, snap)
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

func TestQQuitsFromThePanelUnlessItIsTypedIntoAnAnswer(t *testing.T) {
	now := time.Date(2026, 10, 22, 14, 42, 0, 0, time.UTC)
	idle, node := withNode(Model{Config: &state.Config{StateDir: t.TempDir()}, Now: func() time.Time { return now }, Icons: theme.Nerd}, host.Runtime{})
	tm := teatest.NewTestModel(t, idle, teatest.WithInitialTermSize(34, 44))
	panelKeys(tm, "q")
	tm.WaitFinished(t, teatest.WithFinalTimeout(5*time.Second))

	snap := questionFactory("Stop", now)
	snap.Items[0].Factory.State.Attention[0].Questions = nil
	typist := &fakeTypist{}
	answering, node := withNode(Model{Config: &state.Config{Repos: []string{"/r/cowork"}, StateDir: t.TempDir()}, Now: func() time.Time { return now }, Icons: theme.Nerd}, host.Runtime{Typist: typist})
	node.show(answering, snap)
	tm = teatest.NewTestModel(t, answering, teatest.WithInitialTermSize(34, 44))
	panelKeys(tm, "right", "left")
	typeText(tm, "q")
	panelKeys(tm, "ctrl+s")
	if err := tm.Quit(); err != nil {
		t.Fatal(err)
	}
	tm.FinalModel(t, teatest.WithFinalTimeout(5*time.Second))
	if len(typist.typed) != 1 || typist.typed[0] != "%9: q-7452-1 q" {
		t.Errorf("typed %q, want q typed into the answer, not taken as quit", typist.typed)
	}
}

var escapes = regexp.MustCompile("\x1b\\[[0-9;:]*m")

func TestAnswerIsSentOnceAndThePanelSaysWhatHappened(t *testing.T) {
	now := time.Date(2026, 10, 22, 14, 42, 0, 0, time.UTC)
	gate := state.Attention{ID: "gate-7452-plan", Kind: "gate", Title: "plan approval", CreatedAt: state.Time{Time: now.Add(-time.Minute)}}
	designGate := state.Attention{ID: "gate-7452-design", Kind: "gate", Title: "design approval", CreatedAt: state.Time{Time: now.Add(-30 * time.Second)}}
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
	changesReceived := &state.HookEvent{Event: "UserPromptSubmit", PromptHead: "gate-7452-plan changes: darker header", At: now.Add(time.Second)}
	idleAgain := &state.HookEvent{Event: "Stop", At: now.Add(time.Second)}

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
		{"gate approve while busy waits out of the way", &gate, "UserPromptSubmit", []string{"ctrl+a"}, nil, 0, "Queued gate-7452-plan", "Approve", nil, ""},
		{"a queued approval is sent once the supervisor is idle", &gate, "UserPromptSubmit", []string{"ctrl+a"}, []later{{2 * time.Second, nil, nil, idleAgain}}, 1, "Sending gate-7452-plan", "", nil, "/factory approve plan gate-7452-plan"},
		{"gate approve pressed twice sends once", &gate, "Stop", []string{"ctrl+a", "ctrl+a"}, nil, 1, "Sending", "", nil, ""},
		{"a second press does not approve the gate that moved under the cursor", &gate, "Stop", []string{"ctrl+a", "ctrl+a"}, nil, 1, "Sending gate-7452-plan", "Queued gate-7452-design", &designGate, "/factory approve plan"},
		{"an item settled elsewhere is closed with one key", &gate, "Stop", []string{"ctrl+k"}, nil, 1, "Sending gate-7452-plan", "", nil, "gate-7452-plan handled: settled outside the panel"},
		{"prose typed at an unsent gate goes into its notes", &gate, "Stop", keystrokes("change the colour"), nil, 0, "change the colour", "", nil, ""},
		{"question batch sent twice sends once", nil, "Stop", []string{"ctrl+s", "ctrl+s"}, nil, 1, "Sending", "", nil, ""},
		{"a sent answer leaves the panel at once", nil, "Stop", []string{"ctrl+s", "x", "x", "x", "ctrl+s"}, nil, 1, "Sending q-7452-1", "How many failed auth", nil, ""},
		{"enter picks each answer and moves on until it sends", nil, "Stop", append(append([]string{"down", "enter", "enter"}, keystrokes("5,000 entries")...), "enter", "enter"), nil, 1, "Sending", "", nil, "1) 30 per minute per IP\n2) recommended\n3) 5,000 entries"},
		{"a question with no recommendation needs an answer", &unrecommended, "Stop", []string{"ctrl+s"}, nil, 0, "Question 1 has no recommended answer", "", nil, ""},
		{"a received answer leaves the panel before the supervisor resolves it", nil, "Stop", []string{"ctrl+s"}, []later{{2 * time.Second, received, nil, nil}, {4 * time.Second, received, nil, nil}}, 1, "Answered q-7452-1", "How many failed auth", nil, ""},
		{"a received answer the supervisor left open comes back to send again", nil, "Stop", []string{"ctrl+s"}, []later{{2 * time.Second, received, nil, nil}, {4 * time.Second, received, []string{"ctrl+s"}, turnEnded}}, 2, "Sending", "", nil, ""},
		{"an answer a stage agent took is not received", nil, "Stop", []string{"ctrl+s"}, []later{{2 * time.Second, misrouted, nil, nil}}, 1, "Not received by the supervisor", "Answered", nil, ""},
		{"an answer never received can be sent again", nil, "Stop", []string{"ctrl+s"}, []later{{11 * time.Second, nil, []string{"ctrl+s"}, nil}}, 2, "Sending", "", nil, ""},
		{"a received change request clears the gate's notes", &gate, "Stop", append(keystrokes("darker header"), "ctrl+s"), []later{{2 * time.Second, changesReceived, nil, nil}, {4 * time.Second, changesReceived, nil, turnEnded}}, 1, "✓ Approve", "darker header", nil, "gate-7452-plan changes: darker header"},
		{"enter on the Approve row approves with the notes", &gate, "Stop", append(keystrokes("ship it"), "esc", "down", "down", "enter"), nil, 1, "Sending", "", nil, "/factory approve plan gate-7452-plan ship it"},
		{"a bracket typed at a gate goes into its notes", &gate, "Stop", []string{"["}, nil, 0, "[█", "", nil, ""},
		{"ctrl+n moves to the next item from inside a form", nil, "Stop", []string{"ctrl+n"}, nil, 0, "gate-7452-plan", "q-7452-1", &gate, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			snapshot := func() Snapshot {
				snap := questionFactory(c.hook, now)
				st := snap.Items[0].Factory.State
				if c.item != nil {
					st.Attention[0] = *c.item
				}
				if c.also != nil {
					st.Attention = append(st.Attention, *c.also)
				}
				st.Stages = map[string]state.Stage{"plan": {Artifacts: []state.Artifact{{URL: "https://claude.ai/artifact/p1", Title: "Plan"}, {URL: "https://claude.ai/artifact/s1", Title: "Schematic"}}}}
				return snap
			}
			typist := &fakeTypist{}
			model, node := withNode(Model{Config: &state.Config{Repos: []string{"/r/cowork"}, StateDir: t.TempDir()}, Now: func() time.Time { return now }, Icons: theme.Nerd}, host.Runtime{Typist: typist})
			node.show(model, snapshot())
			tm := drive(model, 34, 44)
			panelKeys(tm, c.keys...)
			for _, l := range c.later {
				next := snapshot()
				next.At = now.Add(l.after)
				next.Items[0].Hook.Submit = l.submit
				if l.main != nil {
					next.Items[0].Hook.Main = l.main
				}
				tm.Send(node.present(next))
				panelKeys(tm, l.keys...)
			}
			final := model
			view := escapes.ReplaceAllString(final.PanelView(109, 28), "")
			hidden := c.hides == "" || !strings.Contains(view, c.hides)
			sent := strings.Contains(strings.Join(typist.typed, "\n"), c.sends)
			if len(typist.typed) != c.typed || !strings.Contains(view, c.shows) || !hidden || !sent {
				t.Errorf("typed %q, panel:\n%s\nwant %d message(s) with %q, %q shown and %q not shown", typist.typed, view, c.typed, c.sends, c.shows, c.hides)
			}
		})
	}
}

func TestAnsweringTakesTheFactoryOffNeedsYouUntilTheAnswerIsLost(t *testing.T) {
	now := time.Date(2026, 10, 22, 14, 42, 0, 0, time.UTC)
	received := state.HookEvent{Event: "UserPromptSubmit", PromptHead: "q-7452-1 answers:", At: now.Add(time.Second)}
	agentMessage := state.HookEvent{Event: "UserPromptSubmit", PromptHead: `<agent-message from="a1">`, At: now.Add(2 * time.Second)}
	cases := []struct {
		name    string
		after   time.Duration
		submits []state.HookEvent
		status  factory.Status
		shows   string
	}{
		{"received, then an agent message", 3 * time.Second, []state.HookEvent{received, agentMessage}, factory.Running, "Answered q-7452-1"},
		{"never received", 11 * time.Second, nil, factory.NeedsYou, "Not received by the supervisor"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			snapshot := func(at time.Time) Snapshot {
				snap := questionFactory("Stop", now)
				snap.At = at
				snap.Items[0].Unblocked = factory.Running
				return snap
			}
			model, node := withNode(Model{Config: &state.Config{Repos: []string{"/r/cowork"}, StateDir: t.TempDir()}, Now: func() time.Time { return now }, Icons: theme.Nerd}, host.Runtime{Typist: &fakeTypist{}})
			node.show(model, snapshot(now))
			tm := drive(model, 34, 44)
			panelKeys(tm, "ctrl+s")
			later := snapshot(now.Add(c.after))
			later.Items[0].Hook.Submits = c.submits
			if len(c.submits) > 0 {
				later.Items[0].Hook.Submit = &c.submits[len(c.submits)-1]
			}
			tm.Send(node.present(later))
			final := model
			view := escapes.ReplaceAllString(final.PanelView(109, 28), "")
			if got := final.snap.Items[0].Status; got != c.status || !strings.Contains(view, c.shows) {
				t.Errorf("status %s, panel:\n%s\nwant %s and %q: the factory list must follow the panel, and a lost answer must ask again", got, view, c.status, c.shows)
			}
		})
	}
}

func TestAfterTheLastAnswerEnterGoesToTheNextFactoryThatNeedsYou(t *testing.T) {
	now := time.Date(2026, 10, 22, 14, 42, 0, 0, time.UTC)
	snap := questionFactory("Stop", now)
	other := snap.Items[0]
	other.Factory.Slug = "eng-7460-exports"
	other.Factory.State = &state.State{ID: "ENG-7460", Stage: "plan", WaitingOn: "user", Attention: []state.Attention{{ID: "gate-7460-plan", Kind: "gate", Title: "plan approval", CreatedAt: state.Time{Time: now.Add(-time.Minute)}}}}
	snap.Items = append(snap.Items, other)
	model, node := withNode(Model{Config: &state.Config{Repos: []string{"/r/cowork"}, StateDir: t.TempDir()}, Now: func() time.Time { return now }, Icons: theme.Nerd}, host.Runtime{Typist: &fakeTypist{}})
	node.show(model, snap)
	tm := teatest.NewTestModel(t, model, teatest.WithInitialTermSize(34, 44))
	panelKeys(tm, "ctrl+s", "enter")
	if err := tm.Quit(); err != nil {
		t.Fatal(err)
	}
	final := tm.FinalModel(t, teatest.WithFinalTimeout(5*time.Second)).(*Model)
	if final.selected != other.Factory.Key() || final.focus != footer.Panel {
		t.Errorf("selected %q with focus %s, want %q with the panel focused: answering one factory after another must not dead-end in an empty panel", final.selected, final.focus, other.Factory.Key())
	}
}

func TestASmallPanelKeepsTheItemAndItsApproveRow(t *testing.T) {
	now := time.Date(2026, 10, 22, 14, 42, 0, 0, time.UTC)
	snap := questionFactory("Stop", now)
	snap.Items[0].Factory.State.Attention[0] = state.Attention{ID: "gate-7452-plan", Kind: "gate", Title: "plan approval", Body: "The architect wrote plan.html.", CreatedAt: state.Time{Time: now.Add(-time.Minute)}}
	snap.Items[0].Factory.State.Stages = map[string]state.Stage{"plan": {Artifacts: []state.Artifact{{URL: "plan.html", Title: "Implementation plan"}}}}
	model, node := withNode(Model{Config: &state.Config{Repos: []string{"/r/cowork"}}, Now: func() time.Time { return now }, Icons: theme.Nerd}, host.Runtime{})
	node.show(model, snap)
	view := escapes.ReplaceAllString(model.PanelView(84, 10), "")
	if !strings.Contains(view, "gate-7452-plan") || !strings.Contains(view, "Approve") {
		t.Errorf("panel at laptop size:\n%s\nwant the item and its Approve row, not only the notes box and its status", view)
	}
}

func TestAReRaisedItemStartsUnsent(t *testing.T) {
	now := time.Date(2026, 10, 22, 14, 42, 0, 0, time.UTC)
	withCap := func(created time.Time, at time.Time) Snapshot {
		snap := questionFactory("Stop", at)
		snap.Items[0].Factory.State.Attention = []state.Attention{{ID: "cap-7452-qa_fixes", Kind: "cap", Title: "QA rounds 3 of 3", CreatedAt: state.Time{Time: created}}}
		return snap
	}
	firstReceived := &state.HookEvent{Event: "UserPromptSubmit", PromptHead: "cap-7452-qa_fixes continue: raise qa_rounds by one for this factory", At: now.Add(time.Second / 2)}
	resolved := questionFactory("Stop", now.Add(time.Second))
	resolved.Items[0].Factory.State.Attention = []state.Attention{}
	resolved.Items[0].Hook.Submit = firstReceived
	reRaised := withCap(now.Add(2*time.Second), now.Add(2*time.Second))
	reRaised.Items[0].Hook.Submit = firstReceived
	typist := &fakeTypist{}
	model, node := withNode(Model{Config: &state.Config{Repos: []string{"/r/cowork"}, StateDir: t.TempDir()}, Now: func() time.Time { return now }, Icons: theme.Nerd}, host.Runtime{Typist: typist})
	node.show(model, withCap(now.Add(-time.Hour), now))
	tm := drive(model, 34, 44)
	panelKeys(tm, "ctrl+r")
	tm.Send(node.present(resolved))
	tm.Send(node.present(reRaised))
	panelKeys(tm, "ctrl+r")
	if len(typist.typed) != 2 {
		t.Errorf("typed %q; the cap tripped again under the same id, so continue must send a second time", typist.typed)
	}
}

func TestPanelOpensOnAttentionOnlyWhenSomethingIsOpen(t *testing.T) {
	now := time.Date(2026, 10, 22, 14, 42, 0, 0, time.UTC)
	asking := questionFactory("Stop", now).Items[0]
	quiet := Item{Status: factory.WaitingAgent, Factory: factory.Factory{Repo: "/r/cowork", Slug: "eng-7449-redact", State: &state.State{ID: "ENG-7449", Stage: "implement", Attention: []state.Attention{}, PRs: []state.PR{{Number: 1076}, {Number: 1077}}}}}
	model, node := withNode(Model{Config: &state.Config{Repos: []string{"/r/cowork"}}, Now: func() time.Time { return now }, Icons: theme.Nerd}, host.Runtime{})
	node.show(model, Snapshot{At: now, Items: []Item{quiet, asking}})

	opened := []panel.Tab{model.panel.tab}
	model.choose(asking.Factory.Key())
	opened = append(opened, model.panel.tab)
	model.choose(quiet.Factory.Key())
	opened = append(opened, model.panel.tab)

	nowAsking := quiet
	nowAsking.Factory.State = &state.State{ID: "ENG-7449", Stage: "implement", Attention: []state.Attention{{ID: "q-7449-1", Kind: "question", Title: "1 question", CreatedAt: state.Time{Time: now}}}, PRs: quiet.Factory.State.PRs}
	node.show(model, Snapshot{At: now.Add(time.Second), Items: []Item{nowAsking, asking}})
	opened = append(opened, model.panel.tab)
	model.switchTab(1)
	node.show(model, Snapshot{At: now.Add(2 * time.Second), Items: []Item{nowAsking, asking}})
	opened = append(opened, model.panel.tab)

	if want := []panel.Tab{panel.Attention, panel.Attention, panel.PRs, panel.Attention, panel.PRs}; !slices.Equal(opened, want) || model.selected != nowAsking.Factory.Key() {
		t.Errorf("tabs on start, choosing the asking and the quiet factory, a question arriving, then the next poll after tab = %v, want %v: the hub starts on the factory that needs you, nothing open opens on PRs (A1), an arriving item opens Attention once (R24)", opened, want)
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
	model, node := withNode(Model{Config: cfg, Now: time.Now, Icons: theme.Nerd}, host.Runtime{})
	node.show(model, loader.Load())

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
		{"queued while busy", "UserPromptSubmit", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			typist := &fakeTypist{}
			model, node := withNode(Model{Config: &state.Config{Repos: []string{"/r/cowork"}, StateDir: t.TempDir()}, Now: func() time.Time { return now }, Icons: theme.Nerd}, host.Runtime{Typist: typist})
			node.show(model, questionFactory(c.hook, now))
			tm := teatest.NewTestModel(t, model, teatest.WithInitialTermSize(34, 44), teatest.WithProgramOptions(tea.WithColorProfile(colorprofile.TrueColor)))
			panelKeys(tm, "down", "enter", "down")
			typeText(tm, "429 with Retry-After.")
			panelKeys(tm, "enter")
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
