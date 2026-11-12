package theme

import (
	"image/color"
	"strings"

	"github.com/rivo/uniseg"
)

type cell struct {
	text string
	ink  Ink
	cont bool
}

type Canvas struct {
	W, H     int
	Dim      bool
	cells    [][]cell
	clipTop  int
	clipDown int
}

func NewCanvas(w, h int) *Canvas {
	w, h = max(w, 0), max(h, 0)
	c := &Canvas{W: w, H: h, cells: make([][]cell, h), clipDown: h}
	for y := range c.cells {
		c.cells[y] = make([]cell, w)
		for x := range c.cells[y] {
			c.cells[y][x] = cell{text: " "}
		}
	}
	return c
}

func (c *Canvas) inside(x, y int) bool {
	return x >= 0 && y >= c.clipTop && x < c.W && y < c.clipDown
}

func (c *Canvas) Clip(top, bottom int) {
	c.clipTop, c.clipDown = max(top, 0), min(bottom, c.H)
}

func (c *Canvas) Unclip() {
	c.clipTop, c.clipDown = 0, c.H
}

func (c *Canvas) Put(x, y int, text string, ink Ink) int {
	if y < c.clipTop || y >= c.clipDown {
		return x + Width(text)
	}
	g := uniseg.NewGraphemes(text)
	for g.Next() {
		cluster := g.Str()
		w := Width(cluster)
		if w == 0 {
			continue
		}
		if x+w > c.W {
			return x + w
		}
		if c.inside(x, y) {
			c.set(x, y, cluster, ink)
			for i := 1; i < w; i++ {
				c.cells[y][x+i] = cell{cont: true, ink: c.merge(x+i, y, ink)}
			}
		}
		x += w
	}
	return x
}

func (c *Canvas) set(x, y int, text string, ink Ink) {
	c.cells[y][x] = cell{text: text, ink: c.merge(x, y, ink)}
}

func (c *Canvas) merge(x, y int, ink Ink) Ink {
	if ink.BG == nil {
		ink.BG = c.cells[y][x].ink.BG
	}
	return ink
}

func (c *Canvas) PutMax(x, y int, text string, ink Ink, limit int) int {
	return c.Put(x, y, Truncate(text, limit), ink)
}

func (c *Canvas) Right(end, y int, text string, ink Ink) int {
	start := end - Width(text) + 1
	c.Put(start, y, text, ink)
	return start
}

func (c *Canvas) Fill(x, y, w, h int, bg color.Color) {
	for row := y; row < y+h; row++ {
		for col := x; col < x+w; col++ {
			if c.inside(col, row) {
				c.cells[row][col].ink.BG = bg
			}
		}
	}
}

func (c *Canvas) HLine(x, y, w int, ink Ink) {
	c.Put(x, y, strings.Repeat("─", max(w, 0)), ink)
}

func (c *Canvas) VLine(x, y, h int, ink Ink) {
	for row := y; row < y+h; row++ {
		c.Put(x, row, "│", ink)
	}
}

type Box struct {
	Title      []Span
	TitleRight []Span
	Foot       []Span
	Ink        Ink
}

type Span struct {
	Text string
	Ink  Ink
}

func (c *Canvas) Box(x, y, w, h int, b Box) {
	if w < 2 || h < 2 {
		return
	}
	c.Put(x, y, "╭"+strings.Repeat("─", w-2)+"╮", b.Ink)
	for row := y + 1; row < y+h-1; row++ {
		c.Put(x, row, "│", b.Ink)
		c.Put(x+w-1, row, "│", b.Ink)
	}
	c.Put(x, y+h-1, "╰"+strings.Repeat("─", w-2)+"╯", b.Ink)
	c.Spans(x+1, y, b.Title)
	if len(b.TitleRight) > 0 {
		c.Spans(x+w-1-SpansWidth(b.TitleRight), y, b.TitleRight)
	}
	if len(b.Foot) > 0 {
		c.Spans(x+1, y+h-1, b.Foot)
	}
}

func (c *Canvas) Spans(x, y int, spans []Span) int {
	for _, s := range spans {
		x = c.Put(x, y, s.Text, s.Ink)
	}
	return x
}

func SpansWidth(spans []Span) int {
	w := 0
	for _, s := range spans {
		w += Width(s.Text)
	}
	return w
}

func (c *Canvas) Render() string {
	lines := make([]string, c.H)
	for y, row := range c.cells {
		var b strings.Builder
		var run strings.Builder
		var runInk Ink
		flush := func() {
			if run.Len() == 0 {
				return
			}
			style := runInk.Style()
			if c.Dim {
				style = style.Faint(true)
			}
			b.WriteString(style.Render(run.String()))
			run.Reset()
		}
		for x, cl := range row {
			if cl.cont {
				continue
			}
			if x == 0 || cl.ink != runInk {
				flush()
				runInk = cl.ink
			}
			run.WriteString(cl.text)
		}
		flush()
		lines[y] = b.String()
	}
	return strings.Join(lines, "\n")
}

func Truncate(text string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if Width(text) <= limit {
		return text
	}
	var b strings.Builder
	w := 0
	g := uniseg.NewGraphemes(text)
	for g.Next() {
		cw := Width(g.Str())
		if w+cw > limit-1 {
			break
		}
		b.WriteString(g.Str())
		w += cw
	}
	return b.String() + "…"
}

func KeepTail(text string, limit int) string {
	if Width(text) <= limit {
		return text
	}
	if limit <= 0 {
		return ""
	}
	runes := []rune(text)
	for len(runes) > 0 && Width(string(runes)) > limit-1 {
		runes = runes[1:]
	}
	return "…" + string(runes)
}

func Wrap(text string, width int) []string {
	var lines []string
	for _, para := range strings.Split(text, "\n") {
		words := strings.Fields(para)
		if len(words) == 0 {
			lines = append(lines, "")
			continue
		}
		line := ""
		for _, word := range words {
			switch {
			case line == "":
				line = word
			case Width(line)+1+Width(word) <= width:
				line += " " + word
			default:
				lines = append(lines, line)
				line = word
			}
		}
		lines = append(lines, line)
	}
	return lines
}
