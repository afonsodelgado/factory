package dialog

import (
	"fmt"

	tea "charm.land/bubbletea/v2"

	"github.com/afonsodelgado/factory/internal/ui/theme"
)

const (
	nodeX      = 3
	nodeStateX = 17
	versionX   = 36
	countX     = 45
	defaultX   = 66
	fieldX     = 15
)

type NodesContext struct {
	Machine   string
	Tailnet   string
	Version   string
	Factories int
	NeedsYou  int
	Phone     PhoneContext
}

type Nodes struct {
	Context NodesContext
	actions PhoneActions
	size
	details  bool
	checkRun int
	checks   []CheckRow
	scroll   int
}

func NewNodes(c NodesContext, actions PhoneActions) *Nodes {
	return &Nodes{Context: c, actions: actions}
}

func (n *Nodes) Init() tea.Cmd { return nil }

func (n *Nodes) Result() (any, bool) { return nil, false }

func (n *Nodes) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	n.resize(msg)
	switch msg := msg.(type) {
	case phoneChecks:
		if msg.run == n.checkRun && n.details {
			n.checks = msg.rows
		}
	case tea.KeyPressMsg:
		return n, n.key(msg.String())
	}
	return n, nil
}

func (n *Nodes) key(key string) tea.Cmd {
	switch {
	case key == "ctrl+c", key == "esc" && !n.details:
		return tea.Quit
	case key == "esc":
		n.details = false
		n.checkRun++
	case key == "enter" && !n.details, key == "r" && n.details:
		n.details, n.checks, n.scroll = true, nil, 0
		n.checkRun++
		return runChecks(n.actions, n.checkRun)
	case n.details && (key == "j" || key == "down"):
		n.scroll = min(n.scroll+1, n.maxScroll())
	case n.details && (key == "k" || key == "up"):
		n.scroll = max(n.scroll-1, 0)
	}
	return nil
}

func (n *Nodes) View() tea.View {
	c := theme.NewCanvas(n.width, n.height)
	if n.details {
		n.drawDetails(c)
	} else {
		n.drawList(c)
	}
	return view(c)
}

func (n *Nodes) drawList(c *theme.Canvas) {
	c.Put(1, 0, "Nodes", theme.Bright.B())
	c.Right(c.W-2, 0, n.Context.Tailnet, theme.Muted)
	defaultMark := "● default"
	newOnesX := min(defaultX, c.W-1-theme.Width(defaultMark))
	for x, heading := range map[int]string{nodeX: "node", nodeStateX: "state", versionX: "version", countX: "factories", newOnesX: "new ones"} {
		c.Put(x, 2, heading, theme.Muted)
	}
	c.Fill(0, 3, c.W, 1, theme.Selection)
	c.Put(1, 3, "❯", theme.Focused.B())
	c.PutMax(nodeX, 3, n.Context.Machine, theme.Bright, nodeStateX-nodeX-1)
	state, stateInk := n.state()
	c.Put(nodeStateX, 3, state, stateInk)
	c.PutMax(versionX, 3, n.version(), theme.Text, countX-versionX-1)
	n.drawFactories(c, countX, 3, "0")
	c.Put(newOnesX, 3, defaultMark, theme.FgInk(theme.Green))
	y := paragraph(c, 5, "No other nodes yet. New factories run on "+n.Context.Machine+".", theme.Text)
	n.drawPhoneApp(c, y+1)
	c.Put(1, c.H-1, "⏎ details   esc close", theme.Text)
}

func (n *Nodes) state() (string, theme.Ink) {
	if n.Context.Phone.Running {
		return "● this machine", theme.FgInk(theme.Green)
	}
	return "! not running", theme.FgInk(theme.Yellow)
}

func (n *Nodes) answered() bool { return n.Context.Version != "" }

func (n *Nodes) version() string {
	if !n.answered() {
		return "–"
	}
	return n.Context.Version
}

func (n *Nodes) drawFactories(c *theme.Canvas, x, y int, none string) {
	switch {
	case !n.answered():
		c.Put(x, y, "–", theme.Muted)
		return
	case n.Context.Factories == 0:
		c.Put(x, y, none, theme.Muted)
		return
	}
	end := c.Put(x, y, fmt.Sprint(n.Context.Factories), theme.Text)
	if n.Context.NeedsYou > 0 {
		c.Put(end, y, fmt.Sprintf(" · %d needs you", n.Context.NeedsYou), theme.FgInk(theme.Orange))
	}
}

func (n *Nodes) drawPhoneApp(c *theme.Canvas, y int) {
	c.Put(1, y, "Phone app", theme.Bright.B())
	url, urlInk := n.Context.Phone.url()
	end := c.PutMax(valueX, y, url, urlInk, c.W-valueX-1)
	where := "lives on " + n.Context.Machine
	if n.Context.Phone.Hostname != "" && end+3+theme.Width(where) <= c.W-1 {
		c.Put(end+3, y, where, theme.Muted)
	}
	notify, notifyInk := n.Context.Phone.notify()
	c.Put(3, y+1, "notify", theme.Text)
	c.PutMax(valueX, y+1, notify, notifyInk, c.W-valueX-1)
	if n.Context.Phone.Hostname == "" {
		return
	}
	c.Put(3, y+2, "open", theme.Text)
	for i, line := range theme.Wrap("that URL on your phone with Tailscale on. No code to scan or type.", c.W-valueX-1) {
		c.Put(valueX, y+2+i, line, theme.Muted)
	}
}

func (n *Nodes) drawDetails(c *theme.Canvas) {
	c.Put(1, 0, n.Context.Machine, theme.Bright.B())
	state, stateInk := n.state()
	c.Right(c.W-2, 0, state, stateInk)
	c.Clip(1, c.H-1)
	n.drawDetailsBody(c, 2-n.scroll)
	c.Unclip()
	hints := "r check again   esc back"
	if n.checks == nil {
		hints = "esc back"
	}
	if n.maxScroll() > 0 {
		hints = "j/k scroll   " + hints
	}
	c.Put(1, c.H-1, hints, theme.Text)
}

func (n *Nodes) maxScroll() int {
	c := theme.NewCanvas(n.width, n.height)
	c.Clip(0, 0)
	return max(n.drawDetailsBody(c, 2)-(c.H-1), 0)
}

func (n *Nodes) drawDetailsBody(c *theme.Canvas, y int) int {
	url, urlInk := n.Context.Phone.url()
	notify, notifyInk := n.Context.Phone.notify()
	c.Put(3, y, "address", theme.Text)
	c.PutMax(fieldX, y, url, urlInk, c.W-fieldX-1)
	c.Put(3, y+1, "factories", theme.Text)
	n.drawFactories(c, fieldX, y+1, "none yet")
	c.Put(3, y+2, "notifies", theme.Text)
	c.PutMax(fieldX, y+2, notify, notifyInk, c.W-fieldX-1)
	c.Put(1, y+4, "Checks", theme.Bright.B())
	if n.checks == nil {
		c.Put(3, y+5, "◌ checking Tailscale, the service and the push services…", theme.Muted)
		return y + 6
	}
	end := drawChecks(c, y+5, n.checks)
	return paragraph(c, end+1, "Nothing was changed. Each fix runs on "+n.Context.Machine+"; r checks again.", theme.Muted)
}
