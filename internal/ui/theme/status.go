package theme

import (
	"fmt"
	"image/color"
	"time"

	"github.com/afonsodelgado/factory/internal/factory"
)

var statusLook = map[factory.Status]struct {
	icon  string
	color color.Color
}{
	factory.Running:       {"running", Green},
	factory.Starting:      {"starting", Cyan},
	factory.FailedToStart: {"failstart", Red},
	factory.NeedsYou:      {"needs", Orange},
	factory.WaitingCI:     {"ci", Magenta},
	factory.WaitingAgent:  {"agent", Magenta},
	factory.Stalled:       {"stalled", Yellow},
	factory.Exited:        {"exited", Red},
	factory.Gone:          {"gone", Red},
	factory.Done:          {"done", Dim},
}

func StatusIcon(s factory.Status) string {
	return statusLook[s].icon
}

func StatusColor(s factory.Status) color.Color {
	return statusLook[s].color
}

func StatusInk(s factory.Status) Ink {
	return FgInk(StatusColor(s))
}

func TrackSpans(steps []factory.Step, s factory.Status) []Span {
	spans := make([]Span, len(steps))
	for i, step := range steps {
		switch {
		case step == factory.StepSkipped:
			spans[i] = Span{"–", FgInk(Gutter)}
		case s == factory.Done || step == factory.StepDone:
			spans[i] = Span{"●", Quiet}
		case step == factory.StepFailed:
			spans[i] = Span{"✕", FgInk(Red).B()}
		case step == factory.StepCurrent:
			spans[i] = Span{"◉", StatusInk(s).B()}
		default:
			spans[i] = Span{"○", Hint}
		}
	}
	return spans
}

func Age(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(max(d, 0).Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}
