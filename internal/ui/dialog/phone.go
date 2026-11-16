package dialog

import (
	"fmt"

	tea "charm.land/bubbletea/v2"

	"github.com/afonsodelgado/factory/internal/ui/theme"
)

type phoneScreen int

const (
	phoneMain phoneScreen = iota
	phoneChecking
	phoneChecked
)

type PhoneContext struct {
	Running       bool   `json:"running"`
	Hostname      string `json:"hostname"`
	Port          int    `json:"port"`
	Subscriptions int    `json:"subscriptions"`
}

type PhoneActions interface {
	Status() (PhoneContext, error)
	Test() (string, error)
	Check() []CheckRow
}

type phoneDone struct {
	status  PhoneContext
	notice  string
	problem string
}

type phoneChecks struct {
	run  int
	rows []CheckRow
}

type Phone struct {
	Context PhoneContext
	actions PhoneActions
	size
	screen   phoneScreen
	busy     string
	notice   string
	problem  string
	checkRun int
	checks   []CheckRow
}

func NewPhone(c PhoneContext, actions PhoneActions) *Phone {
	return &Phone{Context: c, actions: actions}
}

func (p *Phone) Init() tea.Cmd { return nil }

func (p *Phone) Result() (any, bool) { return nil, false }

func (p *Phone) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	p.resize(msg)
	switch msg := msg.(type) {
	case phoneDone:
		p.busy, p.Context, p.notice, p.problem = "", msg.status, msg.notice, msg.problem
	case phoneChecks:
		if msg.run == p.checkRun && p.screen == phoneChecking {
			p.checks, p.screen = msg.rows, phoneChecked
		}
	case tea.KeyPressMsg:
		if msg.String() == "ctrl+c" {
			return p, tea.Quit
		}
		p.problem = ""
		return p, p.key(msg)
	}
	return p, nil
}

func (p *Phone) key(msg tea.KeyPressMsg) tea.Cmd {
	if p.screen != phoneMain {
		if msg.String() == "esc" {
			p.checkRun++
			p.screen = phoneMain
		}
		return nil
	}
	if p.busy != "" && msg.String() != "esc" {
		return nil
	}
	switch msg.String() {
	case "esc":
		return tea.Quit
	case "t":
		p.busy = "Sending a test notification…"
		fallback := p.Context
		return func() tea.Msg {
			result, err := p.actions.Test()
			return p.finish(fallback, result, err)
		}
	case "h":
		p.checkRun++
		p.screen = phoneChecking
		return runChecks(p.actions, p.checkRun)
	}
	return nil
}

func runChecks(actions PhoneActions, run int) tea.Cmd {
	return func() tea.Msg { return phoneChecks{run, actions.Check()} }
}

func (p *Phone) finish(fallback PhoneContext, notice string, err error) phoneDone {
	status, statusErr := p.actions.Status()
	if statusErr != nil {
		status = fallback
	}
	if err != nil {
		return phoneDone{status: status, problem: err.Error()}
	}
	return phoneDone{status: status, notice: notice}
}

func (p *Phone) View() tea.View {
	c := theme.NewCanvas(p.width, p.height)
	if p.screen == phoneMain {
		p.drawMain(c)
	} else {
		p.drawChecks(c)
	}
	return view(c)
}

func (p *Phone) drawMain(c *theme.Canvas) {
	c.Put(1, 0, "Phone", theme.Bright.B())
	running, runningInk := "not running; start it with factory node run", theme.FgInk(theme.Yellow)
	if p.Context.Running {
		running, runningInk = "running", theme.FgInk(theme.Green)
	}
	url, urlInk := p.Context.url()
	notify, notifyInk := p.Context.notify()
	rows := []struct {
		label, value string
		ink          theme.Ink
	}{
		{"node", running, runningInk},
		{"url", url, urlInk},
		{"port", fmt.Sprintf("127.0.0.1:%d, forwarded by tailscale serve", p.Context.Port), theme.Text},
		{"notify", notify, notifyInk},
	}
	for i, row := range rows {
		c.Put(1, 2+i, row.label, theme.Muted)
		c.PutMax(checkDetailX-10, 2+i, row.value, row.ink, c.W-checkDetailX+9)
	}
	y := paragraph(c, 7, "Open the url on a phone signed in to Tailscale as this node's owner. Anyone else gets a refusal.", theme.Muted)
	if text, ink := p.mainMessage(); text != "" {
		paragraph(c, y+1, text, ink)
	}
	c.Put(1, c.H-1, "t test push   h checks   esc close", theme.Text)
}

func (c PhoneContext) url() (string, theme.Ink) {
	if c.Hostname == "" {
		return "unknown; put node.hostname, this machine's ts.net name, in config.yml", theme.FgInk(theme.Yellow)
	}
	return "https://" + c.Hostname, theme.Bright
}

func (c PhoneContext) notify() (string, theme.Ink) {
	if c.Subscriptions == 0 {
		return "off: no phone has turned notifications on yet", theme.Muted
	}
	return "on for " + plural(c.Subscriptions, "phone"), theme.FgInk(theme.Green)
}

func (p *Phone) mainMessage() (string, theme.Ink) {
	switch {
	case p.busy != "":
		return "◌ " + p.busy, theme.FgInk(theme.Yellow)
	case p.problem != "":
		return "✗ " + p.problem, theme.FgInk(theme.Red)
	case p.notice != "":
		return "✓ " + p.notice, theme.FgInk(theme.Green)
	}
	return "", theme.Text
}

func (p *Phone) drawChecks(c *theme.Canvas) {
	if p.screen == phoneChecking {
		c.Put(1, 0, "Checking Tailscale and the push services…", theme.Bright.B())
		c.Put(1, c.H-1, "esc stop", theme.Text)
		return
	}
	c.Put(1, 0, "Checks", theme.Bright.B())
	y := drawChecks(c, 2, p.checks)
	paragraph(c, y+1, "Nothing was changed. Reaching a push service does not prove a phone gets notifications; t sends a test.", theme.Muted)
	c.Put(1, c.H-1, "esc back", theme.Text)
}
