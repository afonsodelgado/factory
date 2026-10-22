package dialog

import (
	"fmt"
	"sort"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/sahilm/fuzzy"

	"github.com/afonsodelgado/factory/internal/ui/theme"
)

type size struct {
	width  int
	height int
}

func (s *size) resize(msg tea.Msg) {
	if w, ok := msg.(tea.WindowSizeMsg); ok {
		s.width, s.height = w.Width, w.Height
	}
}

func view(c *theme.Canvas) tea.View {
	v := tea.NewView(c.Render())
	v.AltScreen = true
	return v
}

type Switch struct {
	Context SwitchContext
	size
	query  string
	cursor int
	result *KeyResult
}

func (s *Switch) Init() tea.Cmd { return nil }

func (s *Switch) Result() (any, bool) {
	if s.result == nil {
		return nil, false
	}
	return *s.result, true
}

func (s *Switch) rows() []SwitchRow {
	if strings.TrimSpace(s.query) == "" {
		rows := append([]SwitchRow(nil), s.Context.Rows...)
		sort.SliceStable(rows, func(i, j int) bool { return rows[i].Needs && !rows[j].Needs })
		return rows
	}
	targets := make([]string, len(s.Context.Rows))
	for i, r := range s.Context.Rows {
		targets[i] = r.Label + " " + r.Title
	}
	var rows []SwitchRow
	for _, match := range fuzzy.Find(s.query, targets) {
		rows = append(rows, s.Context.Rows[match.Index])
	}
	return rows
}

func (s *Switch) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	s.resize(msg)
	key, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return s, nil
	}
	rows := s.rows()
	switch key.String() {
	case "esc", "ctrl+c":
		return s, tea.Quit
	case "enter":
		if s.cursor < len(rows) {
			s.result = &KeyResult{Key: rows[s.cursor].Key}
		}
		return s, tea.Quit
	case "down", "ctrl+j", "ctrl+n":
		s.cursor = min(s.cursor+1, max(len(rows)-1, 0))
	case "up", "ctrl+k", "ctrl+p":
		s.cursor = max(s.cursor-1, 0)
	case "ctrl+g":
		for _, r := range rows {
			if r.Needs {
				s.result = &KeyResult{Key: r.Key}
				return s, tea.Quit
			}
		}
	case "backspace":
		s.query = dropLast(s.query)
		s.cursor = 0
	default:
		s.query += key.Text
		s.cursor = 0
	}
	return s, nil
}

func (s *Switch) View() tea.View {
	c := theme.NewCanvas(s.width, s.height)
	rows := s.rows()
	c.Put(1, 0, "⌕ "+s.query+"█", theme.Bright)
	c.Right(c.W-2, 0, fmt.Sprintf("%d/%d", len(rows), len(s.Context.Rows)), theme.Muted)
	c.HLine(0, 1, c.W, theme.Line)
	visible := c.H - 4
	start := max(0, s.cursor-visible+1)
	for i := 0; i < visible && start+i < len(rows); i++ {
		r := rows[start+i]
		y := 2 + i
		if start+i == s.cursor {
			c.Fill(0, y, c.W, 1, theme.Selection)
			c.Put(0, y, "❯", theme.Focused.B())
		}
		c.PutMax(2, y, r.Label, theme.Bright.B(), 18)
		c.PutMax(21, y, r.Title, theme.Text, max(c.W-21-40, 10))
		c.PutMax(c.W-38, y, r.Repo, theme.Quiet, 8)
		c.PutMax(c.W-29, y, r.Stage, theme.Quiet, 14)
		ink := theme.Text
		if r.Needs {
			ink = theme.FgInk(theme.Orange)
		}
		c.Right(c.W-2, y, r.Status, ink)
	}
	c.Put(1, c.H-2, "Matches ticket, slug and title. Factories that need you sort first when the query is empty.", theme.Muted)
	c.Put(1, c.H-1, "⏎ select   ctrl+j/k move   ctrl+g next needing you   esc close", theme.Text)
	return view(c)
}

type Kill struct {
	Context KillContext
	size
	result *KeyResult
}

func (k *Kill) Init() tea.Cmd { return nil }

func (k *Kill) Result() (any, bool) {
	if k.result == nil {
		return nil, false
	}
	return *k.result, true
}

func (k *Kill) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	k.resize(msg)
	if key, ok := msg.(tea.KeyPressMsg); ok {
		switch key.String() {
		case "y":
			k.result = &KeyResult{Key: k.Context.Key}
			return k, tea.Quit
		case "n", "esc", "ctrl+c", "enter":
			return k, tea.Quit
		}
	}
	return k, nil
}

func (k *Kill) View() tea.View {
	c := theme.NewCanvas(k.width, k.height)
	name := k.Context.Label
	if k.Context.Title != "" {
		name += " · " + strings.TrimRight(k.Context.Title, ".")
	}
	y := 0
	for _, l := range theme.Wrap("Stops Claude Code and closes the pane for "+name+". The worktree, its branch and "+k.Context.FactoryDir+" stay. Wake brings it back later.", c.W-2) {
		c.Put(1, y, l, theme.Text)
		y++
	}
	line := []theme.Span{{Text: "y", Ink: theme.Key}, {Text: "  kill " + k.Context.Label + "       ", Ink: theme.Text}, {Text: "n", Ink: theme.Key}, {Text: "  cancel, the default", Ink: theme.Text}}
	c.Spans(max((c.W-theme.SpansWidth(line))/2, 1), c.H-1, line)
	return view(c)
}

type Archive struct {
	Context ArchiveContext
	size
	step   int
	result *ArchiveResult
}

func (a *Archive) Init() tea.Cmd { return nil }

func (a *Archive) Result() (any, bool) {
	if a.result == nil {
		return nil, false
	}
	return *a.result, true
}

func (a *Archive) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	a.resize(msg)
	key, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return a, nil
	}
	switch {
	case key.String() == "esc" || key.String() == "ctrl+c":
		return a, tea.Quit
	case a.step == 0 && key.String() == "a":
		a.result = &ArchiveResult{Key: a.Context.Key}
		return a, tea.Quit
	case a.step == 0 && key.String() == "r":
		a.step = 1
	case a.step == 1 && a.Context.Removable && key.String() == "y":
		a.result = &ArchiveResult{Key: a.Context.Key, Remove: true}
		return a, tea.Quit
	case a.step == 1 && (key.String() == "n" || key.String() == "a" || key.String() == "enter"):
		a.result = &ArchiveResult{Key: a.Context.Key}
		return a, tea.Quit
	}
	return a, nil
}

func (a *Archive) View() tea.View {
	c := theme.NewCanvas(a.width, a.height)
	ctx := a.Context
	switch {
	case a.step == 0:
		c.Put(1, 0, "Hides the row. H shows archived factories again.", theme.Text)
		c.PutMax(1, 1, "Branches and "+ctx.FactoryDir+" are never deleted.", theme.Text, c.W-2)
		c.Spans(1, c.H-1, []theme.Span{{Text: "a", Ink: theme.Key}, {Text: " archive   ", Ink: theme.Text}, {Text: "r", Ink: theme.Key}, {Text: " archive and remove the worktree…   ", Ink: theme.Text}, {Text: "esc", Ink: theme.Key}, {Text: " cancel", Ink: theme.Text}})
	case ctx.Removable:
		c.Put(1, 0, "Remove the worktree too?", theme.Bright.B())
		c.PutMax(1, 1, "Deletes  "+ctx.Worktree, theme.Text, c.W-2)
		kept := ctx.FactoryDir
		if ctx.FactoryDirMovesTo != "" {
			kept = "the factory directory, moved to " + ctx.FactoryDirMovesTo
		}
		c.PutMax(1, 2, "Keeps    branch "+ctx.Branch+" and "+kept, theme.Text, c.W-2)
		c.Put(1, 3, "✓ no uncommitted changes    ✓ every commit is pushed to its upstream", theme.FgInk(theme.Green))
		c.Spans(1, c.H-1, []theme.Span{{Text: "y", Ink: theme.Key}, {Text: "  remove " + base(ctx.Worktree) + "      ", Ink: theme.Text}, {Text: "n", Ink: theme.Key}, {Text: "  archive only, the default", Ink: theme.Text}})
	default:
		a.drawRefused(c)
	}
	return view(c)
}

func base(path string) string {
	return path[strings.LastIndex(path, "/")+1:]
}

func (a *Archive) drawRefused(c *theme.Canvas) {
	ctx := a.Context
	c.Put(1, 0, "Can't remove the worktree", theme.FgInk(theme.Red).B())
	c.PutMax(1, 1, ctx.Worktree, theme.Text, c.W-2)
	c.Put(1, 2, "It has work that isn't safe to delete:", theme.Text)
	y := 3
	label := func(name string, items []string) {
		for i, item := range items {
			if y >= c.H-2 {
				return
			}
			if i == 0 {
				c.Put(1, y, name, theme.FgInk(theme.Yellow))
			}
			c.PutMax(14, y, item, theme.Text, c.W-16)
			y++
		}
	}
	label("uncommitted", ctx.Dirty)
	label("not pushed", ctx.Unpushed)
	if ctx.NoUpstream {
		label("no upstream", []string{ctx.Branch + " has never been pushed"})
	}
	if ctx.Problem != "" {
		label("problem", []string{ctx.Problem})
	}
	advice := "Nothing was deleted."
	if len(ctx.Dirty) > 0 || len(ctx.Unpushed) > 0 || ctx.NoUpstream {
		advice = "Commit and push it, or keep the worktree. Nothing was deleted."
	}
	c.Put(1, min(y+1, c.H-2), advice, theme.Muted)
	c.Spans(1, c.H-1, []theme.Span{{Text: "a", Ink: theme.Key}, {Text: " archive without removing      ", Ink: theme.Text}, {Text: "esc", Ink: theme.Key}, {Text: " cancel", Ink: theme.Text}})
}

type Keys struct {
	size
}

func (k *Keys) Init() tea.Cmd { return nil }

func (k *Keys) Result() (any, bool) { return nil, false }

func (k *Keys) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	k.resize(msg)
	if _, ok := msg.(tea.KeyPressMsg); ok {
		return k, tea.Quit
	}
	return k, nil
}

var keyColumns = [][][2]string{
	{{"+Factories", ""}, {"j/k", "move"}, {"1-9", "jump to row"}, {"/", "fuzzy find"}, {"g", "next needing you"}, {"⏎", "focus the session"}, {"i", "answer in the panel"}},
	{{"+Lifecycle", ""}, {"n", "new factory"}, {"w", "wake selected"}, {"W", "wake all asleep"}, {"x", "kill (asks first)"}, {"a", "archive (asks first)"}, {"H", "show archived"}},
	{{"+Panel", ""}, {"tab", "next tab"}, {"[ ]", "prev or next item"}, {"o", "open in browser"}, {"z", "fold or unfold"}, {"+/-", "taller or shorter"}, {"t", "all factories (Activity)"}},
	{{"+Anywhere", ""}, {"M-0", "back to factories"}, {"esc", "back to factories"}, {"q", "quit the UI only"}, {"?", "these keys"}},
}

func (k *Keys) View() tea.View {
	c := theme.NewCanvas(k.width, k.height)
	col := max((c.W-2)/len(keyColumns), 20)
	for i, column := range keyColumns {
		x := 1 + i*col
		for y, row := range column {
			if row[1] == "" {
				c.Put(x, y, row[0], theme.FgInk(theme.Magenta).B())
				continue
			}
			c.Put(x, y, fmt.Sprintf("%-3s", row[0]), theme.Key)
			c.PutMax(x+4, y, "➜ "+row[1], theme.Text, col-5)
		}
	}
	c.Put(1, c.H-1, "any key closes", theme.Muted)
	return view(c)
}
