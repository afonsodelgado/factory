package dialog

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/google/uuid"

	"github.com/afonsodelgado/factory/internal/node"
	"github.com/afonsodelgado/factory/internal/ui/theme"
)

const (
	movePoll    = time.Second
	moveTargetX = 26
)

type MoveContext struct {
	Key         string       `json:"key"`
	Label       string       `json:"label"`
	Factory     string       `json:"factory"`
	Incarnation string       `json:"incarnation,omitempty"`
	Holder      string       `json:"holder,omitempty"`
	HolderShort string       `json:"holder_short"`
	Lost        bool         `json:"lost,omitempty"`
	Targets     []NodeChoice `json:"targets"`
	Repo        string       `json:"repo"`
	GitURL      string       `json:"git_url,omitempty"`
	NoRemote    bool         `json:"no_remote,omitempty"`
}

type MoveActions interface {
	Ready(ctx context.Context, on, gitURL string) (node.Report, error)
	Start(ctx context.Context, on string, r node.MoveRequest) (node.Progress, error)
	Progress(ctx context.Context, on, operation string) (node.Progress, error)
	Cancel(ctx context.Context, on, operation string) (node.Progress, error)
	Takeover(ctx context.Context, on string, r node.TakeoverRequest) (node.Progress, error)
	Moves(ctx context.Context, on string) ([]node.Progress, error)
}

type moveScreen int

const (
	movePickScreen moveScreen = iota
	moveWarnScreen
	moveProgressScreen
)

type readiness struct {
	done   bool
	report node.Report
	err    error
}

type moveReady struct {
	name   string
	report node.Report
	err    error
}

type moveProgressed struct {
	progress node.Progress
	err      error
}

type moveWarned struct {
	warning node.Unconfirmed
}

type moveTick struct{}

type Move struct {
	Context MoveContext
	actions MoveActions
	size
	screen    moveScreen
	cursor    int
	ready     map[string]readiness
	problem   string
	operation string
	on        string
	warning   node.Unconfirmed
	progress  node.Progress
}

func NewMove(c MoveContext, actions MoveActions) *Move {
	return &Move{Context: c, actions: actions, ready: map[string]readiness{}}
}

func (m *Move) Result() (any, bool) { return nil, false }

func (m *Move) Init() tea.Cmd {
	var cmds []tea.Cmd
	if !m.Context.Lost {
		m.on = m.Context.Holder
		cmds = append(cmds, m.resume())
	}
	for _, t := range m.Context.Targets {
		if !t.Unreachable && !m.Context.NoRemote {
			cmds = append(cmds, m.check(t))
		}
	}
	return tea.Batch(cmds...)
}

func (m *Move) check(t NodeChoice) tea.Cmd {
	actions, on, gitURL := m.actions, nodeAddress(t), m.Context.GitURL
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), checkWait)
		defer cancel()
		report, err := actions.Ready(ctx, on, gitURL)
		return moveReady{name: t.Name, report: report, err: err}
	}
}

func (m *Move) resume() tea.Cmd {
	actions, holder, key := m.actions, m.Context.Holder, m.Context.Factory
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), checkWait)
		defer cancel()
		moves, _ := actions.Moves(ctx, holder)
		i := slices.IndexFunc(moves, func(p node.Progress) bool { return p.Role == node.RoleSource && p.Key == key && p.Running })
		if i < 0 {
			return nil
		}
		return moveProgressed{progress: moves[i]}
	}
}

func nodeAddress(t NodeChoice) string {
	if t.Here {
		return ""
	}
	return t.Name
}

func (m *Move) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	m.resize(msg)
	switch msg := msg.(type) {
	case moveReady:
		m.ready[msg.name] = readiness{done: true, report: msg.report, err: msg.err}
	case moveWarned:
		return m, m.warned(msg)
	case moveProgressed:
		return m, m.progressed(msg)
	case moveTick:
		return m, m.poll()
	case tea.KeyPressMsg:
		if msg.String() == "ctrl+c" {
			return m, tea.Quit
		}
		m.problem = ""
		return m, m.key(msg.String())
	}
	return m, nil
}

func (m *Move) key(key string) tea.Cmd {
	switch m.screen {
	case moveWarnScreen:
		if key == "y" {
			return m.takeOver(true)
		}
		m.screen = movePickScreen
		return nil
	case moveProgressScreen:
		return m.progressKey(key)
	}
	switch key {
	case "esc":
		return tea.Quit
	case "up", "k":
		m.cursor = max(m.cursor-1, 0)
	case "down", "j":
		m.cursor = min(m.cursor+1, len(m.Context.Targets)-1)
	case "enter":
		return m.choose()
	}
	return nil
}

func (m *Move) picked() (NodeChoice, bool) {
	if m.cursor >= len(m.Context.Targets) {
		return NodeChoice{}, false
	}
	return m.Context.Targets[m.cursor], true
}

func (m *Move) missingRepo(t NodeChoice) (node.Check, bool) {
	r := m.ready[t.Name]
	i := slices.IndexFunc(r.report.Checks, func(c node.Check) bool { return c.Name == "repo" && c.Mark == node.MarkFail })
	if i < 0 {
		return node.Check{}, false
	}
	return r.report.Checks[i], true
}

func (m *Move) noRemote() string {
	ctx := m.Context
	return ctx.Repo + " has no network git remote, so no other node can have it and its factories can't move. Push it to a remote, clone it on another node and run factory init there; then factories of " + ctx.Repo + " can move to that node."
}

func (m *Move) refusal(t NodeChoice) string {
	r := m.ready[t.Name]
	short := shortName(t.Name)
	missing, lacks := m.missingRepo(t)
	switch {
	case m.Context.NoRemote:
		return m.noRemote()
	case t.Unreachable:
		return short + " is unreachable, so nothing moves there. Pick another node."
	case lacks:
		return missing.Sentence() + ". Nothing moved."
	case !r.done:
		return short + " has not answered its readiness check yet."
	case r.err != nil:
		return short + " did not answer its readiness check: " + r.err.Error()
	case r.report.Problems > 0:
		failed := failing(node.CheckReport{Checks: r.report.Checks})
		return short + " can't run factories yet: " + failed.Name + ": " + failed.Detail
	}
	return ""
}

func (m *Move) choose() tea.Cmd {
	t, ok := m.picked()
	if !ok {
		return nil
	}
	if reason := m.refusal(t); reason != "" {
		m.problem = reason
		return nil
	}
	m.operation = uuid.NewString()
	if m.Context.Lost {
		m.on = nodeAddress(t)
		return m.takeOver(false)
	}
	actions, holder := m.actions, m.Context.Holder
	request := node.MoveRequest{Operation: m.operation, Factory: m.Context.Factory, Target: t.Name}
	m.on = holder
	return func() tea.Msg {
		p, err := actions.Start(context.Background(), holder, request)
		return moveProgressed{progress: p, err: err}
	}
}

func (m *Move) takeOver(confirm bool) tea.Cmd {
	actions, on := m.actions, m.on
	request := node.TakeoverRequest{Operation: m.operation, Factory: m.Context.Incarnation, Confirm: confirm}
	return func() tea.Msg {
		p, err := actions.Takeover(context.Background(), on, request)
		var warning node.Unconfirmed
		if errors.As(err, &warning) {
			return moveWarned{warning: warning}
		}
		return moveProgressed{progress: p, err: err}
	}
}

func (m *Move) warned(msg moveWarned) tea.Cmd {
	m.warning, m.screen = msg.warning, moveWarnScreen
	return nil
}

func (m *Move) progressed(msg moveProgressed) tea.Cmd {
	switch {
	case msg.err != nil && m.screen != moveProgressScreen:
		m.problem, m.screen = msg.err.Error(), movePickScreen
		return nil
	case msg.err != nil:
		m.problem = msg.err.Error()
	default:
		m.progress, m.screen = msg.progress, moveProgressScreen
		m.operation = cmp.Or(msg.progress.Operation, m.operation)
	}
	if settled(m.progress) {
		return nil
	}
	return tea.Tick(movePoll, func(time.Time) tea.Msg { return moveTick{} })
}

func (m *Move) poll() tea.Cmd {
	actions, on, op := m.actions, m.on, m.operation
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), checkWait)
		defer cancel()
		p, err := actions.Progress(ctx, on, op)
		return moveProgressed{progress: p, err: err}
	}
}

func settled(p node.Progress) bool {
	return slices.Contains([]string{node.StateActive, node.StatePreflightFailed, node.StateRefused, node.StateCancelled, node.StateRecoveryNeeded, node.StateInterrupted}, p.State)
}

func (m *Move) cancellable() bool {
	p := m.progress
	released := p.Phase == node.PhaseReleased || p.Phase == node.PhaseActive
	return !m.Context.Lost && p.Role != node.RoleTarget && !released && !settled(p)
}

func (m *Move) progressKey(key string) tea.Cmd {
	switch {
	case key == "esc":
		return tea.Quit
	case key == "x" && m.cancellable():
		actions, on, op := m.actions, m.on, m.operation
		return func() tea.Msg {
			p, err := actions.Cancel(context.Background(), on, op)
			return moveProgressed{progress: p, err: err}
		}
	}
	return nil
}

func (m *Move) View() tea.View {
	c := theme.NewCanvas(m.width, m.height)
	switch m.screen {
	case moveWarnScreen:
		m.drawWarning(c)
	case moveProgressScreen:
		m.drawProgress(c)
	default:
		m.drawPick(c)
	}
	return view(c)
}

func (m *Move) drawPick(c *theme.Canvas) {
	ctx := m.Context
	if ctx.Lost {
		c.Put(1, 0, "Take "+ctx.Label+" over", theme.Bright.B())
		c.Right(c.W-2, 0, "↮ "+ctx.HolderShort+" unreachable", theme.FgInk(theme.Red))
	} else {
		c.Put(1, 0, "Move "+ctx.Label, theme.Bright.B())
		c.Right(c.W-2, 0, "on "+ctx.HolderShort, theme.Muted)
	}
	c.Put(3, 2, "node", theme.Muted)
	c.Put(moveTargetX, 2, "state", theme.Muted)
	for i, t := range ctx.Targets {
		y := 3 + i
		if i == m.cursor {
			c.Fill(0, y, c.W, 1, theme.Selection)
			c.Put(1, y, "❯", theme.Focused.B())
		}
		name := shortName(t.Name)
		if t.Here {
			name += " (this machine)"
		}
		state, ink := m.targetState(t)
		c.PutMax(3, y, name, theme.Bright, moveTargetX-4)
		c.PutMax(moveTargetX, y, state, ink, c.W-moveTargetX-1)
	}
	y := 4 + len(ctx.Targets)
	if len(ctx.Targets) == 0 {
		y = paragraph(c, 3, "No other node is listed. N adds one.", theme.Text) + 1
	}
	y = paragraph(c, y, m.explanation(), theme.Text) + 1
	if m.problem != "" {
		paragraph(c, y, "✗ "+m.problem, theme.FgInk(theme.Red))
	}
	action := "⏎ move there"
	if ctx.Lost {
		action = "⏎ see the takeover warning"
	}
	c.Put(1, c.H-1, action+"   j/k pick   esc close", theme.Text)
}

func (m *Move) explanation() string {
	ctx := m.Context
	switch {
	case ctx.NoRemote:
		return m.noRemote()
	case ctx.Lost:
		return ctx.HolderShort + " is unreachable, so " + ctx.Label + " can't stop at a checkpoint there. A node that kept a checkpoint of " + ctx.Label + " from an earlier move, and has " + ctx.Repo + " added, can take it over from that checkpoint; it warns you first."
	}
	return ctx.Label + " stops at its next checkpoint on " + ctx.HolderShort + ", when it waits for you or between stages. Its branches are pushed and the node you pick resumes it with /factory resume and no old conversation. Nodes that are unreachable, not ready or without " + ctx.Repo + " can't be picked."
}

func (m *Move) targetState(t NodeChoice) (string, theme.Ink) {
	r := m.ready[t.Name]
	_, lacks := m.missingRepo(t)
	switch {
	case m.Context.NoRemote:
		return "– " + m.Context.Repo + " is on " + m.Context.HolderShort + " only", theme.Quiet
	case t.Unreachable:
		return "↮ unreachable", theme.FgInk(theme.Red)
	case lacks:
		return "– " + m.Context.Repo + " isn't added here", theme.Quiet
	case !r.done:
		return "◌ asking…", theme.FgInk(theme.Cyan)
	case r.err != nil:
		return "↮ no answer", theme.FgInk(theme.Red)
	case r.report.Problems > 0:
		return "! " + plural(r.report.Problems, "problem") + " to fix", theme.FgInk(theme.Yellow)
	}
	return "● ready", theme.FgInk(theme.Green)
}

func (m *Move) drawWarning(c *theme.Canvas) {
	t, _ := m.picked()
	short := shortName(t.Name)
	c.Put(1, 0, "Take "+m.Context.Label+" over on "+short+"?", theme.Bright.B())
	y := paragraph(c, 2, "! "+m.warning.Warning, theme.FgInk(theme.Yellow)) + 1
	paragraph(c, y, "Nothing changes until you press y.", theme.Text)
	line := []theme.Span{{Text: "y", Ink: theme.Key}, {Text: "  take " + m.Context.Label + " over on " + short + "       ", Ink: theme.Text}, {Text: "n", Ink: theme.Key}, {Text: "  cancel, the default", Ink: theme.Text}}
	c.Spans(max((c.W-theme.SpansWidth(line))/2, 1), c.H-1, line)
}

func (m *Move) names() (label, source, target string) {
	p := m.progress
	t, _ := m.picked()
	return cmp.Or(p.Factory, m.Context.Label), cmp.Or(p.SourceName, m.Context.HolderShort), cmp.Or(p.TargetName, shortName(t.Name))
}

func (m *Move) drawProgress(c *theme.Canvas) {
	p := m.progress
	label, source, target := m.names()
	verb := "Moving "
	if m.Context.Lost {
		verb = "Taking over "
	}
	c.Put(1, 0, verb+label+" to "+target, theme.Bright.B())
	if p.Phase != "" {
		c.Right(c.W-2, 0, p.Phase, theme.Muted)
	}
	headline, ink := m.headline()
	y := paragraph(c, 2, headline, ink) + 1
	switch p.State {
	case node.StateCheckpointWaiting:
		y = paragraph(c, y, p.Waiting, theme.Text) + 1
	case node.StatePreflightFailed:
		failed := slices.DeleteFunc(slices.Clone(p.Checks), func(c node.Check) bool { return c.Mark == node.MarkOK })
		y = drawChecks(c, y, checkRows(failed)) + 1
	case node.StateRecoveryNeeded, node.StateInterrupted:
		for _, step := range p.Recovery {
			y = paragraph(c, y, "· "+step, theme.Text)
		}
		y++
	}
	if p.State == node.StateActive && p.DiscoversQA {
		y = paragraph(c, y, target+" kept its own repo config for "+m.Context.Repo+". Its QA commands are still blank, so "+label+" discovers them there before QA.", theme.Text) + 1
	}
	if after := m.afterword(label, source, target); after != "" {
		y = paragraph(c, y, after, theme.Muted) + 1
	}
	if m.problem != "" {
		paragraph(c, y, "✗ "+m.problem, theme.FgInk(theme.Red))
	}
	c.Put(1, c.H-1, m.progressHints(target), theme.Text)
}

func (m *Move) headline() (string, theme.Ink) {
	p := m.progress
	label, source, target := m.names()
	cyan, red, yellow := theme.FgInk(theme.Cyan), theme.FgInk(theme.Red), theme.FgInk(theme.Yellow)
	switch p.State {
	case node.StateCheckpointWaiting:
		return "◌ waiting for " + label + " to reach a checkpoint on " + source, cyan
	case node.StatePreflightFailed:
		return "✗ " + target + " can't run " + label + ": " + withStop(p.Refusal), red
	case node.StateRefused:
		return "✗ Not moved: " + withStop(p.Refusal), red
	case node.StateCancelled:
		return "– The move was cancelled before " + label + " was released.", theme.Muted
	case node.StateStaged:
		return "◌ " + target + " has the checkpoint; stopping " + label + " on " + source, cyan
	case node.StateReleased:
		return "◌ " + label + " is stopped on " + source + "; " + target + " is starting it", cyan
	case node.StateRecoveryNeeded:
		return "! " + label + " is stopped mid-move and runs nowhere yet", yellow
	case node.StateInterrupted:
		return "! The move stopped before it finished", yellow
	case node.StateActive:
		return "✓ " + label + " runs on " + target + " now.", theme.FgInk(theme.Green)
	}
	if p.Phase == node.PhaseCheckpoint {
		return "◌ sending the checkpoint of " + label + " to " + target, cyan
	}
	if p.Factory == "" {
		return "◌ starting the move", cyan
	}
	return "◌ checking that " + target + " can run " + label, cyan
}

func (m *Move) afterword(label, source, target string) string {
	switch p := m.progress; {
	case p.State == node.StateActive && !p.Forced:
		return "Its files stay on " + source + " until factory delete " + label + " there."
	case p.State == node.StatePreflightFailed, p.State == node.StateRefused, p.State == node.StateCancelled:
		return label + " still runs on " + source + ". Nothing moved."
	case p.State == node.StateReleased:
		return "It can't be cancelled now: " + target + " finishes the move, or this names how to recover it."
	}
	return ""
}

func (m *Move) progressHints(target string) string {
	switch p := m.progress; {
	case m.cancellable():
		return "x cancel the move   esc close; it keeps going"
	case p.State == node.StateReleased || p.State == node.StateStaged:
		return "esc close; " + target + " finishes it"
	}
	return "esc close"
}
