package selector

import (
	"fmt"

	"github.com/afonsodelgado/factory/internal/factory"
	"github.com/afonsodelgado/factory/internal/ui/theme"
)

const NarrowWidth = 20

type Row struct {
	Key      string
	Label    string
	Title    string
	Repo     string
	Status   factory.Status
	Track    []factory.Step
	Stage    string
	Time     string
	Items    int
	Number   int
	Archived bool
}

func (r Row) Done() bool {
	return r.Status == factory.Done
}

type Group struct {
	Name string
	Path string
	Rows []Row
}

type Data struct {
	Groups       []Group
	Archived     []Row
	ShowArchived bool
	Selected     string
	Focused      bool
	Icons        theme.Icons
	ConfigFile   string
	Dim          bool
}

func Arrange(groups []Group) []Group {
	n := 0
	arranged := make([]Group, len(groups))
	for i, g := range groups {
		var active, done []Row
		for _, r := range g.Rows {
			if r.Done() {
				r.Number = 0
				done = append(done, r)
				continue
			}
			n++
			r.Number = 0
			if n <= 9 {
				r.Number = n
			}
			active = append(active, r)
		}
		arranged[i] = Group{Name: g.Name, Path: g.Path, Rows: append(active, done...)}
	}
	return arranged
}

func Visible(d Data) []Row {
	var rows []Row
	for _, g := range d.Groups {
		rows = append(rows, g.Rows...)
	}
	if d.ShowArchived {
		rows = append(rows, d.Archived...)
	}
	return rows
}

func Counts(d Data) (active, done int) {
	for _, g := range d.Groups {
		for _, r := range g.Rows {
			if r.Done() {
				done++
			} else {
				active++
			}
		}
	}
	return active, done
}

func Render(d Data, w, h int) string {
	c := theme.NewCanvas(w, h)
	c.Dim = d.Dim
	if w < NarrowWidth {
		renderNarrow(c, d)
		return c.Render()
	}
	border, title := theme.Line, theme.Quiet
	if d.Focused {
		border, title = theme.Focused, theme.Focused.B()
	}
	box := theme.Box{Ink: border, Title: []theme.Span{{Text: " Factories ", Ink: title}}}
	active, done := Counts(d)
	if active+done > 0 {
		box.Title = append(box.Title, theme.Span{Text: "─", Ink: border})
		box.TitleRight = []theme.Span{{Text: fmt.Sprintf(" %d active · %d done ", active, done), Ink: theme.Muted}}
	}
	c.Box(0, 0, w, h, box)
	if active+done == 0 && len(d.Archived) == 0 {
		renderEmpty(c, d, w)
		return c.Render()
	}
	renderList(c, d, w, h)
	return c.Render()
}

type item struct {
	height int
	key    string
	draw   func(c *theme.Canvas, y int)
}

func renderList(c *theme.Canvas, d Data, w, h int) {
	inner := w - 2
	ic := d.Icons
	var items []item
	for _, g := range d.Groups {
		if len(g.Rows) == 0 {
			continue
		}
		group := g
		items = append(items, item{height: 1, draw: func(c *theme.Canvas, y int) {
			x := c.Put(2, y, ic.Icon("repo"), theme.Focused)
			x = c.Put(x, y, group.Name, theme.Focused.B())
			c.PutMax(x+2, y, group.Path, theme.Muted, inner-x-1)
		}})
		for _, r := range group.Rows {
			row := r
			if row.Done() {
				items = append(items, item{height: 1, key: row.Key, draw: func(c *theme.Canvas, y int) {
					shortRow(c, y, inner, row, d)
				}})
				continue
			}
			items = append(items, item{height: 2, key: row.Key, draw: func(c *theme.Canvas, y int) {
				factoryRow(c, y, inner, row, d)
			}})
		}
		items = append(items, item{height: 1})
	}
	if d.ShowArchived && len(d.Archived) > 0 {
		items = append(items, item{height: 1, draw: func(c *theme.Canvas, y int) {
			x := c.Put(2, y, ic.Icon("archive"), theme.Quiet)
			x = c.Put(x, y, "archived", theme.Quiet.B())
			c.Put(x+2, y, "H hides them", theme.Muted)
		}})
		for _, r := range d.Archived {
			row := r
			items = append(items, item{height: 1, key: row.Key, draw: func(c *theme.Canvas, y int) {
				shortRow(c, y, inner, row, d)
			}})
		}
	}

	bottom := h - 1
	hint := !d.ShowArchived && len(d.Archived) > 0
	if hint {
		bottom = h - 2
		x := c.Put(2, h-2, ic.Icon("archive")+fmt.Sprintf("%d archived · ", len(d.Archived)), theme.Muted)
		x = c.Put(x, h-2, "H", theme.FgInk(theme.Cyan))
		c.Put(x, h-2, " show", theme.Muted)
	}
	offset := scrollOffset(items, d.Selected, bottom-1)
	c.Clip(1, bottom)
	y := 1 - offset
	for _, it := range items {
		if it.draw != nil {
			it.draw(c, y)
		}
		y += it.height
	}
	c.Unclip()
}

func scrollOffset(items []item, selected string, visible int) int {
	y := 0
	for _, it := range items {
		if it.key == selected && selected != "" {
			if end := y + it.height; end > visible {
				return end - visible
			}
			return 0
		}
		y += it.height
	}
	return 0
}

func factoryRow(c *theme.Canvas, y, inner int, r Row, d Data) {
	sel := r.Key == d.Selected
	if sel {
		c.Fill(1, y, inner, 2, rowBackground(d.Focused))
	}
	cursor(c, y, sel)
	if r.Number > 0 {
		c.Put(2, y, fmt.Sprint(r.Number), numberInk(sel))
	}
	st := theme.StatusInk(r.Status)
	c.Put(4, y, d.Icons.Icon(theme.StatusIcon(r.Status)), st)

	end := inner - 1
	word := r.Status.String()
	badge := ""
	if r.Status == factory.NeedsYou && r.Items > 1 {
		badge = fmt.Sprint(r.Items)
	}
	wordWidth := theme.Width(word)
	if badge != "" {
		wordWidth += 1 + theme.Width(badge)
	}
	start := end - wordWidth + 1
	x := c.Put(start, y, word, st.B())
	if badge != "" {
		c.Put(x+1, y, badge, theme.Ink{FG: theme.Black, BG: theme.Orange, Bold: true})
	}
	c.PutMax(6, y, r.Label, theme.Bright.B(), start-6-1)

	c.Spans(6, y+1, theme.TrackSpans(r.Track, r.Status))
	timeStart := c.Right(end, y+1, r.Time, theme.Muted)
	stage := r.Stage
	if stage == "" {
		stage = "no state yet"
	}
	c.PutMax(17, y+1, stage, theme.Quiet, timeStart-1-17)
}

func shortRow(c *theme.Canvas, y, inner int, r Row, d Data) {
	sel := r.Key == d.Selected
	if sel {
		c.Fill(1, y, inner, 1, rowBackground(d.Focused))
	}
	cursor(c, y, sel)
	icon, word, ink := "done", "done", theme.Quiet
	if r.Archived {
		icon, word, ink = "archive", "archived", theme.Muted
	}
	c.Put(4, y, d.Icons.Icon(icon), ink)
	label := theme.Quiet
	if sel {
		label = theme.Bright.B()
	}
	start := c.Right(inner-1, y, word, ink)
	c.PutMax(6, y, r.Label, label, start-6-1)
}

func cursor(c *theme.Canvas, y int, sel bool) {
	if sel {
		c.Put(1, y, "❯", theme.Focused.B())
	}
}

func numberInk(sel bool) theme.Ink {
	if sel {
		return theme.Bright.B()
	}
	return theme.Quiet
}

func rowBackground(focused bool) theme.Color {
	if focused {
		return theme.Selection
	}
	return theme.Highlight
}

func renderEmpty(c *theme.Canvas, d Data, w int) {
	inner := w - 4
	y := 3
	c.Put(2, y, "No factories running.", theme.Bright)
	y += 2
	for _, line := range theme.Wrap("A factory is one /factory run in its own worktree and tmux pane. Start one from here.", inner) {
		c.Put(2, y, line, theme.Text)
		y++
	}
	y++
	x := c.Put(2, y, "n", theme.Key)
	c.Put(x+2, y, "new factory", theme.Text)
	y++
	x = c.Put(2, y, "?", theme.Key)
	c.Put(x+2, y, "all keys", theme.Text)
	y += 2
	c.Put(2, y, "Watching", theme.Quiet.B())
	y++
	nameWidth := 0
	for _, g := range d.Groups {
		nameWidth = max(nameWidth, theme.Width(g.Name))
	}
	for _, g := range d.Groups {
		x := c.Put(2, y, d.Icons.Icon("repo"), theme.Focused)
		x = c.Put(x, y, g.Name, theme.Focused)
		c.PutMax(x+nameWidth-theme.Width(g.Name)+2, y, g.Path, theme.Muted, w-2-x)
		y++
	}
	config := "config.yml"
	if d.ConfigFile != "" {
		config = d.ConfigFile
	}
	c.PutMax(2, y, "repos live in "+config, theme.Muted, inner)
	y += 2
	for _, line := range theme.Wrap("Factories already running in their own tmux sessions (from the old factory function) show up here by themselves.", inner) {
		c.Put(2, y, line, theme.Text)
		y++
	}
}

func renderNarrow(c *theme.Canvas, d Data) {
	y := 0
	for _, g := range d.Groups {
		if len(g.Rows) == 0 {
			continue
		}
		c.Put(3, y, d.Icons.Icon("repo"), theme.Focused)
		y++
		for _, r := range g.Rows {
			sel := r.Key == d.Selected
			if sel {
				c.Fill(0, y, c.W, 1, theme.Selection)
				c.Put(0, y, "❯", theme.Focused.B())
			}
			if r.Number > 0 {
				c.Put(1, y, fmt.Sprint(r.Number), numberInk(sel))
			}
			ink := theme.StatusInk(r.Status)
			c.Put(3, y, d.Icons.Icon(theme.StatusIcon(r.Status)), ink)
			y++
		}
		y++
	}
}
