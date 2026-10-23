package hub

import (
	"github.com/afonsodelgado/factory/internal/factory"
	"github.com/afonsodelgado/factory/internal/state"
)

type SessionSource string

const (
	FromRegistry SessionSource = "the registry"
	FromState    SessionSource = "state.json"
	FromHooks    SessionSource = "the hook files"
	NoSession    SessionSource = ""
)

func SessionFor(f factory.Factory, token string, hooks []*state.HookFile) (string, SessionSource) {
	if h := paneHook(token, hooks); h != nil {
		return h.SessionID, FromHooks
	}
	if f.Entry != nil && f.Entry.SessionID != "" {
		return f.Entry.SessionID, FromRegistry
	}
	if f.State != nil && f.State.SessionID != "" {
		return f.State.SessionID, FromState
	}
	if f.Worktree == "" {
		return "", NoSession
	}
	var newest *state.HookFile
	for _, h := range hooks {
		if within(h.CWD, f.Worktree) && (newest == nil || h.Last.At.After(newest.Last.At)) {
			newest = h
		}
	}
	if newest != nil {
		return newest.SessionID, FromHooks
	}
	return "", NoSession
}

type Wake struct {
	Kind     WakeKind
	Launch   factory.Launch
	Source   SessionSource
	Command  string
	ThenType string
}

type WakeKind int

const (
	WakeNewPane WakeKind = iota
	WakeShell
	WakeResume
)

func PlanWake(it Item, profile state.Profile, remoteControl bool, hooks []*state.HookFile, writableDirs []string) Wake {
	id, source := SessionFor(it.Factory, tokenOf(it.Pane), hooks)
	supervisor := it.Factory.Supervisor()
	launch := factory.Launch{Repo: it.Factory.Repo, Slug: it.Factory.Slug, SessionID: id, Supervisor: supervisor}
	if supervisor.OnClaude() {
		launch.Chrome, launch.RemoteControl = profile.BrowserQA(), remoteControl
	} else {
		launch.AddDirs = writableDirs
	}
	w := Wake{Launch: launch, Source: source, Command: launch.ResumeCommand()}
	if launch.TypesResumeAfterStart() {
		w.ThenType = launch.ResumeMessage()
	}
	switch it.Status {
	case factory.Gone:
		w.Kind = WakeNewPane
	case factory.Exited:
		w.Kind = WakeShell
	default:
		w.Kind, w.Command, w.ThenType = WakeResume, factory.ForSupervisor(supervisor.ToolName(), Retry()), ""
	}
	return w
}

func (w Wake) Note() string {
	if w.Kind == WakeResume {
		return "woke with " + w.Command + " typed into the session"
	}
	if w.Source == NoSession && w.Launch.Supervisor.OnClaude() {
		return "woke with --continue: no session id in the registry, state.json or the hook files"
	}
	if w.Source == NoSession {
		return "woke a fresh " + w.Launch.Supervisor.ToolName() + " session: no session id in the registry, state.json or the hook files"
	}
	return "woke " + w.Launch.Supervisor.ToolName() + " session " + w.Launch.SessionID + " from " + string(w.Source)
}
