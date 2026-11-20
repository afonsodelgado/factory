package host

import (
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/afonsodelgado/factory/internal/factory"
	"github.com/afonsodelgado/factory/internal/state"
	"github.com/afonsodelgado/factory/internal/tmux"
)

type Item struct {
	Factory    factory.Factory
	Status     factory.Status
	Unblocked  factory.Status
	Hook       *state.HookFile
	Pane       *tmux.Pane
	Stage      *StageView
	Discovery  *StageView
	Revising   map[string]string
	Screen     *Screen
	Unreported bool
}

type Snapshot struct {
	At          time.Time
	Items       []Item
	Panes       []tmux.Pane
	Hooks       []*state.HookFile
	StageTokens map[string]bool
}

func (s Snapshot) Find(key string) (Item, bool) {
	for _, it := range s.Items {
		if it.Factory.Key() == key {
			return it, true
		}
	}
	return Item{}, false
}

func (s Snapshot) Named(id string) (Item, bool) {
	for _, it := range s.Items {
		if it.Factory.Label() == id {
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

func (l *Loader) SetRepos(repos []string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.Config.Repos = repos
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
	found = slices.DeleteFunc(found, Moved)
	hooks, _ := state.ReadHookFiles(l.Config.StateDir)
	var panes []tmux.Pane
	if l.TM != nil {
		panes, _ = l.TM.Panes()
	}
	runs := map[string]*state.StageRuns{}
	tokens := map[string]bool{}
	for _, f := range found {
		runs[f.Key()], _ = state.LoadStageRuns(state.StageRunsPath(l.Config.StateDir, f.Repo, f.Slug))
		for _, token := range runs[f.Key()].Tokens() {
			tokens[token] = true
		}
	}
	supervisorHooks := WithoutTokens(hooks, tokens)
	stalledAfter := map[string]time.Duration{}
	for _, f := range found {
		if _, read := stalledAfter[f.Repo]; !read {
			profile, _ := state.ReadProfile(f.Repo)
			stalledAfter[f.Repo] = l.Config.WithProfile(profile).Stalled()
		}
	}
	snap := Snapshot{At: now, Panes: panes, Hooks: hooks, StageTokens: tokens}
	for _, f := range found {
		f = l.keepLastGood(f)
		var pane *tmux.Pane
		if p, ok := FactoryPane(panes, f.Repo, f.Slug); ok {
			pane = &p
		}
		hook := HookFor(f, TokenOf(pane), supervisorHooks)
		l.rememberSession(f, PaneHook(TokenOf(pane), hooks))
		l.rememberBranches(f)
		stage := RunningStage(f, runs[f.Key()], panes, hooks, stalledAfter[f.Repo])
		discovery := DiscoveryStage(f, runs[f.Key()], panes, hooks, stalledAfter[f.Repo])
		revising := RevisingGates(f, runs[f.Key()], panes, hooks)
		live := factory.Live{Pane: LivePane(panes, f), Hook: hook, Stage: promptingFirst(stage.HookValue(), discovery.HookValue()), Revising: revising}
		status := factory.StatusOf(f, live, now, stalledAfter[f.Repo])
		live.Answered = true
		unblocked := factory.StatusOf(f, live, now, stalledAfter[f.Repo])
		l.logHook(f, hook)
		unreported := status == factory.NeedsYou && factory.Unreported(f, live, now)
		snap.Items = append(snap.Items, Item{Factory: f, Status: status, Unblocked: unblocked, Hook: hook, Pane: pane, Stage: stage, Discovery: discovery, Revising: revising, Unreported: unreported})
	}
	return snap
}

func Moved(f factory.Factory) bool {
	return f.Entry != nil && f.Entry.MovedTo != ""
}

func WithoutTokens(hooks []*state.HookFile, tokens map[string]bool) []*state.HookFile {
	var kept []*state.HookFile
	for _, h := range hooks {
		if !tokens[h.Token] {
			kept = append(kept, h)
		}
	}
	return kept
}

func RunningStage(f factory.Factory, runs *state.StageRuns, panes []tmux.Pane, hooks []*state.HookFile, patience time.Duration) *StageView {
	if runs == nil {
		return nil
	}
	run, ok := runs.Running()
	if !ok {
		return nil
	}
	view := &StageView{Run: run, Hook: PaneHook(run.Token, hooks), patience: patience}
	if p, found := stagePaneOf(panes, f, run.Role, run.Token); found {
		view.Pane = &p
	}
	return view
}

const discoveryGrace = time.Minute

func DiscoveryStage(f factory.Factory, runs *state.StageRuns, panes []tmux.Pane, hooks []*state.HookFile, patience time.Duration) *StageView {
	if runs == nil {
		return nil
	}
	run, ok := runs.Discovering()
	if !ok {
		return nil
	}
	view := &StageView{Run: run, Hook: PaneHook(run.Token, hooks), patience: patience}
	if p, found := stagePaneOf(panes, f, run.Role, run.Token); found {
		view.Pane = &p
	}
	return view
}

func promptingFirst(stage, discovery *state.HookFile) *state.HookFile {
	if factory.PermissionPrompt(discovery) && !factory.PermissionPrompt(stage) {
		return discovery
	}
	return stage
}

func (v *StageView) Lost(now time.Time) bool {
	if v == nil || now.Sub(v.Run.StartedAt) <= discoveryGrace {
		return false
	}
	if _, live := LiveStagePane(v.paneValue()); !live {
		return true
	}
	stoppedWithoutResult := v.Hook != nil && v.Hook.Main != nil && v.Hook.Main.At.After(v.Run.StartedAt) && stopped(*v.Hook.Main)
	return stoppedWithoutResult && now.Sub(v.lastActivity()) > v.patience
}

func (v *StageView) lastActivity() time.Time {
	if v.Hook.Last.At.After(v.Hook.Main.At) {
		return v.Hook.Last.At
	}
	return v.Hook.Main.At
}

func (v *StageView) paneValue() (tmux.Pane, bool) {
	if v == nil || v.Pane == nil {
		return tmux.Pane{}, false
	}
	return *v.Pane, true
}

func RevisingGates(f factory.Factory, runs *state.StageRuns, panes []tmux.Pane, hooks []*state.HookFile) map[string]string {
	if f.State == nil || runs == nil {
		return nil
	}
	revising := map[string]string{}
	for _, a := range f.State.OpenAttention() {
		role := state.RoleOf(a.Stage())
		run, ran := runs.Latest(role)
		if a.Kind == "gate" && ran && run.StartedAt.After(a.CreatedAt.Time) && stillWorking(f, run, panes, hooks) {
			revising[a.ID] = role
		}
	}
	return revising
}

func stillWorking(f factory.Factory, run state.StageRun, panes []tmux.Pane, hooks []*state.HookFile) bool {
	if run.Finished() {
		return false
	}
	if _, alive := LiveStagePane(stagePaneOf(panes, f, run.Role, run.Token)); !alive {
		return false
	}
	hook := PaneHook(run.Token, hooks)
	stoppedSinceStart := hook != nil && hook.Main != nil && hook.Main.At.After(run.StartedAt) && stopped(*hook.Main)
	return !stoppedSinceStart
}

func stopped(event state.HookEvent) bool {
	return event.Is("Stop") || event.Is("StopFailure") || event.Is("Notification", "idle_prompt")
}

func (v *StageView) HookValue() *state.HookFile {
	if v == nil {
		return nil
	}
	return v.Hook
}

func (l *Loader) rememberSession(f factory.Factory, hook *state.HookFile) {
	if hook == nil || f.Entry == nil || f.Entry.SessionID != "" {
		return
	}
	if entry := l.Registry.Find(f.Repo, f.Slug); entry != nil {
		entry.SessionID = hook.SessionID
		l.Registry.Save()
	}
}

func (l *Loader) rememberBranches(f factory.Factory) {
	entry := l.Registry.Find(f.Repo, f.Slug)
	if entry == nil || f.State == nil {
		return
	}
	known := len(entry.Branches)
	branches := []string{f.State.Branch}
	for _, pr := range f.State.PRs {
		branches = append(branches, pr.Branch)
	}
	for _, branch := range branches {
		if branch != "" && !slices.Contains(entry.Branches, branch) {
			entry.Branches = append(entry.Branches, branch)
		}
	}
	if len(entry.Branches) > known {
		l.Registry.Save()
	}
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
	if l.Registry.Incarnate() || changed {
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

func TokenOf(pane *tmux.Pane) string {
	if pane == nil {
		return ""
	}
	return pane.Token
}

func PaneHook(token string, hooks []*state.HookFile) *state.HookFile {
	if token == "" {
		return nil
	}
	var newest *state.HookFile
	for _, h := range hooks {
		if h.Token == token && (newest == nil || h.Last.At.After(newest.Last.At)) {
			newest = h
		}
	}
	return newest
}

func HookFor(f factory.Factory, token string, hooks []*state.HookFile) *state.HookFile {
	if h := PaneHook(token, hooks); h != nil {
		return h
	}
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
		if !Within(h.CWD, f.Worktree) {
			continue
		}
		if newest == nil || h.Last.At.After(newest.Last.At) {
			newest = h
		}
	}
	return newest
}

func Within(path, dir string) bool {
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
	toolEvent := main.Is("PreToolUse") || main.Is("PostToolUse") || main.Is("PostToolUseFailure")
	if !seen || previous == signature || toolEvent {
		return
	}
	text := main.Event
	if main.Matcher != "" {
		text += " " + main.Matcher
	}
	state.AppendActivity(state.ActivityPath(l.Config.ActivityDir(), f.Repo, f.Slug), state.ActivityEntry{At: main.At, Source: "hook", Text: text})
}

func (l *Loader) Forget(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.lastGood, key)
	delete(l.lastMain, key)
}

func (l *Loader) Known(repo, slug string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.Registry.Find(repo, slug) != nil
}

func (l *Loader) Lookup(repo, slug string) (factory.Factory, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, f := range factory.Discover([]string{repo}, l.Registry) {
		if f.Slug == slug {
			return f, true
		}
	}
	return factory.Factory{}, false
}

func (l *Loader) Entry(key string) (state.Entry, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, e := range l.Registry.Factories {
		if e.Key() == key {
			return e, true
		}
	}
	return state.Entry{}, false
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
