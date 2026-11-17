package dialog

import (
	"bytes"
	"context"
	"errors"
	"fmt"
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
	"github.com/afonsodelgado/factory/internal/host"
	"github.com/afonsodelgado/factory/internal/node"
	"github.com/afonsodelgado/factory/internal/state"
)

func TestNewFactorySelectsTheFactoryWithTheTypedID(t *testing.T) {
	context := NewContext{
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
	teatest.WaitFor(t, tm.Output(), func(b []byte) bool { return bytes.Contains(b, []byte("Stages & agents")) }, teatest.WithDuration(5*time.Second))
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

type fakePhoneActions struct {
	status PhoneContext
	checks []CheckRow
}

func (f *fakePhoneActions) Status() (PhoneContext, error) { return f.status, nil }
func (f *fakePhoneActions) Test() (string, error) {
	return "", errors.New("3f2a9c1d: the push service could not be reached: Post \"https://push.invalid/qa-sub\": dial tcp: lookup push.invalid: no such host (end)")
}
func (f *fakePhoneActions) Check() []CheckRow {
	if f.checks != nil {
		return f.checks
	}
	return []CheckRow{{Name: "serve", Mark: "fail", Detail: "https://devbox.tail1234.ts.net/ does not forward to http://127.0.0.1:7418", Fix: "tailscale serve --bg --https=443 http://127.0.0.1:7418"}}
}

func TestPhonePopupShowsTheNodeURLATestPushFailureAndFailingChecksAt80x24(t *testing.T) {
	actions := &fakePhoneActions{status: PhoneContext{Running: true, Hostname: "devbox.tail1234.ts.net", Port: 7418, Subscriptions: 2}}
	p := NewPhone(actions.status, actions)
	p.Update(tea.WindowSizeMsg{Width: 74, Height: 18})
	press := func(key string) {
		_, cmd := p.Update(tea.KeyPressMsg{Code: rune(key[0]), Text: key})
		for cmd != nil {
			_, cmd = p.Update(cmd())
		}
	}
	press("t")
	if screen := p.View().Content; !strings.Contains(screen, "https://devbox.tail1234.ts.net") || !strings.Contains(screen, "(end)") || !strings.Contains(screen, "t test push   h checks   esc close") {
		t.Fatalf("after a failed test push the popup shows:\n%s\nwant the phone URL, the whole error and every key", screen)
	}
	press("h")
	if screen := p.View().Content; !strings.Contains(screen, "✗") || !strings.Contains(screen, "tailscale serve --bg --https=443") {
		t.Fatalf("checks show:\n%s\nwant the failing serve mapping with its fix", screen)
	}
}

func TestNodesPopupLinesThisMachineUpUnderItsHeadingsAndScrollsItsChecksAt80x24(t *testing.T) {
	actions := &fakeNodeActions{fakePhoneActions: fakePhoneActions{status: PhoneContext{Running: true, Hostname: "macbook.tail5e2c.ts.net", Port: 7418, Subscriptions: 1}}}
	n := NewNodes(NodesContext{Machine: "macbook", Tailnet: "tail5e2c.ts.net", Version: "0.9.4", Factories: 2, NeedsYou: 1, Phone: actions.status}, actions)
	n.Update(tea.WindowSizeMsg{Width: 88, Height: 28})
	golden.RequireEqual(t, n.View().Content)

	for i := range 10 {
		actions.checks = append(actions.checks, CheckRow{Name: fmt.Sprintf("check %d", i), Mark: "fail", Detail: "failing", Fix: fmt.Sprintf("fix number %d", i)})
	}
	n.Update(tea.WindowSizeMsg{Width: 74, Height: 18})
	_, check := n.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	n.Update(check())
	for range 30 {
		n.Update(tea.KeyPressMsg{Code: 'j', Text: "j"})
	}
	if details := n.View().Content; !strings.Contains(details, "fix number 9") || !strings.Contains(details, "Nothing was changed") || !strings.Contains(details, "j/k scroll   r check again   esc back") {
		t.Fatalf("⏎ on this machine in an 80×24 terminal, scrolled down, shows:\n%s\nwant the last fix, the summary and every key", details)
	}
}

type fakeNodeActions struct {
	fakePhoneActions
	answers    map[string]NodeAnswer
	candidates []node.Candidate
	report     node.CheckReport
	result     node.AddResult
	added      []string
	switched   []bool
	removed    []string
}

func (f *fakeNodeActions) Ask(_ context.Context, name string, _ []string) (NodeAnswer, error) {
	answer, ok := f.answers[name]
	if !ok {
		return NodeAnswer{}, errors.New("dial " + name + ": no route to host")
	}
	return answer, nil
}

func (f *fakeNodeActions) Seen(context.Context, string) (node.Peer, error) {
	return node.Peer{Online: false, LastSeen: nodesNow.Add(-13 * time.Minute)}, nil
}

func (f *fakeNodeActions) Machines(context.Context) (node.Inventory, error) {
	return node.Inventory{}, nil
}

func (f *fakeNodeActions) Candidates(context.Context) ([]node.Candidate, error) {
	return f.candidates, nil
}

func (f *fakeNodeActions) Inspect(context.Context, string) (node.CheckReport, error) {
	return f.report, nil
}

func (f *fakeNodeActions) Add(_ context.Context, _, name string, replaceKey bool) (node.AddResult, error) {
	f.added, f.switched = append(f.added, name), append(f.switched, replaceKey)
	return f.result, nil
}

func (f *fakeNodeActions) Remove(_, id string) error {
	f.removed = append(f.removed, id)
	return nil
}

func (f *fakeNodeActions) SetTarget(string) error                              { return nil }
func (f *fakeNodeActions) Clone(context.Context, string, string, string) error { return nil }
func (f *fakeNodeActions) MoveAll(context.Context, string, string) (node.BatchProgress, error) {
	return node.BatchProgress{}, nil
}
func (f *fakeNodeActions) Batch(context.Context, string) (node.BatchProgress, error) {
	return node.BatchProgress{}, nil
}
func (f *fakeNodeActions) CancelAll(context.Context, string) (node.BatchProgress, error) {
	return node.BatchProgress{}, nil
}

type fakeMoveActions struct {
	reports   map[string]node.Report
	progress  node.Progress
	warning   node.Unconfirmed
	started   []node.MoveRequest
	cancelled []string
	takeovers []node.TakeoverRequest
	moves     []node.Progress
}

func (f *fakeMoveActions) Moves(context.Context, string) ([]node.Progress, error) {
	return f.moves, nil
}

func (f *fakeMoveActions) Ready(_ context.Context, on string) (node.Report, error) {
	report, ok := f.reports[on]
	if !ok {
		return report, errors.New("dial " + on + ": no route to host")
	}
	return report, nil
}

func (f *fakeMoveActions) Start(_ context.Context, _ string, r node.MoveRequest) (node.Progress, error) {
	f.started = append(f.started, r)
	return f.progress, nil
}

func (f *fakeMoveActions) Progress(context.Context, string, string) (node.Progress, error) {
	return f.progress, nil
}

func (f *fakeMoveActions) Cancel(_ context.Context, _, op string) (node.Progress, error) {
	f.cancelled = append(f.cancelled, op)
	return f.progress, nil
}

func (f *fakeMoveActions) Takeover(_ context.Context, _ string, r node.TakeoverRequest) (node.Progress, error) {
	f.takeovers = append(f.takeovers, r)
	if !r.Confirm {
		return node.Progress{}, f.warning
	}
	return f.progress, nil
}

func runMove(m *Move, cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	switch msg := cmd().(type) {
	case tea.BatchMsg:
		for _, c := range msg {
			runMove(m, c)
		}
	case nil:
	default:
		_, next := m.Update(msg)
		if _, polled := msg.(moveProgressed); !polled {
			runMove(m, next)
		}
	}
}

func pressMove(m *Move, keys ...string) {
	for _, k := range keys {
		msg := tea.KeyPressMsg{Code: rune(k[0]), Text: k}
		if k == "enter" {
			msg = tea.KeyPressMsg{Code: tea.KeyEnter}
		}
		_, cmd := m.Update(msg)
		runMove(m, cmd)
	}
}

func TestMovingAFactoryNeverFallsBackCancelsOnlyBeforeReleaseAndReopensOnTheRunningMove(t *testing.T) {
	actions := &fakeMoveActions{reports: map[string]node.Report{
		"homelab.tail5e2c.ts.net": {At: nodesNow},
		"studio.tail5e2c.ts.net":  {At: nodesNow, Problems: 1, Checks: []node.Check{{Name: "claude", Mark: node.MarkFail, Detail: "not logged in"}}},
	}}
	csv := MoveContext{Key: "/src/cowork:csv", Label: "csv", Factory: "/src/cowork:csv", HolderShort: "macbook", Targets: []NodeChoice{
		{Name: "build-box.tail5e2c.ts.net", Unreachable: true},
		{Name: "homelab.tail5e2c.ts.net"},
		{Name: "studio.tail5e2c.ts.net"},
	}}
	m := NewMove(csv, actions)
	m.Update(tea.WindowSizeMsg{Width: 88, Height: 22})
	runMove(m, m.Init())
	pressMove(m, "enter", "j", "j", "enter")
	if len(actions.started) > 0 || !strings.Contains(m.View().Content, "studio can't run factories yet: claude: not logged in") {
		t.Fatalf("⏎ on an unreachable and an unready node started %v and shows:\n%s\nwant nothing started and the reason named", actions.started, m.View().Content)
	}

	actions.progress = node.Progress{Transfer: node.Transfer{Operation: "op-csv", Role: node.RoleSource, Factory: "csv", Key: "/src/cowork:csv", SourceName: "macbook", TargetName: "homelab", Phase: node.PhaseCheckpoint, Waiting: "csv is implementing; it stops for the move when it next waits for you or between stages."}, State: node.StateCheckpointWaiting, Running: true}
	pressMove(m, "k", "enter")
	if len(actions.started) != 1 || actions.started[0].Target != "homelab.tail5e2c.ts.net" {
		t.Fatalf("⏎ on homelab started %+v; want one move to homelab", actions.started)
	}
	golden.RequireEqual(t, m.View().Content)

	actions.progress.Phase, actions.progress.State, actions.progress.Waiting = node.PhaseReleased, node.StateReleased, ""
	_, poll := m.Update(moveTick{})
	runMove(m, poll)
	pressMove(m, "x")
	if released := m.View().Content; len(actions.cancelled) > 0 || strings.Contains(released, "x cancel") || !strings.Contains(released, "csv is stopped on macbook; homelab is starting it") {
		t.Errorf("a released move shows:\n%s\nand x cancelled %v; want released-not-active named and no cancel", released, actions.cancelled)
	}

	actions.moves = []node.Progress{actions.progress}
	again := NewMove(csv, actions)
	again.Update(tea.WindowSizeMsg{Width: 88, Height: 22})
	runMove(again, again.Init())
	pressMove(again, "enter")
	if reopened := again.View().Content; len(actions.started) != 1 || !strings.Contains(reopened, "csv is stopped on macbook; homelab is starting it") {
		t.Errorf("m again during the move started %+v and shows:\n%s\nwant the running move, not a second one", actions.started, reopened)
	}
}

func TestATakeoverShowsItsWarningAndStartsOnlyOnY(t *testing.T) {
	actions := &fakeMoveActions{
		reports: map[string]node.Report{"": {At: nodesNow}},
		warning: node.Unconfirmed{Warning: "Forcing a takeover starts csv on macbook from the checkpoint homelab took at 11:40 on 16 Nov. The node that holds csv now may only be out of reach: it can keep running csv until it reconnects and learns it lost it, and work done after that checkpoint may be lost or diverge."},
	}
	m := NewMove(MoveContext{Key: "node:n-homelab/inc-csv", Label: "csv", Factory: "/srv/cowork:csv", Incarnation: "inc-csv", Holder: "homelab.tail5e2c.ts.net", HolderShort: "homelab", Lost: true, Targets: []NodeChoice{
		{Name: "macbook.tail5e2c.ts.net", Here: true},
	}}, actions)
	m.Update(tea.WindowSizeMsg{Width: 88, Height: 22})
	runMove(m, m.Init())
	pressMove(m, "enter")
	golden.RequireEqual(t, m.View().Content)

	pressMove(m, "enter", "enter")
	if confirmed := slices.ContainsFunc(actions.takeovers, func(r node.TakeoverRequest) bool { return r.Confirm }); confirmed || len(actions.started) > 0 {
		t.Fatalf("takeovers %+v before y; want only unconfirmed requests for the warning", actions.takeovers)
	}
	actions.progress = node.Progress{Transfer: node.Transfer{Operation: "op-take", Role: node.RoleTarget, Forced: true, Factory: "csv", SourceName: "homelab", TargetName: "macbook", Phase: node.PhaseStaged}, State: node.StateMoving, Running: true}
	pressMove(m, "y")
	last, warned := actions.takeovers[len(actions.takeovers)-1], actions.takeovers[len(actions.takeovers)-2]
	if !last.Confirm || last.Operation != warned.Operation || last.Factory != "inc-csv" {
		t.Errorf("y sent %+v after the warning for %+v; want the same operation confirmed for the incarnation", last, warned)
	}
}

var nodesNow = time.Date(2026, 11, 16, 12, 10, 0, 0, time.Local)

func threeNodes(t *testing.T) (*Nodes, *fakeNodeActions) {
	t.Helper()
	actions := &fakeNodeActions{
		fakePhoneActions: fakePhoneActions{status: PhoneContext{Running: true, Hostname: "macbook.tail5e2c.ts.net", Port: 7418, Subscriptions: 1}},
		answers: map[string]NodeAnswer{"homelab.tail5e2c.ts.net": {
			Snapshot: node.Snapshot{Version: "0.9.4", Factories: []host.LocalFactory{{ID: "rate-limits"}, {ID: "doctor-json", NeedsSince: nodesNow}}},
			Report:   node.Report{At: nodesNow},
		}},
	}
	n := NewNodes(NodesContext{
		Machine: "macbook", Tailnet: "tail5e2c.ts.net", Version: "0.9.4", Factories: 2, NeedsYou: 1, Phone: actions.status, Home: "https://macbook.tail5e2c.ts.net",
		Others: []NodeRow{
			{ID: "n-homelab", Name: "homelab.tail5e2c.ts.net", OS: "linux"},
			{ID: "n-build-box", Name: "build-box.tail5e2c.ts.net", OS: "linux", Version: "0.9.3", Factories: 2, Contact: nodesNow.Add(-12 * time.Minute)},
		},
	}, actions)
	n.Now = func() time.Time { return nodesNow }
	n.Update(tea.WindowSizeMsg{Width: 88, Height: 28})
	runNodes(n, n.Init())
	return n, actions
}

func runNodes(n *Nodes, cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	switch msg := cmd().(type) {
	case tea.BatchMsg:
		for _, c := range msg {
			runNodes(n, c)
		}
	case nil:
	default:
		_, next := n.Update(msg)
		runNodes(n, next)
	}
}

func pressNodes(n *Nodes, keys ...string) {
	for _, k := range keys {
		msg := tea.KeyPressMsg{Code: rune(k[0]), Text: k}
		if special, ok := map[string]rune{"enter": tea.KeyEnter, "esc": tea.KeyEscape}[k]; ok {
			msg = tea.KeyPressMsg{Code: special}
		}
		_, cmd := n.Update(msg)
		runNodes(n, cmd)
	}
}

func TestNodesPopupListsEveryNodeAndAnUnreachableOneAt90x30(t *testing.T) {
	n, _ := threeNodes(t)
	golden.RequireEqual(t, n.View().Content)
}

func TestNodesPopupRemovesAnotherNodeOnlyAfterYesAndNeverThisMachine(t *testing.T) {
	n, actions := threeNodes(t)
	pressNodes(n, "x")
	if screen := n.View().Content; !strings.Contains(screen, "factory node uninstall") || len(actions.removed) > 0 {
		t.Fatalf("x on this machine shows:\n%s\nremoved %v; want a refusal that points to factory node uninstall and nothing removed", screen, actions.removed)
	}
	pressNodes(n, "j", "j", "x")
	golden.RequireEqual(t, n.View().Content)
	pressNodes(n, "y")
	if !slices.Equal(actions.removed, []string{"n-build-box"}) || strings.Contains(n.View().Content, "build-box") {
		t.Errorf("y removed %v and the list shows:\n%s\nwant build-box gone, by its node id", actions.removed, n.View().Content)
	}
}

func TestAddingANodeShowsDisabledMachinesAndOffersNoEnterForARefusal(t *testing.T) {
	n, actions := threeNodes(t)
	actions.candidates = []node.Candidate{
		{Name: "studio.tail5e2c.ts.net", Short: "studio", OS: "macOS", State: node.StateNode, Detail: "factory 0.9.4", Selectable: true},
		{Name: "nas.tail5e2c.ts.net", Short: "nas", OS: "linux", State: node.StateNoNode, Detail: "no node answers at https://nas.tail5e2c.ts.net", Fix: "factory node install", On: "nas"},
		{Name: "ci-runner.tail5e2c.ts.net", Short: "ci-runner", OS: "linux", State: node.StateTagged, Detail: "tagged tag:ci, so it can't show it is yours", Fix: node.UntagFix, On: "ci-runner"},
		{Name: "old-mini.tail5e2c.ts.net", Short: "old-mini", OS: "macOS", State: node.StateOffline, Detail: "offline", LastSeen: nodesNow.Add(-72 * time.Hour)},
	}
	actions.report = node.CheckReport{
		Target:  node.Candidate{Name: "studio.tail5e2c.ts.net", Short: "studio", State: node.StateFunnel},
		Refusal: "Funnel is on, so anyone on the internet reaches its node",
		Checks: []node.Check{
			{Name: "tailscale", Mark: node.MarkOK, Detail: "studio is online, logged in as alice@example.com"},
			{Name: "funnel", Mark: node.MarkFail, Detail: "Funnel is on, so anyone on the internet reaches its node", Fix: "tailscale funnel --https=443 off", On: "studio"},
		},
	}
	pressNodes(n, "a")
	t.Run("pick", func(t *testing.T) { golden.RequireEqual(t, n.View().Content) })
	pressNodes(n, "enter")
	refusal := n.View().Content
	t.Run("refusal", func(t *testing.T) { golden.RequireEqual(t, refusal) })
	pressNodes(n, "enter")
	if strings.Contains(refusal, "⏎") || len(actions.added) > 0 {
		t.Fatalf("a refused check shows:\n%s\nand ⏎ added %v; want no ⏎ hint and nothing added", refusal, actions.added)
	}
}

func TestAddingANodeThatNotifiesAnotherPhoneSwitchesItsKeyOnlyOnP(t *testing.T) {
	studio := node.Candidate{Name: "studio.tail5e2c.ts.net", Short: "studio", OS: "macOS", State: node.StateNode, Detail: "factory 0.9.4", Selectable: true}
	conflict := node.CheckReport{Target: studio, PushConflict: true, Checks: []node.Check{
		{Name: "node", Mark: node.MarkOK, Detail: "0.9.4 · macOS"},
		{Name: "push", Mark: node.MarkWarn, Detail: "studio has its own push key, and 1 phone subscribed to it"},
	}}
	for _, key := range []string{"enter", "p"} {
		n, actions := threeNodes(t)
		actions.candidates, actions.report = []node.Candidate{studio}, conflict
		actions.result = node.AddResult{Member: node.Member{ID: "n-studio", Name: studio.Name}, Push: node.PushOutcome{Conflict: "studio is added, but it keeps its own push key, so it can't notify your phone"}}
		pressNodes(n, "a", "enter")
		check := n.View().Content
		for _, want := range []string{"studio sends with another key", "switches it to macbook's key; that phone subscribes again", "Without p, studio is added but can't notify your phone", "p switch key, then add"} {
			if !strings.Contains(check, want) {
				t.Fatalf("the check of a node with its own subscribed push key shows:\n%s\nwant %q", check, want)
			}
		}
		pressNodes(n, key)
		if want := key == "p"; !slices.Equal(actions.switched, []bool{want}) {
			t.Fatalf("%s added with key switches %v; want only p to switch studio's key", key, actions.switched)
		}
		if key == "enter" && !strings.Contains(n.View().Content, "it keeps its own push key") {
			t.Fatalf("after ⏎ the list shows:\n%s\nwant the named push conflict", n.View().Content)
		}
	}
}

func TestNewFactoryFindsAnIDAlreadyRunningOnAnotherNode(t *testing.T) {
	context := NewContext{
		Repos: []RepoOption{{Name: "cowork", Path: "/r/cowork", Display: "~/dev/cowork", Browser: true, GitURL: "git@github.com:acme/cowork.git"}},
		Nodes: []NodeChoice{{Name: "homelab.tail5e2c.ts.net", Unreachable: true}},
		Elsewhere: []Existing{
			{Key: "node:n-homelab/inc-1", Worktree: "csv-export", Label: "csv-export", Status: "needs you", Stage: "requirements", Remote: "homelab"},
		},
	}
	typed := func(id string, keys ...string) *NewFactoryModel {
		m := NewFactory(context)
		m.Update(tea.WindowSizeMsg{Width: 86, Height: 26})
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
	if r, _ := listed.Result(); !reflect.DeepEqual(r, NewResult{Select: "node:n-homelab/inc-1"}) {
		t.Errorf("an id that runs on homelab gave %+v; want that factory selected, never a second one here", r)
	}
	if here := typed("tag-search", "enter"); here.step != 2 {
		t.Error("an unreachable node blocked a factory on this machine")
	}
}

func TestNewFactoryStartsOnTheDefaultNodeAndNeverFallsBackFromAnUnreachableOne(t *testing.T) {
	context := NewContext{
		Repos:  []RepoOption{{Name: "cowork", Path: "/r/cowork", GitURL: "git@github.com:acme/cowork.git"}},
		Nodes:  []NodeChoice{{Name: "build-box.tail5e2c.ts.net", Unreachable: true}, {Name: "homelab.tail5e2c.ts.net"}},
		Target: "build-box.tail5e2c.ts.net",
	}
	m := NewFactory(context)
	m.Update(tea.WindowSizeMsg{Width: 86, Height: 26})
	for _, r := range "csv" {
		m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.step != 1 || m.node() != "build-box.tail5e2c.ts.net" {
		t.Fatalf("⏎ with the unreachable default node went to step %d on %q; want it held there, never moved to this machine", m.step, m.node())
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	m.Update(probedMsg{node: "homelab.tail5e2c.ts.net", text: "homelab is ready"})
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m.prompt.text = "Export contacts."
	m.start()
	if r, _ := m.Result(); r.(NewResult).Node != "homelab.tail5e2c.ts.net" {
		t.Errorf("picking homelab started %+v; want it created on homelab", r)
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
	t.Run("unlisted repo with its own config", func(t *testing.T) {
		repo := t.TempDir()
		if out, err := exec.Command("git", "-C", repo, "init", "-q").CombinedOutput(); err != nil {
			t.Fatalf("git: %s: %v", out, err)
		}
		os.MkdirAll(state.RepoDir(repo), 0o755)
		os.WriteFile(state.RepoConfigPath(repo), []byte("commands: {build: mine}\n"), 0o644)
		m := NewFactory(NewContext{StateDir: t.TempDir()})
		m.repoPath = repo
		m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		result, ok := m.Result()
		if !ok || strings.Contains(result.(NewResult).Settings.Banner, "wrote") {
			t.Fatalf("banner claims setup wrote an existing config: %+v, %s", result, m.problem)
		}
	})
	t.Run("submodule add lists the remembered repo", func(t *testing.T) {
		repo, source := t.TempDir(), t.TempDir()
		run := func(dir string, args ...string) {
			t.Helper()
			if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
				t.Fatalf("git: %s: %v", out, err)
			}
		}
		run(repo, "init", "-q")
		run(source, "init", "-q")
		run(source, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-qm", "initial")
		run(repo, "-c", "protocol.file.allow=always", "submodule", "add", source, "sub")
		stateDir := t.TempDir()
		m := NewFactory(NewContext{StateDir: stateDir})
		settings, err := m.prepareRepo(filepath.Join(repo, "sub"))
		if err != nil {
			t.Fatal(err)
		}
		known, err := state.LoadKnownRepos(stateDir)
		if err != nil || len(known) != 1 || !sameRepo(settings.Repo, known[0]) {
			t.Fatalf("hub repo %q differs from remembered repos %v: %v", settings.Repo, known, err)
		}
	})
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
	os.Mkdir(filepath.Join(home, "répo one", "child"), 0o755)
	if got := completeDirectory("~/répo one/"); got.suffix != "child/" {
		t.Fatalf("completion after home directory slash: %+v", got)
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
			if settings.values["commands.build"] != "old" || len(settings.fields()) != 10 {
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
			if len(settings.fields()) != 10 {
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
