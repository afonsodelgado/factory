package hub

import (
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/afonsodelgado/factory/internal/factory"
	"github.com/afonsodelgado/factory/internal/state"
	"github.com/afonsodelgado/factory/internal/tmux"
)

type Item struct {
	Factory factory.Factory
	Status  factory.Status
	Hook    *state.HookFile
	Pane    *tmux.Pane
}

type Snapshot struct {
	At    time.Time
	Items []Item
	Panes []tmux.Pane
}

func (s Snapshot) Find(key string) (Item, bool) {
	for _, it := range s.Items {
		if it.Factory.Key() == key {
			return it, true
		}
	}
	return Item{}, false
}

type Loader struct {
	Config   *state.Config
	Registry *state.Registry
	TM       *tmux.Client
	Now      func() time.Time

	mu       sync.Mutex
	lastGood map[string]*state.State
	lastMain map[string]string
}

func (l *Loader) Load() Snapshot {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.lastGood == nil {
		l.lastGood = map[string]*state.State{}
		l.lastMain = map[string]string{}
	}
	now := l.Now()
	found := factory.Discover(l.Config.Repos, l.Registry)
	l.observe(found, now)
	hooks, _ := state.ReadHookFiles(l.Config.StateDir)
	var panes []tmux.Pane
	if l.TM != nil {
		panes, _ = l.TM.Panes()
	}
	snap := Snapshot{At: now, Panes: panes}
	for _, f := range found {
		f = l.keepLastGood(f)
		hook := hookFor(f, hooks)
		var pane *tmux.Pane
		if p, ok := FactoryPane(panes, f.Repo, f.Slug); ok {
			pane = &p
		}
		status := factory.StatusOf(f, factory.Live{Pane: LivePane(panes, f), Hook: hook}, now, l.Config.Stalled())
		l.logHook(f, hook)
		snap.Items = append(snap.Items, Item{Factory: f, Status: status, Hook: hook, Pane: pane})
	}
	return snap
}

func (l *Loader) observe(found []factory.Factory, now time.Time) {
	changed := false
	for _, f := range found {
		if f.Entry != nil {
			continue
		}
		seen := f.CreatedAt()
		if seen.IsZero() {
			seen = now
		}
		if _, added := l.Registry.Observe(f.Repo, f.Slug, seen); added {
			changed = true
		}
	}
	if changed {
		l.Registry.Save()
	}
}

func (l *Loader) keepLastGood(f factory.Factory) factory.Factory {
	if f.State != nil {
		l.lastGood[f.Key()] = f.State
		return f
	}
	if last, ok := l.lastGood[f.Key()]; ok && f.Dir != "" {
		f.State, f.StateErr = last, nil
	}
	return f
}

func hookFor(f factory.Factory, hooks []*state.HookFile) *state.HookFile {
	ids := []string{}
	if f.State != nil && f.State.SessionID != "" {
		ids = append(ids, f.State.SessionID)
	}
	if f.Entry != nil && f.Entry.SessionID != "" {
		ids = append(ids, f.Entry.SessionID)
	}
	for _, id := range ids {
		for _, h := range hooks {
			if h.SessionID == id {
				return h
			}
		}
	}
	if f.Worktree == "" {
		return nil
	}
	var newest *state.HookFile
	for _, h := range hooks {
		if !within(h.CWD, f.Worktree) {
			continue
		}
		if newest == nil || h.Last.At.After(newest.Last.At) {
			newest = h
		}
	}
	return newest
}

func within(path, dir string) bool {
	path, dir = filepath.Clean(path), filepath.Clean(dir)
	return path == dir || strings.HasPrefix(path, dir+string(filepath.Separator))
}

func (l *Loader) logHook(f factory.Factory, hook *state.HookFile) {
	if hook == nil || hook.Main == nil {
		return
	}
	main := *hook.Main
	signature := main.Event + "|" + main.Matcher + "|" + main.At.String()
	previous, seen := l.lastMain[f.Key()]
	l.lastMain[f.Key()] = signature
	if !seen || previous == signature || main.Is("PreToolUse") {
		return
	}
	text := main.Event
	if main.Matcher != "" {
		text += " " + main.Matcher
	}
	state.AppendActivity(state.ActivityPath(l.Config.ActivityDir(), f.Repo, f.Slug), state.ActivityEntry{At: main.At, Source: "hook", Text: text})
}

func (l *Loader) UpdateRegistry(fn func(*state.Registry)) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	fn(l.Registry)
	return l.Registry.Save()
}

func (l *Loader) Note(f factory.Factory, text string) {
	state.AppendActivity(state.ActivityPath(l.Config.ActivityDir(), f.Repo, f.Slug), state.ActivityEntry{At: l.Now(), Source: "app", Text: text})
}
