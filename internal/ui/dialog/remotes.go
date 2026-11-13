package dialog

import (
	"fmt"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/afonsodelgado/factory/internal/remote"
	"github.com/afonsodelgado/factory/internal/state"
	"github.com/afonsodelgado/factory/internal/ui/theme"
)

type remotesScreen int

const (
	listScreen remotesScreen = iota
	addScreen
	checkingScreen
	checkedScreen
	pushScreen
)

const (
	nameField = iota
	targetField
	identityField
	fieldCount
)

type checkDone struct {
	run  int
	rows []CheckRow
}

type Remotes struct {
	Context RemotesContext
	actions RemoteActions
	size
	screen   remotesScreen
	cursor   int
	removing bool
	problem  string

	field  int
	fields [fieldCount]string

	checked   string
	firstAdd  bool
	checkRun  int
	checkRows []CheckRow

	server        string
	topic         string
	editingServer bool
}

func NewRemotes(c RemotesContext, actions RemoteActions) *Remotes {
	return &Remotes{Context: c, actions: actions}
}

func (r *Remotes) Init() tea.Cmd { return nil }

func (r *Remotes) Result() (any, bool) { return nil, false }

func (r *Remotes) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	r.resize(msg)
	switch msg := msg.(type) {
	case checkDone:
		if msg.run == r.checkRun && r.screen == checkingScreen {
			r.checkRows, r.screen = msg.rows, checkedScreen
		}
	case tea.PasteMsg:
		r.typeText(strings.ReplaceAll(DecodeKeys(msg.Content), "\n", ""))
	case tea.KeyPressMsg:
		if msg.String() == "ctrl+c" {
			return r, tea.Quit
		}
		r.problem = ""
		return r, r.key(msg)
	}
	return r, nil
}

func (r *Remotes) key(msg tea.KeyPressMsg) tea.Cmd {
	switch r.screen {
	case addScreen:
		return r.addKey(msg)
	case checkingScreen:
		if msg.String() == "esc" {
			r.checkRun++
			r.screen = listScreen
		}
		return nil
	case checkedScreen:
		r.checkedKey(msg)
		return nil
	case pushScreen:
		r.pushKey(msg)
		return nil
	}
	return r.listKey(msg)
}

func (r *Remotes) typeText(text string) {
	switch {
	case r.screen == addScreen:
		r.fields[r.field] += text
	case r.screen == pushScreen && r.editingServer:
		r.server += text
	}
}

func (r *Remotes) selected() (RemoteRow, bool) {
	if r.cursor < len(r.Context.Remotes) {
		return r.Context.Remotes[r.cursor], true
	}
	return RemoteRow{}, false
}

func (r *Remotes) listKey(msg tea.KeyPressMsg) tea.Cmd {
	if r.removing {
		r.removing = false
		if msg.String() == "y" {
			r.remove()
		}
		return nil
	}
	if msg.String() == "esc" {
		return tea.Quit
	}
	if msg.String() == "a" {
		r.openAdd()
		return nil
	}
	if len(r.Context.Remotes) == 0 {
		return nil
	}
	row, onRemote := r.selected()
	switch msg.String() {
	case "up", "k":
		r.cursor = max(r.cursor-1, 0)
	case "down", "j":
		r.cursor = min(r.cursor+1, len(r.Context.Remotes))
	case "enter":
		r.makeDefault(row.Name)
	case "p":
		r.openPush()
	case "c":
		if onRemote {
			return r.check(row.Name, false)
		}
	case "x":
		r.removing = onRemote
	}
	return nil
}

func (r *Remotes) makeDefault(name string) bool {
	if err := r.actions.SetDefault(name); err != nil {
		r.problem = err.Error()
		return false
	}
	r.Context.Default = name
	return true
}

func (r *Remotes) remove() {
	row, _ := r.selected()
	if err := r.actions.Remove(row.Name); err != nil {
		r.problem = err.Error()
		return
	}
	r.Context.Remotes = slices.Delete(r.Context.Remotes, r.cursor, r.cursor+1)
	if r.Context.Default == row.Name {
		r.Context.Default = ""
	}
	r.cursor = min(r.cursor, max(len(r.Context.Remotes)-1, 0))
}

func (r *Remotes) openAdd() {
	r.fields, r.field, r.screen = [fieldCount]string{}, nameField, addScreen
}

func (r *Remotes) addKey(msg tea.KeyPressMsg) tea.Cmd {
	field := &r.fields[r.field]
	switch msg.String() {
	case "esc":
		r.screen = listScreen
	case "tab":
		r.field = (r.field + 1) % fieldCount
	case "shift+tab":
		r.field = (r.field + fieldCount - 1) % fieldCount
	case "ctrl+i":
		r.field = identityField
	case "enter":
		return r.add()
	case "backspace":
		*field = dropLast(*field)
	case "ctrl+u":
		*field = ""
	default:
		*field += DecodeKeys(msg.Text)
	}
	return nil
}

func (r *Remotes) add() tea.Cmd {
	name, target := strings.TrimSpace(r.fields[nameField]), strings.TrimSpace(r.fields[targetField])
	if err := r.actions.Add(name, target, strings.TrimSpace(r.fields[identityField])); err != nil {
		r.problem = err.Error()
		return nil
	}
	r.Context.Remotes = append(r.Context.Remotes, RemoteRow{Name: name, Endpoint: target, Link: "connecting"})
	r.cursor = len(r.Context.Remotes) - 1
	return r.check(name, true)
}

func (r *Remotes) check(name string, firstAdd bool) tea.Cmd {
	r.checked, r.firstAdd, r.checkRows, r.screen = name, firstAdd, nil, checkingScreen
	r.checkRun++
	run, actions := r.checkRun, r.actions
	return func() tea.Msg { return checkDone{run: run, rows: actions.Check(name)} }
}

func (r *Remotes) checkedKey(msg tea.KeyPressMsg) {
	switch msg.String() {
	case "enter", "esc":
		r.screen = listScreen
	case "d":
		if r.Context.Default != r.checked && r.makeDefault(r.checked) {
			r.screen = listScreen
		}
	}
}

func (r *Remotes) openPush() {
	r.server, r.editingServer, r.screen = r.Context.Push.ServerURL(), false, pushScreen
	if r.Context.Push.On() {
		r.topic = r.Context.Push.Topic
	}
	if r.topic == "" {
		r.topic = r.actions.NewTopic()
	}
}

func (r *Remotes) pushKey(msg tea.KeyPressMsg) {
	key := msg.String()
	if r.editingServer {
		switch key {
		case "tab", "esc":
			r.editingServer = false
		case "enter":
			r.togglePush()
		case "backspace":
			r.server = dropLast(r.server)
		case "ctrl+u":
			r.server = ""
		default:
			r.server += DecodeKeys(msg.Text)
		}
		return
	}
	pushOff := !r.Context.Push.On()
	switch {
	case key == "esc":
		r.screen = listScreen
	case key == "enter":
		r.togglePush()
	case key == "tab" && pushOff:
		r.editingServer = true
	case key == "r" && pushOff:
		r.topic = r.actions.NewTopic()
	}
}

func (r *Remotes) togglePush() {
	push := state.Push{}
	if !r.Context.Push.On() {
		push.Topic = r.topic
		if server := strings.TrimSpace(r.server); server != state.DefaultPushServer {
			push.Server = server
		}
	}
	if err := r.actions.SetPush(push); err != nil {
		r.problem = err.Error()
		return
	}
	r.Context.Push, r.editingServer = push, false
}

func (r *Remotes) View() tea.View {
	c := theme.NewCanvas(r.width, r.height)
	switch {
	case r.screen == addScreen:
		r.drawAdd(c)
	case r.screen == checkingScreen || r.screen == checkedScreen:
		r.drawCheck(c)
	case r.screen == pushScreen:
		r.drawPush(c)
	case len(r.Context.Remotes) == 0:
		r.drawEmpty(c)
	default:
		r.drawList(c)
	}
	return view(c)
}

func paragraph(c *theme.Canvas, y int, text string, ink theme.Ink) int {
	for _, l := range theme.Wrap(text, c.W-2) {
		c.Put(1, y, l, ink)
		y++
	}
	return y
}

func (r *Remotes) drawProblem(c *theme.Canvas, y int) {
	if r.problem != "" {
		paragraph(c, y, "✗ "+r.problem, theme.FgInk(theme.Red))
	}
}

func (r *Remotes) drawEmpty(c *theme.Canvas) {
	c.Put(1, 0, "Remotes", theme.Bright.B())
	c.Put(1, 2, "No remotes. Every factory runs on this machine.", theme.Text)
	y := paragraph(c, 4, "A remote is another machine you reach over ssh, such as a home server that stays on. Factories you start there keep running while this laptop sleeps. You still review, answer, open links and get notified here.", theme.Text)
	c.Spans(1, y+1, []theme.Span{{Text: "a", Ink: theme.Key}, {Text: "  add a remote", Ink: theme.Text}})
	c.Spans(1, y+3, []theme.Span{{Text: "From a shell:  ", Ink: theme.Muted}, {Text: "factory remote add <name> <user@host>", Ink: theme.Bright}})
	r.drawProblem(c, y+5)
	c.Put(1, c.H-1, "a add a remote   esc close", theme.Text)
}

func (r *Remotes) drawAdd(c *theme.Canvas) {
	c.Put(1, 0, "Add a remote", theme.Bright.B())
	c.Right(c.W-2, 0, "1 of 2", theme.Muted)
	y := 2
	input := func(label string, field int) {
		c.Put(1, y, label, theme.Text)
		text := r.fields[field]
		if r.field == field {
			text += "█"
		}
		drawInput(c, 1, y+1, c.W-2, "", []string{text}, r.field == field, theme.Focused)
		y += 4
	}
	input("Name, as the hub shows it", nameField)
	input("SSH target, as you would type it after ssh", targetField)
	if r.field == identityField || r.fields[identityField] != "" {
		input("Identity file, instead of ssh-agent and ~/.ssh/config", identityField)
	}
	y = paragraph(c, y, "Keys, port and identity come from ssh-agent and ~/.ssh/config, as for plain ssh. user@host:2222 sets a port. ctrl+i picks an identity file.", theme.Muted)
	name := strings.TrimSpace(r.fields[nameField])
	who := name
	if who == "" {
		who = "it"
	}
	y = paragraph(c, y+1, "A Tailscale MagicDNS name or tailnet IP reaches "+who+" from anywhere.", theme.Muted)
	r.drawProblem(c, y+1)
	check := "⏎ check"
	if name != "" {
		check += " " + name
	}
	c.Put(1, c.H-1, check+"   tab next field   ctrl+i identity   esc back", theme.Text)
}

var markGlyphs = map[string]struct {
	glyph string
	ink   theme.Ink
}{
	"ok":   {"✓", theme.FgInk(theme.Green)},
	"fail": {"✗", theme.FgInk(theme.Red)},
	"skip": {"–", theme.Muted},
	"warn": {"!", theme.FgInk(theme.Yellow)},
}

const (
	checkNameX   = 5
	checkDetailX = 18
	checkFixX    = 23
)

func (r *Remotes) drawCheck(c *theme.Canvas) {
	title := "Checked " + r.checked
	if r.screen == checkingScreen {
		title = "Checking " + r.checked + " over ssh"
	}
	c.Put(1, 0, title, theme.Bright.B())
	if r.firstAdd {
		c.Right(c.W-2, 0, "2 of 2", theme.Muted)
	}
	var y int
	if r.screen == checkingScreen {
		y = r.drawPendingChecks(c)
		paragraph(c, y+1, "factory doctor --remote "+r.checked+"'s checks, before push is set up. Nothing is written.", theme.Muted)
		c.Put(1, c.H-1, "esc stop", theme.Text)
		return
	}
	y = r.drawCheckRows(c)
	y = paragraph(c, y+1, r.checkSummary(), theme.Text)
	r.drawProblem(c, y+1)
	c.Put(1, c.H-1, r.checkedHints(), theme.Text)
}

func (r *Remotes) drawPendingChecks(c *theme.Canvas) int {
	c.Put(3, 2, "◌", theme.FgInk(theme.Yellow))
	c.Put(checkNameX, 2, "ssh", theme.Text)
	c.Put(checkDetailX, 2, "checking…", theme.Muted)
	y := 3
	for _, name := range remote.ServerChecks {
		c.Put(3, y, "·", theme.Muted)
		c.Put(checkNameX, y, name, theme.Muted)
		y++
	}
	return y
}

func (r *Remotes) drawCheckRows(c *theme.Canvas) int {
	y := 2
	for _, row := range r.checkRows {
		mark := markGlyphs[row.Mark]
		detailInk := theme.Text
		if row.Mark == "skip" {
			detailInk = theme.Muted
		}
		c.Put(3, y, mark.glyph, mark.ink)
		c.PutMax(checkNameX, y, row.Name, theme.Text, checkDetailX-checkNameX-1)
		c.PutMax(checkDetailX, y, row.Detail, detailInk, c.W-checkDetailX-1)
		y++
		if row.Fix != "" && row.Mark != "ok" {
			c.Put(checkDetailX, y, "fix", theme.Muted)
			c.PutMax(checkFixX, y, row.Fix, theme.Bright, c.W-checkFixX-1)
			y++
		}
	}
	return y
}

func (r *Remotes) checkSummary() string {
	problems := 0
	for _, row := range r.checkRows {
		if row.Mark == "fail" {
			problems++
		}
	}
	switch {
	case problems == 0:
		return "Every check passes. Factories can start on " + r.checked + "."
	case r.firstAdd:
		return fmt.Sprintf("%s. %s is added either way; factories won't start there until %s.", plural(problems, "problem"), r.checked, passes(problems))
	}
	return fmt.Sprintf("%s. Factories won't start on %s until %s.", plural(problems, "problem"), r.checked, passes(problems))
}

func passes(problems int) string {
	if problems == 1 {
		return "it passes"
	}
	return "they pass"
}

func (r *Remotes) checkedHints() string {
	isDefault := r.Context.Default == r.checked
	switch {
	case r.firstAdd && isDefault:
		return "⏎ add " + r.checked + "   esc back"
	case r.firstAdd:
		return "⏎ add " + r.checked + "   d add it as the default for new factories   esc back"
	case isDefault:
		return "⏎ back   esc back"
	}
	return "⏎ back   d make it the default for new factories   esc back"
}

const (
	whereX     = 3
	targetX    = 18
	linkX      = 40
	factoriesX = 57
	newOnesX   = 76
	valueX     = 14
)

func (r *Remotes) drawList(c *theme.Canvas) {
	connected := 0
	for _, row := range r.Context.Remotes {
		if row.Link == "connected" {
			connected++
		}
	}
	c.Put(1, 0, "Remotes", theme.Bright.B())
	c.Right(c.W-2, 0, fmt.Sprintf("%d connected", connected), theme.Muted)
	for x, heading := range map[int]string{whereX: "where", targetX: "ssh target", linkX: "link", factoriesX: "factories", newOnesX: "new ones"} {
		c.Put(x, 2, heading, theme.Muted)
	}
	y := 3
	for i, row := range r.Context.Remotes {
		r.drawRemoteRow(c, y, i, row)
		y++
	}
	r.drawLocalRow(c, y)
	y = paragraph(c, y+2, r.whereNewFactoriesRun(), theme.Text)
	y = r.drawPushSummary(c, y+1)
	if r.removing {
		row, _ := r.selected()
		paragraph(c, c.H-4, "Removing "+row.Name+" stops this laptop connecting to it. Its factories keep running there and it keeps sending push.", theme.FgInk(theme.Yellow))
		c.Spans(1, c.H-1, []theme.Span{{Text: "y", Ink: theme.Key}, {Text: " remove   ", Ink: theme.Text}, {Text: "n", Ink: theme.Key}, {Text: " keep", Ink: theme.Text}})
		return
	}
	r.drawProblem(c, y+1)
	c.Put(1, c.H-1, "⏎ make default   a add   p push   c doctor   x remove   esc close", theme.Text)
}

func (r *Remotes) drawCursor(c *theme.Canvas, y, i int) {
	if i == r.cursor {
		c.Fill(0, y, c.W, 1, theme.Selection)
		c.Put(1, y, "❯", theme.Focused.B())
	}
}

func (r *Remotes) drawRemoteRow(c *theme.Canvas, y, i int, row RemoteRow) {
	r.drawCursor(c, y, i)
	c.PutMax(whereX, y, row.Name, theme.Bright, targetX-whereX-1)
	c.PutMax(targetX, y, row.Endpoint, theme.Text, linkX-targetX-1)
	link, ink := linkLabel(row.Link)
	c.Put(linkX, y, link, ink)
	x := c.Put(factoriesX, y, fmt.Sprint(row.Factories), theme.Text)
	if row.NeedsYou > 0 {
		c.Put(x, y, fmt.Sprintf(" · %d needs you", row.NeedsYou), theme.FgInk(theme.Orange))
	}
	r.drawDefaultMark(c, y, row.Name)
}

func (r *Remotes) drawLocalRow(c *theme.Canvas, y int) {
	r.drawCursor(c, y, len(r.Context.Remotes))
	c.Put(whereX, y, "this machine", theme.Bright)
	c.Put(targetX, y, "–", theme.Muted)
	c.Put(linkX, y, "–", theme.Muted)
	c.Put(factoriesX, y, fmt.Sprint(r.Context.Local), theme.Text)
	r.drawDefaultMark(c, y, "")
}

func (r *Remotes) drawDefaultMark(c *theme.Canvas, y int, name string) {
	if r.Context.Default == name {
		c.Put(newOnesX, y, "● default", theme.FgInk(theme.Green))
		return
	}
	c.Put(newOnesX, y, "○", theme.Muted)
}

func linkLabel(link string) (string, theme.Ink) {
	switch link {
	case "connected":
		return "↔ connected", theme.FgInk(theme.Green)
	case "connecting":
		return "↔ connecting…", theme.FgInk(theme.Yellow)
	case "unreachable":
		return "↮ unreachable", theme.FgInk(theme.Red)
	}
	return "–", theme.Muted
}

func (r *Remotes) whereNewFactoriesRun() string {
	if r.Context.Default == "" {
		return "New factories run on this machine."
	}
	return "New factories run on " + r.Context.Default + ". Pick this machine in New factory for a one-off."
}

func (r *Remotes) exampleRemote() string {
	if r.Context.Default != "" {
		return r.Context.Default
	}
	if len(r.Context.Remotes) > 0 {
		return r.Context.Remotes[0].Name
	}
	return "homelab"
}

func (r *Remotes) examplePush() string {
	return "csv-export on " + r.exampleRemote() + ": question"
}

func valueWithNote(c *theme.Canvas, x, y int, value, note string) {
	end := c.Put(x, y, value, theme.Bright)
	if end+3+theme.Width(note) <= c.W-1 {
		c.Put(end+3, y, note, theme.Muted)
	}
}

func (r *Remotes) drawPushSummary(c *theme.Canvas, y int) int {
	c.Put(1, y, "Phone push", theme.Bright.B())
	push := r.Context.Push
	if !push.On() {
		c.Put(valueX, y, "off", theme.Muted)
		c.PutMax(3, y+1, "p sets it up, so a remote reaches your phone while this laptop sleeps.", theme.Muted, c.W-4)
		return y + 2
	}
	c.Put(valueX, y, "on", theme.FgInk(theme.Green))
	rows := []struct{ label, value, note string }{
		{"server", push.ServerURL(), ""},
		{"topic", push.Topic, "private: anyone who has it can read them"},
		{"sends", r.examplePush(), "only the id, the remote and the kind"},
		{"sent by", "each remote itself, so pushes arrive while this laptop sleeps", ""},
	}
	for i, row := range rows {
		c.Put(3, y+1+i, row.label, theme.Text)
		valueWithNote(c, valueX, y+1+i, row.value, row.note)
	}
	return y + 1 + len(rows)
}

func (r *Remotes) drawPush(c *theme.Canvas) {
	on := r.Context.Push.On()
	c.Put(1, 0, "Phone push", theme.Bright.B())
	if on {
		c.Right(c.W-2, 0, "on", theme.FgInk(theme.Green))
	} else {
		c.Right(c.W-2, 0, "off", theme.Muted)
	}
	y := paragraph(c, 2, "When a factory on a remote needs you, that remote sends a push through ntfy, so it reaches your phone while this laptop is closed. You still answer in the hub.", theme.Text)
	c.Put(1, y+1, "Server", theme.Text)
	server := r.server
	if r.editingServer {
		server += "█"
	}
	drawInput(c, 1, y+2, c.W-2, "", []string{server}, r.editingServer, theme.Focused)
	y = paragraph(c, y+5, "ntfy.sh works anywhere. A self-hosted server needs a URL your phone can reach.", theme.Muted)
	topicNote := "generated and unguessable · r makes a new one"
	if on {
		topicNote = "private: anyone who has it can read them"
	}
	c.Put(1, y+1, "Topic", theme.Text)
	valueWithNote(c, 9, y+1, r.topic, topicNote)
	y = paragraph(c, y+2, "On your phone, open ntfy, tap +, and subscribe to this topic on the same server.", theme.Muted)
	c.Put(1, y+1, "Every push reads", theme.Text)
	c.Put(20, y+1, r.examplePush(), theme.Bright)
	c.Put(1, y+2, "Never prompt text, questions, answers or secrets.", theme.Muted)
	r.drawProblem(c, y+4)
	c.Put(1, c.H-1, r.pushHints(), theme.Text)
}

func (r *Remotes) pushHints() string {
	switch {
	case r.Context.Push.On():
		return "⏎ turn push off   esc back"
	case r.editingServer:
		return "⏎ turn push on   tab or esc done"
	}
	return "⏎ turn push on   tab edit server   r new topic   esc back"
}
