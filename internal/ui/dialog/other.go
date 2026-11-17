package dialog

import (
	"fmt"
	"maps"
	"slices"
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
	c.Put(1, c.H-2, "Matches id, worktree and title. Factories that need you sort first when the query is empty.", theme.Muted)
	c.Put(1, c.H-1, "⏎ select   ctrl+j/k move   ctrl+g next needing you   esc close", theme.Text)
	return view(c)
}

type Pause struct {
	Context PauseContext
	size
	result *KeyResult
}

func (p *Pause) Init() tea.Cmd { return nil }

func (p *Pause) Result() (any, bool) {
	if p.result == nil {
		return nil, false
	}
	return *p.result, true
}

func (p *Pause) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	p.resize(msg)
	if key, ok := msg.(tea.KeyPressMsg); ok {
		switch key.String() {
		case "y":
			p.result = &KeyResult{Key: p.Context.Key}
			return p, tea.Quit
		case "n", "esc", "ctrl+c", "enter":
			return p, tea.Quit
		}
	}
	return p, nil
}

func (p *Pause) View() tea.View {
	c := theme.NewCanvas(p.width, p.height)
	name := p.Context.Label
	if p.Context.Title != "" {
		name += " · " + strings.TrimRight(p.Context.Title, ".")
	}
	y := 0
	for _, l := range theme.Wrap("Stops Claude Code and closes the pane for "+name+". The worktree, its branch and "+p.Context.FactoryDir+" stay. Wake brings it back.", c.W-2) {
		c.Put(1, y, l, theme.Text)
		y++
	}
	line := []theme.Span{{Text: "y", Ink: theme.Key}, {Text: "  pause " + p.Context.Label + "       ", Ink: theme.Text}, {Text: "n", Ink: theme.Key}, {Text: "  cancel, the default", Ink: theme.Text}}
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
		c.Put(1, 1, "Branches and the factory directory are never deleted:", theme.Text)
		c.Put(3, 2, pathTail(ctx.FactoryDir, c.W-4), theme.Bright)
		c.Spans(1, c.H-1, []theme.Span{{Text: "a", Ink: theme.Key}, {Text: " archive   ", Ink: theme.Text}, {Text: "r", Ink: theme.Key}, {Text: " archive and remove the worktree…   ", Ink: theme.Text}, {Text: "esc", Ink: theme.Key}, {Text: " cancel", Ink: theme.Text}})
	case ctx.Removable:
		const indent = 10
		where, kept := "the factory directory, at", ctx.FactoryDir
		if ctx.FactoryDirMovesTo != "" {
			where, kept = "the factory directory, moved to", ctx.FactoryDirMovesTo
		}
		c.Put(1, 0, "Remove the worktree too?", theme.Bright.B())
		c.Put(1, 1, "Deletes", theme.Text)
		c.Put(indent, 1, pathTail(ctx.Worktree, c.W-indent-1), theme.Bright)
		c.Put(1, 2, "Keeps", theme.Text)
		c.Put(indent, 2, "branch "+ctx.Branch, theme.Text)
		c.Put(indent, 3, where, theme.Text)
		c.Put(indent, 4, pathTail(kept, c.W-indent-1), theme.Bright)
		c.Put(1, 6, "✓ no uncommitted changes    ✓ every commit is pushed to its upstream", theme.FgInk(theme.Green))
		c.Spans(1, c.H-1, []theme.Span{{Text: "y", Ink: theme.Key}, {Text: "  remove " + base(ctx.Worktree) + "      ", Ink: theme.Text}, {Text: "n", Ink: theme.Key}, {Text: "  archive only, the default", Ink: theme.Text}})
	default:
		a.drawRefused(c)
	}
	return view(c)
}

func base(path string) string {
	return path[strings.LastIndex(path, "/")+1:]
}

func pathTail(path string, width int) string {
	if theme.Width(path) <= width {
		return path
	}
	runes := []rune(path)
	for len(runes) > 0 && theme.Width(string(runes)) > width-1 {
		runes = runes[1:]
	}
	return "…" + string(runes)
}

func (a *Archive) drawRefused(c *theme.Canvas) {
	ctx := a.Context
	c.Put(1, 0, "Can't remove the worktree", theme.FgInk(theme.Red).B())
	c.Put(1, 1, pathTail(ctx.Worktree, c.W-2), theme.Text)
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

type Delete struct {
	Context DeleteContext
	size
	step   int
	result *DeleteResult
}

func (d *Delete) Init() tea.Cmd { return nil }

func (d *Delete) Result() (any, bool) {
	if d.result == nil {
		return nil, false
	}
	return *d.result, true
}

func (d *Delete) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	d.resize(msg)
	key, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return d, nil
	}
	confirmed := key.String() == "y" && !d.Context.Unreachable
	switch {
	case key.String() == "f" && d.Context.Remote != "":
		d.result = &DeleteResult{Key: d.Context.Key, Forget: true}
	case confirmed && d.step == 0 && d.Context.HasWork():
		d.step = 1
		return d, nil
	case confirmed:
		d.result = &DeleteResult{Key: d.Context.Key, Confirmed: true}
	}
	return d, tea.Quit
}

func (d *Delete) View() tea.View {
	c := theme.NewCanvas(d.width, d.height)
	switch {
	case d.Context.Unreachable:
		d.drawUnreachable(c)
	case d.step == 0:
		d.drawWhatGoes(c)
	default:
		d.drawWork(c)
	}
	return view(c)
}

func (d *Delete) drawUnreachable(c *theme.Canvas) {
	ctx := d.Context
	c.PutMax(1, 0, ctx.Remote+" is unreachable, so "+ctx.Label+" can't be deleted now", theme.Bright.B(), c.W-2)
	y := 2
	for _, text := range []string{
		"Nothing was deleted. Its panes, worktree and state stay on " + ctx.Remote + ", and it may still be running there. Delete it once " + ctx.Remote + " is back.",
		"",
		"Forget removes it from this hub only. " + ctx.Remote + " is not touched.",
		"factory open " + ctx.Label + " --remote " + ctx.Remote + " lists it again.",
	} {
		for _, line := range theme.Wrap(text, c.W-2) {
			c.Put(1, y, line, theme.Text)
			y++
		}
		if text == "" {
			y++
		}
	}
	line := []theme.Span{{Text: "f", Ink: theme.Key}, {Text: "  forget " + ctx.Label + "       ", Ink: theme.Text}, {Text: "n", Ink: theme.Key}, {Text: "  cancel, the default", Ink: theme.Text}}
	c.Spans(max((c.W-theme.SpansWidth(line))/2, 1), c.H-1, line)
}

func (d *Delete) drawWhatGoes(c *theme.Canvas) {
	ctx := d.Context
	name := ctx.Label
	if ctx.Title != "" {
		name += " · " + strings.TrimRight(ctx.Title, ".")
	}
	const indent = 12
	where := ""
	if ctx.Remote != "" {
		where = ", on " + ctx.Remote
	}
	c.PutMax(1, 0, "Deletes "+name+" for good"+where+":", theme.Bright.B(), c.W-2)
	worktree, worktreeInk := pathTail(ctx.Worktree, c.W-indent-1), theme.Bright
	if ctx.WorktreeGone || ctx.Worktree == "" {
		worktree, worktreeInk = "already removed", theme.Muted
	}
	branches, branchesInk := strings.Join(ctx.Branches, ", "), theme.Bright
	if len(ctx.Branches) == 0 {
		branches, branchesInk = "none left", theme.Muted
	}
	rows := []struct {
		name, value string
		ink         theme.Ink
	}{
		{"session", "its Claude and stage panes" + onRemote(ctx.Remote) + ", and what they started", theme.Text},
		{"worktree", worktree, worktreeInk},
		{"branches", branches, branchesInk},
		{"state", "its factory and QA directories" + onRemote(ctx.Remote) + ", logs, drafts and hook records", theme.Text},
	}
	for i, r := range rows {
		c.Put(3, 2+i, r.name, theme.Text)
		c.PutMax(indent+2, 2+i, r.value, r.ink, c.W-indent-3)
	}
	c.Put(1, 7, "Pushed PRs and remote branches stay. The id can be used again.", theme.Muted)
	y := 8
	if ctx.Remote != "" {
		c.Put(1, y, ctx.Remote+" keeps its clone of "+ctx.Repo+" for the next factory.", theme.Muted)
		y++
	}
	if ctx.HasWork() {
		c.Put(1, y, "It has unpushed or uncommitted work: y shows it and asks again.", theme.FgInk(theme.Yellow))
	}
	line := []theme.Span{{Text: "y", Ink: theme.Key}, {Text: "  delete " + ctx.Label + "       ", Ink: theme.Text}, {Text: "n", Ink: theme.Key}, {Text: "  cancel, the default", Ink: theme.Text}}
	if ctx.Remote != "" {
		line = []theme.Span{{Text: "y", Ink: theme.Key}, {Text: "  delete on " + ctx.Remote + "      ", Ink: theme.Text}, {Text: "f", Ink: theme.Key}, {Text: "  forget here only      ", Ink: theme.Text}, {Text: "n", Ink: theme.Key}, {Text: "  cancel, the default", Ink: theme.Text}}
	}
	c.Spans(max((c.W-theme.SpansWidth(line))/2, 1), c.H-1, line)
}

func onRemote(name string) string {
	if name == "" {
		return ""
	}
	return " on " + name
}

func (d *Delete) drawWork(c *theme.Canvas) {
	ctx := d.Context
	c.Put(1, 0, ctx.Label+" has work that exists nowhere else", theme.FgInk(theme.Red).B())
	y := 2
	label := func(name string, items []string) {
		for i, item := range items {
			if y >= c.H-3 {
				return
			}
			if i == 0 {
				c.Put(1, y, name, theme.FgInk(theme.Yellow))
			}
			c.PutMax(15, y, item, theme.Text, c.W-17)
			y++
		}
	}
	label("uncommitted", ctx.Dirty)
	branches := slices.Sorted(maps.Keys(ctx.Unpushed))
	for _, branch := range branches {
		var commits []string
		for _, commit := range ctx.Unpushed[branch] {
			commits = append(commits, branch+"  "+commit)
		}
		label("not pushed", commits)
	}
	c.Put(1, c.H-3, "Deleting loses it. Without a second y, nothing is deleted.", theme.Muted)
	line := []theme.Span{{Text: "y", Ink: theme.Key}, {Text: "  delete anyway       ", Ink: theme.Text}, {Text: "n", Ink: theme.Key}, {Text: "  keep everything, the default", Ink: theme.Text}}
	c.Spans(max((c.W-theme.SpansWidth(line))/2, 1), c.H-1, line)
}

type Keys struct {
	Context KeysContext
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
	{{"Factories", ""}, {"j/k", "move"}, {"1-9", "jump to row"}, {"/", "fuzzy find"}, {"g", "next needing you"}, {"⏎", "focus the session"}, {"i", "answer in the panel"}},
	{{"Lifecycle", ""}, {"n", "new factory"}, {"w", "wake selected"}, {"W", "wake all asleep"}, {"p", "pause (asks first)"}, {"m", "move to a node"}, {"a", "archive (asks first)"}, {"d", "delete (asks first)"}, {"H", "show archived"}},
	{{"Panel", ""}, {"tab", "next tab"}, {"[ ]", "item, without a form"}, {"o", "open in browser"}, {"z", "fold or unfold"}, {"+/-", "taller or shorter"}, {"t", "Activity: all factories"}},
	{{"Anywhere", ""}, {"M-0", "back to factories"}, {"esc", "back to factories"}, {"q", "quit the UI only"}, {"N", "nodes and the phone app"}, {"P", "phone app setup"}, {"?", "these keys"}},
}

var answerKeys = [][2]string{
	{"type", "into the box"},
	{"ctrl+s", "send"},
	{"ctrl+a", "approve"},
	{"⏎", "approve, on the Approve row"},
	{"ctrl+n/p", "next or previous item"},
	{"ctrl+r", "retry / one more round"},
	{"ctrl+x", "skip"},
	{"ctrl+d", "discard"},
	{"ctrl+k", "already handled elsewhere"},
	{"ctrl+o", "open link"},
	{"⏎", "next factory, once nothing is left"},
}

func (k *Keys) View() tea.View {
	c := theme.NewCanvas(k.width, k.height)
	columns := keyColumns
	if k.Context.Remotes {
		columns = slices.Clone(keyColumns)
		columns[0] = append(slices.Clone(columns[0]), [2]string{"R", "remotes"})
	}
	col := max((c.W-2)/len(columns), 20)
	rows := 0
	for i, column := range columns {
		x := 1 + i*col
		rows = max(rows, len(column))
		for y, row := range column {
			if row[1] == "" {
				c.Put(x, y, row[0], theme.FgInk(theme.Magenta).B())
				continue
			}
			c.Put(x, y, fmt.Sprintf("%-3s", row[0]), theme.Key)
			c.PutMax(x+4, y, "➜ "+row[1], theme.Text, col-5)
		}
	}
	heading := rows + 1
	c.Put(1, heading, "Answering in the panel", theme.FgInk(theme.Magenta).B())
	x, y := 1, heading+1
	for _, pair := range answerKeys {
		entry := []theme.Span{{Text: pair[0], Ink: theme.Key}, {Text: " ➜ " + pair[1] + "    ", Ink: theme.Text}}
		if x > 1 && x+theme.SpansWidth(entry) > c.W-1 {
			x, y = 1, y+1
		}
		x = c.Spans(x, y, entry)
	}
	c.Put(1, c.H-1, "any key closes", theme.Muted)
	return view(c)
}
