package frame

import (
	"github.com/afonsodelgado/factory/internal/ui/theme"
)

const shortestPath = 8

type Labels struct {
	Title string
	Path  string
	Stage string
}

func Render(side string, w, h int, labels Labels, focused bool) string {
	c := theme.NewCanvas(w, h)
	border := theme.Line
	if focused {
		border = theme.Focused
	}
	split := labels.Stage != ""
	switch side {
	case "left":
		drawSide(c, "╭", "╰", border)
	case "right":
		drawSide(c, "╮", "╯", border)
	case "top":
		drawTop(c, labels, border, focused)
	case "split":
		c.VLine(0, 0, c.H, border)
	case "bottom":
		c.HLine(0, 0, c.W, border)
		if split {
			c.Put(SplitColumn(c.W), 0, "┴", border)
		}
	}
	return c.Render()
}

func SplitColumn(w int) int {
	return w - 1 - (w-1)/2
}

func drawSide(c *theme.Canvas, first, last string, border theme.Ink) {
	for y := range c.H {
		glyph := "│"
		switch y {
		case 0:
			glyph = first
		case c.H - 1:
			glyph = last
		}
		c.Put(0, y, glyph, border)
	}
}

func drawTop(c *theme.Canvas, labels Labels, border theme.Ink, focused bool) {
	titleInk := theme.Quiet
	if focused {
		titleInk = theme.Focused.B()
	}
	title, path, end := labels.Title, labels.Path, c.W
	if title == "" {
		title = "factory"
	}
	c.HLine(0, 0, c.W, border)
	if labels.Stage != "" {
		end, path = SplitColumn(c.W), ""
		c.Put(end, 0, "┬", border)
		stageRoom := c.W - end - theme.Width("┬─  ─")
		c.Put(end+2, 0, " "+theme.Truncate(labels.Stage, stageRoom)+" ", titleInk)
	}
	pathRoom := end - theme.Width("─ ✻ "+title+" ─  ─")
	if pathRoom < shortestPath {
		path = ""
	}
	c.Put(1, 0, " ✻ "+theme.Truncate(title, end-theme.Width("─ ✻  ─"))+" ", titleInk)
	if path != "" {
		c.Right(end-2, 0, " "+keepTail(path, pathRoom)+" ", theme.Muted)
	}
}

func keepTail(path string, width int) string {
	if theme.Width(path) <= width {
		return path
	}
	runes := []rune(path)
	for len(runes) > 0 && theme.Width(string(runes)) > width-1 {
		runes = runes[1:]
	}
	return "…" + string(runes)
}
