package host

import (
	"fmt"

	"github.com/afonsodelgado/factory/internal/factory"
	"github.com/afonsodelgado/factory/internal/state"
	"github.com/afonsodelgado/factory/internal/tmux"
)

type Typist interface {
	Type(pane, text string) error
}

type Target struct {
	Label string
	Pane  *tmux.Pane
	Hook  *state.HookFile
}

func Refusal(t Target) string {
	switch {
	case t.Pane == nil || t.Pane.Dead:
		return t.Label + " has no session pane. Wake it first."
	case factory.IsShell(t.Pane.Command):
		return t.Label + " is not running Claude: its pane is at a shell prompt. Wake it first."
	case t.Pane.InMode:
		return t.Label + " is in copy mode. Press q in the session, then send again."
	case t.Hook == nil:
		return t.Label + " has reported no hook events yet, so the app cannot tell that it waits at its prompt."
	case factory.PermissionPrompt(t.Hook):
		return t.Label + " is showing a permission prompt. Answer it in the session first."
	case !factory.IdleAtPrompt(t.Hook):
		return Busy(t.Label, t.Hook.MainEvent())
	}
	return ""
}

func Busy(label string, main state.HookEvent) string {
	at := main.At.Local().Format("15:04")
	if main.Is("UserPromptSubmit") {
		return fmt.Sprintf("%s is busy. A prompt was sent at %s and Claude is working on it, not waiting at its prompt.", label, at)
	}
	return fmt.Sprintf("%s is busy. Claude has been working since %s, not waiting at its prompt.", label, at)
}

func Deliver(typist Typist, t Target, text string) (string, error) {
	if reason := Refusal(t); reason != "" {
		return reason, nil
	}
	return "", typist.Type(t.Pane.ID, text)
}

func Unqueueable(t Target) string {
	waitsForItsTurn := t.Pane != nil && !t.Pane.Dead && !factory.IsShell(t.Pane.Command) && !t.Pane.InMode && t.Hook != nil
	if waitsForItsTurn {
		return ""
	}
	return Refusal(t)
}
