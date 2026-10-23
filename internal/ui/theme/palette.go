package theme

import (
	"image/color"

	"charm.land/lipgloss/v2"
)

var (
	Fg        = lipgloss.Color("#a9b1d6")
	Hi        = lipgloss.Color("#c0caf5")
	Comment   = lipgloss.Color("#565f89")
	Dim       = lipgloss.Color("#737aa2")
	Faint     = lipgloss.Color("#545c7e")
	Gutter    = lipgloss.Color("#3b4261")
	Border    = lipgloss.Color("#414868")
	Red       = lipgloss.Color("#f7768e")
	Green     = lipgloss.Color("#9ece6a")
	Yellow    = lipgloss.Color("#e0af68")
	Blue      = lipgloss.Color("#7aa2f7")
	Magenta   = lipgloss.Color("#bb9af7")
	Cyan      = lipgloss.Color("#7dcfff")
	Orange    = lipgloss.Color("#ff9e64")
	Black     = lipgloss.Color("#15161e")
	Claude    = lipgloss.Color("#d77757")
	Selection = lipgloss.Color("#292e42")
	Highlight = lipgloss.Color("#1f2335")
	Bar       = lipgloss.Color("#16161e")
)

type Color = color.Color

type Ink struct {
	FG   color.Color
	BG   color.Color
	Bold bool
}

func (i Ink) B() Ink {
	i.Bold = true
	return i
}

func (i Ink) Style() lipgloss.Style {
	s := lipgloss.NewStyle()
	if i.FG != nil {
		s = s.Foreground(i.FG)
	}
	if i.BG != nil {
		s = s.Background(i.BG)
	}
	if i.Bold {
		s = s.Bold(true)
	}
	return s
}

func FgInk(c color.Color) Ink {
	return Ink{FG: c}
}

var (
	Text    = FgInk(Fg)
	Bright  = FgInk(Hi)
	Muted   = FgInk(Comment)
	Quiet   = FgInk(Dim)
	Hint    = FgInk(Faint)
	Line    = FgInk(Border)
	Key     = FgInk(Cyan).B()
	Focused = FgInk(Blue)
)

func Width(s string) int {
	return lipgloss.Width(s)
}
