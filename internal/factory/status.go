package factory

import (
	"path/filepath"
	"strings"
	"time"

	"github.com/afonsodelgado/factory/internal/state"
)

type Status int

const (
	Running Status = iota
	Starting
	FailedToStart
	NeedsYou
	WaitingCI
	WaitingAgent
	Stalled
	Exited
	Gone
	Done
)

var statusWords = map[Status]string{
	Running:       "running",
	Starting:      "starting",
	FailedToStart: "failed to start",
	NeedsYou:      "needs you",
	WaitingCI:     "on CI",
	WaitingAgent:  "on agent",
	Stalled:       "stalled",
	Exited:        "exited",
	Gone:          "gone",
	Done:          "done",
}

func (s Status) String() string {
	return statusWords[s]
}

func (s Status) Asleep() bool {
	return s == Stalled || s == Exited || s == Gone
}

type Pane struct {
	Found bool
	Shell bool
}

type Live struct {
	Pane Pane
	Hook *state.HookFile
}

var shells = map[string]bool{"zsh": true, "bash": true, "sh": true, "fish": true, "dash": true, "ksh": true, "tcsh": true, "csh": true, "nu": true}

func IsShell(command string) bool {
	return shells[strings.TrimPrefix(filepath.Base(command), "-")]
}

func StatusOf(f Factory, live Live, now time.Time, stalledAfter time.Duration) Status {
	switch {
	case f.Done():
		return Done
	case !live.Pane.Found:
		return Gone
	case f.State == nil && live.Hook == nil:
		return startingStatus(f, now)
	case live.Pane.Shell:
		return Exited
	case needsYou(f, live.Hook):
		return NeedsYou
	case stalled(f, live.Hook, now, stalledAfter):
		return Stalled
	case f.WaitingOn() == "ci":
		return WaitingCI
	case f.WaitingOn() == "agent":
		return WaitingAgent
	}
	return Running
}

func startingStatus(f Factory, now time.Time) Status {
	if now.Sub(f.CreatedAt()) > StartTimeout {
		return FailedToStart
	}
	return Starting
}

func needsYou(f Factory, hook *state.HookFile) bool {
	if PermissionPrompt(hook) {
		return true
	}
	if f.State != nil && f.State.HasAttentionField() {
		return len(f.State.OpenAttention()) > 0
	}
	waitingOnUser := f.WaitingOn() == "user" || f.WaitingOn() == ""
	return idle(hook) && waitingOnUser
}

func stalled(f Factory, hook *state.HookFile, now time.Time, after time.Duration) bool {
	waitingOn := f.WaitingOn()
	if waitingOn != "agent" && waitingOn != "ci" {
		return false
	}
	return idle(hook) && now.Sub(lastSign(f, hook)) > after
}

func lastSign(f Factory, hook *state.HookFile) time.Time {
	newest := f.StateMod
	if f.Heartbeat.After(newest) {
		newest = f.Heartbeat
	}
	if hook != nil && hook.Last.At.After(newest) {
		newest = hook.Last.At
	}
	return newest
}

func idle(hook *state.HookFile) bool {
	return hook == nil || hook.Main == nil || IdleAtPrompt(hook)
}

func IdleAtPrompt(hook *state.HookFile) bool {
	if hook == nil || hook.Main == nil {
		return false
	}
	main := *hook.Main
	return main.Is("Stop") || main.Is("StopFailure") || main.Is("SessionStart") || main.Is("Notification", "idle_prompt")
}

func PermissionPrompt(hook *state.HookFile) bool {
	if hook == nil {
		return false
	}
	asks := func(e state.HookEvent) bool {
		return e.Is("PermissionRequest") || e.Is("Notification", "permission_prompt")
	}
	return asks(hook.MainEvent()) || asks(hook.Last)
}
