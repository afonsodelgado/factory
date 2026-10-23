package frame

import (
	"github.com/afonsodelgado/factory/internal/ui/theme"
)

const shortestPath = 8

func Render(side string, w, h int, title, path string, focused bool) string {
	c := theme.NewCanvas(w, h)
	border := theme.Line
	if focused {
		border = theme.Focused
	}
	switch side {
	case "left":
		drawSide(c, "╭", "╰", border)
	case "right":
		drawSide(c, "╮", "╯", border)
	case "top":
		drawTop(c, title, path, border, focused)
	case "bottom":
		c.HLine(0, 0, c.W, border)
	}
	return c.Render()
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

func drawTop(c *theme.Canvas, title, path string, border theme.Ink, focused bool) {
	titleInk := theme.Quiet
	if focused {
		titleInk = theme.Focused.B()
	}
	if title == "" {
		title = "factory"
	}
	pathRoom := c.W - theme.Width("─ ✻ "+title+" ─  ─")
	if pathRoom < shortestPath {
		path = ""
	}
	c.HLine(0, 0, c.W, border)
	c.Put(1, 0, " ✻ "+theme.Truncate(title, c.W-theme.Width("─ ✻  ─"))+" ", titleInk)
	if path != "" {
		c.Right(c.W-2, 0, " "+keepTail(path, pathRoom)+" ", theme.Muted)
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
