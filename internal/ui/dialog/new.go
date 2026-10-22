package dialog

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/rivo/uniseg"

	"github.com/afonsodelgado/factory/internal/factory"
	"github.com/afonsodelgado/factory/internal/ui/theme"
)

type NewFactoryModel struct {
	Context NewContext
	repo    int
	name    string
	brief   string
	step    int
	problem string
	result  *NewResult
	width   int
	height  int
}

func NewFactory(c NewContext) *NewFactoryModel {
	return &NewFactoryModel{Context: c, repo: max(0, min(c.Repo, len(c.Repos)-1)), step: 1}
}

func (m *NewFactoryModel) Init() tea.Cmd { return nil }

func (m *NewFactoryModel) Result() (any, bool) {
	if m.result == nil {
		return nil, false
	}
	return *m.result, true
}

func (m *NewFactoryModel) selectedRepo() RepoOption {
	if len(m.Context.Repos) == 0 {
		return RepoOption{}
	}
	return m.Context.Repos[m.repo]
}

func (m *NewFactoryModel) parsed() (ticket, feature, slug string) {
	ticket, feature = factory.ParseName(m.name)
	return ticket, feature, factory.SlugFor(ticket, feature)
}

func (m *NewFactoryModel) existing() (Existing, bool) {
	ticket, _, slug := m.parsed()
	for _, e := range m.selectedRepo().Existing {
		if ticket != "" && factory.MatchTicket(e.Worktree, ticket) {
			return e, true
		}
		if ticket == "" && slug != "" && e.Worktree == slug {
			return e, true
		}
	}
	return Existing{}, false
}

func (m *NewFactoryModel) nearMiss() (Existing, bool) {
	ticket, _, _ := m.parsed()
	if ticket == "" {
		return Existing{}, false
	}
	for _, e := range m.selectedRepo().Existing {
		if strings.HasPrefix(e.Worktree, strings.ToLower(ticket)) {
			return e, true
		}
	}
	return Existing{}, false
}

func (m *NewFactoryModel) launch() factory.Launch {
	ticket, _, slug := m.parsed()
	repo := m.selectedRepo()
	return factory.Launch{Repo: repo.Path, Slug: slug, Ticket: ticket, SessionID: "<uuid>", Chrome: repo.Browser, RemoteControl: m.Context.RemoteControl}
}

func (m *NewFactoryModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case tea.PasteMsg:
		m.typeText(msg.Content)
	case tea.KeyPressMsg:
		if m.step == 1 {
			return m, m.nameKey(msg)
		}
		return m, m.briefKey(msg)
	}
	return m, nil
}

func (m *NewFactoryModel) typeText(text string) {
	if m.step == 1 {
		m.name += strings.ReplaceAll(text, "\n", " ")
		return
	}
	m.brief += text
}

func (m *NewFactoryModel) nameKey(msg tea.KeyPressMsg) tea.Cmd {
	m.problem = ""
	switch msg.String() {
	case "esc", "ctrl+c":
		return tea.Quit
	case "up":
		m.repo = max(m.repo-1, 0)
	case "down", "tab":
		m.repo = (m.repo + 1) % max(len(m.Context.Repos), 1)
	case "backspace":
		m.name = dropLast(m.name)
	case "ctrl+u":
		m.name = ""
	case "ctrl+w":
		if e, ok := m.existing(); ok {
			m.result = &NewResult{Select: e.Key, Wake: true}
			return tea.Quit
		}
	case "enter":
		return m.next()
	default:
		m.name += msg.Text
	}
	return nil
}

func (m *NewFactoryModel) next() tea.Cmd {
	ticket, feature, slug := m.parsed()
	if slug == "" {
		m.problem = "Type a ticket id, such as ENG-1234, or a feature name."
		return nil
	}
	if e, ok := m.existing(); ok {
		m.result = &NewResult{Select: e.Key}
		return tea.Quit
	}
	if ticket != "" {
		m.result = &NewResult{Repo: m.selectedRepo().Path, Ticket: ticket, Feature: feature, Slug: slug}
		return tea.Quit
	}
	m.step = 2
	return nil
}

func (m *NewFactoryModel) briefKey(msg tea.KeyPressMsg) tea.Cmd {
	m.problem = ""
	switch msg.String() {
	case "esc":
		m.step = 1
	case "ctrl+c":
		return tea.Quit
	case "ctrl+s":
		if strings.TrimSpace(m.brief) == "" {
			m.problem = "The brief is empty. Write at least a sentence about what the feature should do; the factory won't start without one."
			return nil
		}
		_, feature, slug := m.parsed()
		m.result = &NewResult{Repo: m.selectedRepo().Path, Feature: feature, Slug: slug, Brief: m.brief}
		return tea.Quit
	case "enter":
		m.brief += "\n"
	case "backspace":
		m.brief = dropLast(m.brief)
	case "ctrl+u":
		m.brief = ""
	default:
		m.brief += msg.Text
	}
	return nil
}

func dropLast(s string) string {
	g := uniseg.NewGraphemes(s)
	last := 0
	for g.Next() {
		from, _ := g.Positions()
		last = from
	}
	return s[:last]
}

func (m *NewFactoryModel) View() tea.View {
	c := theme.NewCanvas(m.width, m.height)
	if m.step == 1 {
		m.drawName(c)
	} else {
		m.drawBrief(c)
	}
	v := tea.NewView(c.Render())
	v.AltScreen = true
	return v
}

func (m *NewFactoryModel) drawName(c *theme.Canvas) {
	w := c.W - 2
	c.Put(1, 0, "Repo", theme.Bright.B())
	c.Right(c.W-2, 0, "1 of 2", theme.Muted)
	y := 1
	for i, r := range m.Context.Repos {
		mark, ink := "○ ", theme.Text
		if i == m.repo {
			c.Put(1, y, "❯", theme.Focused.B())
			mark, ink = "● ", theme.Bright
		}
		qa, rabbit := "terminal QA", "CodeRabbit off"
		if r.Browser {
			qa = "browser QA"
		}
		if r.CodeRabbit {
			rabbit = "CodeRabbit on"
		}
		x := c.Put(3, y, mark+r.Name, ink)
		x = c.PutMax(max(x+2, 14), y, r.Display, theme.Muted, 28)
		c.Put(max(x+2, 44), y, qa+" · "+rabbit, theme.Quiet)
		y++
	}
	y++
	c.Put(1, y, "Ticket or feature", theme.Bright.B())
	y++
	drawInput(c, 1, y, w, "", []string{m.name}, true)
	y += 3
	for _, l := range m.notice(w) {
		c.Spans(1, y, l)
		y++
	}
	if m.problem != "" {
		for _, l := range theme.Wrap("✗ "+m.problem, w) {
			c.Put(1, y, l, theme.FgInk(theme.Red))
			y++
		}
	}
	if _, matched := m.existing(); !matched && m.name != "" {
		y++
		m.drawPreview(c, 1, y, w)
	}
	hints := "⏎ next: brief   ↑↓ repo   esc cancel"
	ticket, _, _ := m.parsed()
	if _, matched := m.existing(); matched {
		hints = "⏎ select it   ctrl+w select and wake it   esc cancel"
	} else if ticket != "" {
		hints = "⏎ start factory   ↑↓ repo   esc cancel"
	}
	c.Put(1, c.H-1, hints, theme.Text)
}

func (m *NewFactoryModel) notice(w int) [][]theme.Span {
	ticket, _, _ := m.parsed()
	var spans [][]theme.Span
	add := func(text string, ink theme.Ink) {
		for _, l := range theme.Wrap(text, w) {
			spans = append(spans, []theme.Span{{Text: l, Ink: ink}})
		}
	}
	if e, ok := m.existing(); ok {
		add(fmt.Sprintf("%s already has a factory: %s in %s. No second factory is created.", e.Label, e.Status, e.Stage), theme.FgInk(theme.Yellow))
		return spans
	}
	switch {
	case m.name == "":
		add("A ticket id such as ENG-1234, optionally with /feature-name, or a feature name.", theme.Muted)
	case ticket == "":
		add("Not a ticket id, so this is a feature. You'll write its brief next.", theme.Muted)
	default:
		if e, ok := m.nearMiss(); ok {
			add(fmt.Sprintf("%s is a new ticket. %s belongs to %s and won't be reused: tickets match exactly.", ticket, e.Worktree, e.Label), theme.Muted)
		} else {
			add(ticket+" is a new ticket.", theme.Muted)
		}
	}
	return spans
}

func (m *NewFactoryModel) drawPreview(c *theme.Canvas, x, y, w int) {
	repo := m.selectedRepo()
	c.Put(x, y, "Will run, from "+repo.Display, theme.Quiet)
	y++
	for _, l := range theme.Wrap(strings.SplitN(m.launch().CreateCommand(), "; cd ", 2)[0], w-4) {
		c.Put(x+2, y, l, theme.Text)
		y++
	}
	reason := "no --chrome: the " + repo.Name + " profile uses terminal QA."
	if repo.Browser {
		reason = "--chrome because the " + repo.Name + " profile uses browser QA."
	}
	c.Put(x, y, reason, theme.Muted)
}

func (m *NewFactoryModel) drawBrief(c *theme.Canvas) {
	w := c.W - 2
	_, _, slug := m.parsed()
	c.Put(1, 0, "New factory · "+slug, theme.Bright.B())
	c.Right(c.W-2, 0, "2 of 2", theme.Muted)
	y := 2
	for _, l := range theme.Wrap("Describe the feature: what it should do, for whom, and anything you already know you want. The supervisor writes this to brief.md word for word and won't ask for it again.", w) {
		c.Put(1, y, l, theme.Text)
		y++
	}
	y++
	var lines []string
	for i, raw := range strings.Split(m.brief, "\n") {
		for j, l := range theme.Wrap(raw, w-8) {
			number := "   "
			if j == 0 {
				number = fmt.Sprintf("%2d ", i+1)
			}
			lines = append(lines, number+l)
		}
	}
	room := c.H - y - 5
	if len(lines) > room && room > 0 {
		lines = lines[len(lines)-room:]
	}
	drawInput(c, 1, y, w, "brief", lines, true)
	y += len(lines) + 2
	if m.problem != "" {
		for _, l := range theme.Wrap("✗ "+m.problem, w) {
			y++
			c.Put(1, y, l, theme.FgInk(theme.Red))
		}
	}
	count := fmt.Sprintf("%d lines · %d characters", strings.Count(m.brief, "\n")+1, uniseg.GraphemeClusterCount(m.brief))
	c.Put(1, c.H-1, count, theme.Muted)
	c.Right(c.W-2, c.H-1, "ctrl+s start factory   esc back", theme.Text)
}

func drawInput(c *theme.Canvas, x, y, w int, title string, lines []string, focused bool) {
	border := theme.Line
	if focused {
		border = theme.Focused
	}
	head := ""
	if title != "" {
		head = "─ " + title + " "
	}
	c.Put(x, y, "╭"+head+strings.Repeat("─", max(w-2-theme.Width(head), 0))+"╮", border)
	for i, l := range lines {
		if i == len(lines)-1 && focused {
			l += "█"
		}
		c.Put(x, y+1+i, "│ ", border)
		c.PutMax(x+2, y+1+i, l, theme.Bright, w-4)
		c.Put(x+w-1, y+1+i, "│", border)
	}
	c.Put(x, y+1+len(lines), "╰"+strings.Repeat("─", max(w-2, 0))+"╯", border)
}
