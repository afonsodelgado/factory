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
	Unreachable
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
	Unreachable:   "unreachable",
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
	Pane     Pane
	Hook     *state.HookFile
	Stage    *state.HookFile
	Answered bool
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
	case live.Pane.Shell && !launching(f, live, now):
		return Exited
	case f.State == nil && live.Hook == nil:
		return startingStatus(f, now)
	case needsYou(f, live), PermissionPrompt(live.Stage):
		return NeedsYou
	case stalled(f, live, now, stalledAfter):
		return Stalled
	case f.WaitingOn() == "ci":
		return WaitingCI
	case f.WaitingOn() == "agent":
		return WaitingAgent
	}
	return Running
}

func launching(f Factory, live Live, now time.Time) bool {
	return f.State == nil && live.Hook == nil && now.Sub(f.CreatedAt()) < LaunchGrace
}

func startingStatus(f Factory, now time.Time) Status {
	if now.Sub(f.CreatedAt()) > StartTimeout {
		return FailedToStart
	}
	return Starting
}

func needsYou(f Factory, live Live) bool {
	if PermissionPrompt(live.Hook) {
		return true
	}
	if f.State != nil && f.State.HasAttentionField() {
		return !live.Answered && len(f.State.OpenAttention()) > 0
	}
	waitingOnUser := f.WaitingOn() == "user" || f.WaitingOn() == ""
	return idle(live.Hook) && waitingOnUser
}

func stalled(f Factory, live Live, now time.Time, after time.Duration) bool {
	waitingOn := f.WaitingOn()
	if waitingOn != "agent" && waitingOn != "ci" {
		return false
	}
	stageWorking := live.Stage != nil && !idle(live.Stage)
	return idle(live.Hook) && !stageWorking && now.Sub(lastSign(f, live)) > after
}

func lastSign(f Factory, live Live) time.Time {
	newest := f.StateMod
	if f.Heartbeat.After(newest) {
		newest = f.Heartbeat
	}
	for _, hook := range []*state.HookFile{live.Hook, live.Stage} {
		if hook != nil && hook.Last.At.After(newest) {
			newest = hook.Last.At
		}
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
	freshSession := main.Is("SessionStart") && main.Matcher != "compact"
	return main.Is("Stop") || main.Is("StopFailure") || freshSession || main.Is("Notification", "idle_prompt")
}

func PermissionPrompt(hook *state.HookFile) bool {
	return hook != nil && hook.Prompt != nil
}
