package footer

import (
	"github.com/afonsodelgado/factory/internal/ui/theme"
)

type Mode string

const (
	Factories Mode = "FACTORIES"
	Session   Mode = "SESSION"
	Panel     Mode = "PANEL"
	Dialog    Mode = "DIALOG"
)

var modeColor = map[Mode]theme.Color{
	Factories: theme.Blue,
	Session:   theme.Green,
	Panel:     theme.Magenta,
	Dialog:    theme.Yellow,
}

type Hint struct {
	Key  string
	Does string
	Good bool
}

func Render(mode Mode, context string, hints []Hint, width int) string {
	c := theme.NewCanvas(width, 1)
	c.Fill(0, 0, width, 1, theme.Bar)
	x := c.Put(0, 0, " "+string(mode)+" ", theme.Ink{FG: theme.Black, BG: modeColor[mode], Bold: true})
	if context != "" {
		x = c.Put(x, 0, " "+context+" ", theme.Ink{FG: theme.Hi, BG: theme.Gutter})
	}
	right := []theme.Span{{Text: "?", Ink: theme.Key}, {Text: " keys ", Ink: theme.Quiet}}
	limit := width - theme.SpansWidth(right) - 1
	x++
	for i, h := range hints {
		segment := theme.Width(h.Key) + 1 + theme.Width(h.Does)
		if i == 0 && x+1+segment > limit {
			h.Does = theme.Truncate(h.Does, limit-x-2-theme.Width(h.Key))
			segment = theme.Width(h.Key) + 1 + theme.Width(h.Does)
		}
		if x+1+segment > limit {
			break
		}
		key := theme.Key
		if h.Good {
			key = theme.FgInk(theme.Green).B()
		}
		x = c.Put(x+1, 0, h.Key, key)
		x = c.Put(x+1, 0, h.Does, theme.Text) + 1
	}
	c.Spans(width-theme.SpansWidth(right), 0, right)
	return c.Render()
}
