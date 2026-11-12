package hub

import (
	"github.com/afonsodelgado/factory/internal/factory"
	"github.com/afonsodelgado/factory/internal/host"
	"github.com/afonsodelgado/factory/internal/state"
)

type SessionSource = host.SessionSource
type Wake = host.Wake
type WakeKind = host.WakeKind

const (
	FromRegistry = host.FromRegistry
	FromState    = host.FromState
	FromHooks    = host.FromHooks
	NoSession    = host.NoSession
	WakeNewPane  = host.WakeNewPane
	WakeShell    = host.WakeShell
	WakeResume   = host.WakeResume
)

func SessionFor(f factory.Factory, token string, hooks []*state.HookFile) (string, SessionSource) {
	return host.SessionFor(f, token, hooks)
}
func PlanWake(it Item, profile state.Profile, remoteControl bool, hooks []*state.HookFile, writableDirs []string) Wake {
	return host.PlanWake(it, profile, remoteControl, hooks, writableDirs)
}
