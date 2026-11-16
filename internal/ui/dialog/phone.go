package dialog

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/afonsodelgado/factory/internal/ui/theme"
)

type phoneScreen int

const (
	phoneMain phoneScreen = iota
	phoneConfigure
	phonePairing
	phoneChecking
	phoneChecked
)

const (
	hostnameField = iota
	portField
)

type PhoneContext struct {
	Enabled  bool          `json:"enabled"`
	Running  bool          `json:"running"`
	Owner    string        `json:"owner,omitempty"`
	Hostname string        `json:"hostname"`
	Port     int           `json:"port"`
	Devices  []PhoneDevice `json:"devices"`
}

type PhoneDevice struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Paired string `json:"paired"`
}

type PhonePairing struct {
	URL     string
	Expires time.Time
	QR      []string
}

type PhoneActions interface {
	Status() (PhoneContext, error)
	Enable() error
	Disable() error
	Start() error
	Stop() error
	Configure(port int, hostname string) error
	Pair() (PhonePairing, error)
	Revoke(id string) error
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

type phonePaired struct {
	pairing PhonePairing
	err     error
}

type phoneTick struct{}

type Phone struct {
	Context PhoneContext
	actions PhoneActions
	Now     func() time.Time
	size
	screen   phoneScreen
	cursor   int
	revoking bool
	busy     string
	notice   string
	problem  string
	field    int
	fields   [2]string
	pairing  PhonePairing
	checkRun int
	checks   []CheckRow
}

func NewPhone(c PhoneContext, actions PhoneActions) *Phone {
	return &Phone{Context: c, actions: actions, Now: time.Now}
}

func (p *Phone) Init() tea.Cmd { return nil }

func (p *Phone) Result() (any, bool) { return nil, false }

func (p *Phone) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	p.resize(msg)
	switch msg := msg.(type) {
	case phoneDone:
		p.busy, p.Context, p.notice, p.problem = "", msg.status, msg.notice, msg.problem
		p.cursor = min(p.cursor, max(len(p.Context.Devices)-1, 0))
	case phoneChecks:
		if msg.run == p.checkRun && p.screen == phoneChecking {
			p.checks, p.screen = msg.rows, phoneChecked
		}
	case phonePaired:
		p.busy = ""
		if msg.err != nil {
			p.problem = msg.err.Error()
			return p, nil
		}
		p.pairing, p.screen = msg.pairing, phonePairing
		return p, tick()
	case phoneTick:
		if p.screen == phonePairing && p.Now().Before(p.pairing.Expires) {
			return p, tick()
		}
	case tea.PasteMsg:
		p.typeText(strings.ReplaceAll(DecodeKeys(msg.Content), "\n", ""))
	case tea.KeyPressMsg:
		if msg.String() == "ctrl+c" {
			return p, tea.Quit
		}
		p.problem = ""
		return p, p.key(msg)
	}
	return p, nil
}

func tick() tea.Cmd {
	return tea.Tick(time.Second, func(time.Time) tea.Msg { return phoneTick{} })
}

func (p *Phone) key(msg tea.KeyPressMsg) tea.Cmd {
	switch p.screen {
	case phoneConfigure:
		return p.configureKey(msg)
	case phonePairing, phoneChecking, phoneChecked:
		if msg.String() != "esc" {
			return nil
		}
		p.checkRun++
		paired := p.screen == phonePairing
		p.screen = phoneMain
		if paired {
			return p.run("Looking for the new phone…", "", func() error { return nil })
		}
		return nil
	}
	return p.mainKey(msg)
}

func (p *Phone) mainKey(msg tea.KeyPressMsg) tea.Cmd {
	if p.revoking {
		p.revoking = false
		if msg.String() == "y" {
			device := p.Context.Devices[p.cursor]
			return p.run("Revoking "+device.Name+"…", "Revoked "+device.Name+".", func() error { return p.actions.Revoke(device.ID) })
		}
		return nil
	}
	if p.busy != "" && msg.String() != "esc" {
		return nil
	}
	switch msg.String() {
	case "esc":
		return tea.Quit
	case "j", "down":
		p.cursor = min(p.cursor+1, max(len(p.Context.Devices)-1, 0))
	case "k", "up":
		p.cursor = max(p.cursor-1, 0)
	case "e":
		if p.Context.Enabled {
			return p.run("Turning Phone off…", "Phone is off. Paired phones keep their pairing.", p.actions.Disable)
		}
		return p.run("Turning Phone on…", "Phone is on.", p.actions.Enable)
	case "s":
		if p.Context.Running {
			return p.run("Stopping factory serve…", "factory serve stopped. Phones get no notifications until it starts.", p.actions.Stop)
		}
		return p.run("Starting factory serve…", "factory serve is running.", p.actions.Start)
	case "c":
		p.screen, p.field = phoneConfigure, hostnameField
		p.fields = [2]string{p.Context.Hostname, strconv.Itoa(p.Context.Port)}
	case "p":
		p.busy = "Making a pairing code…"
		return func() tea.Msg {
			pairing, err := p.actions.Pair()
			return phonePaired{pairing, err}
		}
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
		run := p.checkRun
		return func() tea.Msg { return phoneChecks{run, p.actions.Check()} }
	case "r":
		p.revoking = len(p.Context.Devices) > 0
	}
	return nil
}

func (p *Phone) run(busy, done string, action func() error) tea.Cmd {
	p.busy = busy
	fallback := p.Context
	return func() tea.Msg { return p.finish(fallback, done, action()) }
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

func (p *Phone) configureKey(msg tea.KeyPressMsg) tea.Cmd {
	switch msg.String() {
	case "esc":
		p.screen = phoneMain
	case "tab", "down", "up", "shift+tab":
		p.field = 1 - p.field
	case "backspace":
		text := []rune(p.fields[p.field])
		if len(text) > 0 {
			p.fields[p.field] = string(text[:len(text)-1])
		}
	case "enter":
		port, err := strconv.Atoi(strings.TrimSpace(p.fields[portField]))
		if err != nil {
			p.problem = "the port must be a number, such as 7418"
			return nil
		}
		hostname := strings.TrimSpace(p.fields[hostnameField])
		p.screen = phoneMain
		return p.run("Saving…", "Saved. Restart factory serve to use a new port or hostname.", func() error { return p.actions.Configure(port, hostname) })
	default:
		if msg.Text != "" {
			p.typeText(msg.Text)
		}
	}
	return nil
}

func (p *Phone) typeText(text string) {
	if p.screen == phoneConfigure {
		p.fields[p.field] += text
	}
}

func (p *Phone) View() tea.View {
	c := theme.NewCanvas(p.width, p.height)
	switch p.screen {
	case phoneConfigure:
		p.drawConfigure(c)
	case phonePairing:
		p.drawPairing(c)
	case phoneChecking, phoneChecked:
		p.drawChecks(c)
	default:
		p.drawMain(c)
	}
	return view(c)
}

func (p *Phone) drawMain(c *theme.Canvas) {
	c.Put(1, 0, "Phone", theme.Bright.B())
	on, onInk := "off", theme.Muted
	if p.Context.Enabled {
		on, onInk = "on", theme.FgInk(theme.Green)
	}
	serve, serveInk := "not running", theme.FgInk(theme.Yellow)
	if p.Context.Running {
		serve, serveInk = "running", theme.FgInk(theme.Green)
		if p.Context.Owner != "" {
			serve += " · " + p.Context.Owner
		}
	}
	url := "set this machine's ts.net name with c"
	if p.Context.Hostname != "" {
		url = "https://" + p.Context.Hostname
	}
	rows := []struct {
		label, value string
		ink          theme.Ink
	}{
		{"phone", on, onInk},
		{"serve", serve, serveInk},
		{"url", url, theme.Bright},
		{"port", fmt.Sprintf("127.0.0.1:%d, forwarded by tailscale serve", p.Context.Port), theme.Text},
	}
	for i, row := range rows {
		c.Put(1, 2+i, row.label, theme.Muted)
		c.PutMax(checkDetailX-10, 2+i, row.value, row.ink, c.W-checkDetailX+9)
	}
	y := 7
	c.Put(1, y, "Paired phones", theme.Bright)
	y++
	if len(p.Context.Devices) == 0 {
		c.Put(3, y, "none yet; p shows a code to scan", theme.Muted)
		y++
	}
	for i, d := range p.Context.Devices {
		ink := theme.Text
		if i == p.cursor {
			c.Put(1, y, "›", theme.Key)
			ink = theme.Bright
		}
		c.PutMax(3, y, d.Name, ink, 24)
		c.PutMax(28, y, "paired "+d.Paired, theme.Muted, c.W-29)
		y++
	}
	y++
	switch {
	case p.revoking:
		paragraph(c, y, fmt.Sprintf("Revoke %s? It loses access and its notifications at once. y revokes, any other key keeps it.", p.Context.Devices[p.cursor].Name), theme.FgInk(theme.Yellow))
	case p.busy != "":
		paragraph(c, y, "◌ "+p.busy, theme.FgInk(theme.Yellow))
	case p.problem != "":
		paragraph(c, y, "✗ "+p.problem, theme.FgInk(theme.Red))
	case p.notice != "":
		paragraph(c, y, "✓ "+p.notice, theme.FgInk(theme.Green))
	}
	hints := packHints(p.mainHints(), c.W-2)
	for i, line := range hints {
		c.Put(1, c.H-len(hints)+i, line, theme.Text)
	}
}

func (p *Phone) mainHints() []string {
	toggle, serve := "e turn on", "s start"
	if p.Context.Enabled {
		toggle = "e turn off"
	}
	if p.Context.Running {
		serve = "s stop"
	}
	hints := []string{toggle, serve, "c configure", "p pair", "t test push", "h checks"}
	if len(p.Context.Devices) > 0 {
		hints = append(hints, "r revoke")
	}
	return append(hints, "esc close")
}

func packHints(hints []string, width int) []string {
	var rows []string
	for _, hint := range hints {
		last := len(rows) - 1
		if last >= 0 && theme.Width(rows[last]+"   "+hint) <= width {
			rows[last] += "   " + hint
			continue
		}
		rows = append(rows, hint)
	}
	return rows
}

func breakAt(text string, width int) []string {
	var lines []string
	line := ""
	for _, r := range text {
		if line != "" && theme.Width(line+string(r)) > width {
			lines = append(lines, line)
			line = ""
		}
		line += string(r)
	}
	return append(lines, line)
}

func (p *Phone) drawConfigure(c *theme.Canvas) {
	c.Put(1, 0, "Configure Phone", theme.Bright.B())
	labels := [2]string{"This machine's full Tailscale name, such as devbox.tail1234.ts.net", "Local port that tailscale serve forwards to"}
	for i, label := range labels {
		text := p.fields[i]
		if p.field == i {
			text += "█"
		}
		c.Put(1, 2+i*4, label, theme.Text)
		drawInput(c, 1, 3+i*4, c.W-2, "", []string{text}, p.field == i, theme.Focused)
	}
	y := paragraph(c, 11, "The app answers only at https://<name>/, through tailscale serve. factory never changes your tailnet.", theme.Muted)
	if p.problem != "" {
		paragraph(c, y+1, "✗ "+p.problem, theme.FgInk(theme.Red))
	}
	c.Put(1, c.H-1, "⏎ save   tab next field   esc back", theme.Text)
}

func (p *Phone) drawPairing(c *theme.Canvas) {
	left := p.pairing.Expires.Sub(p.Now()).Round(time.Second)
	c.Put(1, 0, "Pair a phone", theme.Bright.B())
	if left <= 0 {
		c.Right(c.W-2, 0, "expired; esc, then p for a new code", theme.FgInk(theme.Red))
	} else {
		c.Right(c.W-2, 0, fmt.Sprintf("expires in %d:%02d", int(left.Minutes()), int(left.Seconds())%60), theme.Muted)
	}
	y := paragraph(c, 2, "Scan with the phone's camera, or open this link on it. The code works once.", theme.Text)
	for _, line := range breakAt(p.pairing.URL, c.W-2) {
		c.Put(1, y, line, theme.Bright)
		y++
	}
	if qrFits := y+1+len(p.pairing.QR) < c.H-1; qrFits {
		drawQR(c, y+1, p.pairing.QR)
	} else {
		paragraph(c, y+1, "Make this window taller to see the QR code.", theme.Muted)
	}
	c.Put(1, c.H-1, "esc back", theme.Text)
}

func drawQR(c *theme.Canvas, y int, qr []string) {
	width := 0
	for _, line := range qr {
		width = max(width, lipgloss.Width(line))
	}
	x := max((c.W-width)/2, 1)
	for i, line := range qr {
		c.Put(x, y+i, line, theme.Bright)
	}
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
