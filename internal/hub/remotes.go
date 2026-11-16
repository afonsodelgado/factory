package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/afonsodelgado/factory/internal/factory"
	"github.com/afonsodelgado/factory/internal/host"
	"github.com/afonsodelgado/factory/internal/remote"
	"github.com/afonsodelgado/factory/internal/state"
	"github.com/afonsodelgado/factory/internal/tmux"
	"github.com/afonsodelgado/factory/internal/ui/dialog"
	"github.com/afonsodelgado/factory/internal/ui/selector"
	"github.com/afonsodelgado/factory/internal/ui/theme"
)

const (
	unreachableAfter = 15 * time.Second
	remoteWait       = 15 * time.Second
	attachRetry      = 10 * time.Second
	proxyUI          = "proxy"
)

type linkState int

const (
	connecting linkState = iota
	connected
	unreachable
)

func (s linkState) String() string {
	return [...]string{"connecting", "connected", "unreachable"}[s]
}

type link struct {
	view    remote.View
	server  string
	offset  time.Duration
	ok      bool
	contact time.Time
	since   time.Time
	problem string
	busy    bool
}

func (l *link) state(now time.Time) linkState {
	last := l.contact
	if last.IsZero() {
		last = l.since
	}
	switch {
	case l.ok:
		return connected
	case now.Sub(last) < unreachableAfter:
		return connecting
	}
	return unreachable
}

func (l *link) lost(now time.Time) time.Duration {
	if l.contact.IsZero() {
		return now.Sub(l.since)
	}
	return now.Sub(l.contact)
}

type remoteMsg struct {
	name   string
	view   remote.View
	server string
	events *remote.Events
	err    error
}

type attachedMsg struct {
	key, role, token, view string
	err                    error
}

type attempt struct {
	at      time.Time
	busy    bool
	problem string
}

func (m *Model) remoteNames() []string {
	return slices.Sorted(maps.Keys(m.Config.Remotes))
}

func (m *Model) link(name string) *link {
	if m.links == nil {
		m.links = map[string]*link{}
	}
	l, ok := m.links[name]
	if !ok {
		l = &link{since: m.Now()}
		if m.Config.StateDir != "" {
			l.view, l.contact = loadView(m.Config.StateDir, name)
			cache, _ := remote.LoadCache(m.Config.StateDir, name)
			l.server = cache.Server
		}
		m.links[name] = l
	}
	return l
}

func viewPath(stateDir, name string) string {
	return filepath.Join(stateDir, "remotes", name+".view.json")
}

func loadView(stateDir, name string) (remote.View, time.Time) {
	var view remote.View
	path := viewPath(stateDir, name)
	data, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(data, &view) != nil {
		return remote.View{}, time.Time{}
	}
	info, err := os.Stat(path)
	if err != nil {
		return view, time.Time{}
	}
	return view, info.ModTime()
}

func (m *Model) pollRemotes() tea.Cmd {
	if m.Config.StateDir == "" {
		return nil
	}
	m.followMachine()
	var cmds []tea.Cmd
	for _, name := range m.remoteNames() {
		l := m.link(name)
		if l.busy {
			continue
		}
		l.busy = true
		cfg := new(*m.Config)
		cmds = append(cmds, func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), remoteWait)
			defer cancel()
			view, server, err := fetchView(ctx, cfg, name)
			if err != nil {
				return remoteMsg{name: name, err: err}
			}
			return remoteMsg{name: name, view: view, server: server, events: listen(ctx, cfg, name)}
		})
	}
	return tea.Batch(cmds...)
}

func fetchView(ctx context.Context, cfg *state.Config, name string) (remote.View, string, error) {
	client, err := remote.For(cfg, name)
	if err != nil {
		return remote.View{}, "", err
	}
	cache, err := remote.LoadCache(cfg.StateDir, name)
	if err != nil {
		return remote.View{}, "", err
	}
	if cache.Server == "" {
		hello, err := client.Hello(ctx)
		if err != nil {
			return remote.View{}, "", err
		}
		cache.Server = hello.Server
	}
	var view remote.View
	if err := client.Read(ctx, "view", nil, &view); err != nil {
		return remote.View{}, "", err
	}
	data, err := json.Marshal(view)
	if err == nil {
		err = state.WriteFileAtomic(viewPath(cfg.StateDir, name), data)
	}
	return view, cache.Server, err
}

func (m *Model) followMachine() {
	if m.Config.ConfigDir == "" {
		return
	}
	if fresh, err := state.LoadMachine(m.Config.ConfigDir); err == nil && !fresh.Equal(m.Config.Machine) {
		m.Config.Machine = fresh
		m.refresh()
	}
}

func pushOnDisk(cfg *state.Config) state.Push {
	if cfg.ConfigDir == "" {
		return cfg.Push
	}
	if fresh, err := state.LoadMachine(cfg.ConfigDir); err == nil {
		return fresh.Push
	}
	return cfg.Push
}

func listen(ctx context.Context, cfg *state.Config, name string) *remote.Events {
	client, err := remote.For(cfg, name)
	if err != nil {
		return nil
	}
	client.SyncPush(ctx, pushOnDisk(cfg))
	w, _ := remote.LoadWatch(cfg.StateDir, name)
	events, err := client.Events(ctx, w.Cursor)
	if err != nil {
		return nil
	}
	return &events
}

func (m *Model) remoteArrived(msg remoteMsg) tea.Cmd {
	l := m.link(msg.name)
	l.busy = false
	if msg.err != nil {
		if l.ok {
			m.noteLink(msg.name, "lost: "+msg.err.Error()+" · factories keep running")
		}
		l.ok, l.problem = false, msg.err.Error()
		m.refresh()
		return nil
	}
	back := !l.ok
	if back && !l.contact.IsZero() {
		m.noteLink(msg.name, "back after "+theme.Age(m.Now().Sub(l.contact))+" · tunnels and inbox restored")
	}
	l.ok, l.view, l.server, l.contact, l.problem = true, msg.view, msg.server, m.Now(), ""
	if !msg.view.Now.IsZero() {
		l.offset = msg.view.Now.Sub(m.Now())
	}
	m.refresh()
	m.heard(msg.name, msg.server, msg.events)
	if back {
		return tea.Batch(m.restoreTunnels(msg.name), m.settleOpens(msg.name))
	}
	return m.settleOpens(msg.name)
}

func (m *Model) heard(name, server string, batch *remote.Events) {
	if batch == nil || m.Config.StateDir == "" {
		return
	}
	w, _ := remote.LoadWatch(m.Config.StateDir, name)
	w, fresh := w.Take(server, *batch)
	if remote.SaveWatch(m.Config.StateDir, name, w) != nil {
		return
	}
	for _, e := range fresh {
		m.alertRemote(name, e)
	}
}

func (m *Model) alertRemote(name string, e remote.Attention) {
	for _, it := range m.snap.Items {
		if it.Factory.Remote != nil && it.Factory.Remote.Incarnation == e.Incarnation && it.Factory.Key() == m.selected {
			return
		}
	}
	if m.Config.Alerts.Bell && m.Bell != nil {
		m.Bell.Write([]byte("\a"))
	}
	if m.Config.Alerts.Notifies() && m.Notify != nil {
		go m.Notify(e.Factory + " on " + name + " needs you")
	}
}

func linkActivityPath(stateDir, name string) string {
	return remote.CachePath(stateDir, name+".activity")
}

func (m *Model) noteLink(name, text string) {
	if m.Config.StateDir != "" {
		state.AppendActivity(linkActivityPath(m.Config.StateDir, name), state.ActivityEntry{At: m.Now(), Source: "hub", Stage: "link", Text: text})
	}
}

func (m *Model) settleRemoteClaims() {
	open := map[string]bool{}
	for _, it := range m.remoteItems() {
		for _, item := range attentionItems(it, m.snap.At, nil) {
			open[draftKey(it.Factory, item.ID)] = true
		}
	}
	for key, mark := range m.claims {
		if !host.Remote(mark.Factory) || open[key] {
			continue
		}
		delete(m.panel.drafts, key)
		os.Remove(mark.Draft)
		m.dropClaim(key)
	}
}

func (m *Model) remoteItems() []Item {
	now := m.Now()
	var items []Item
	m.wires = map[string]remote.Factory{}
	for _, name := range m.remoteNames() {
		l := m.link(name)
		var cache remote.Cache
		if m.Config.StateDir != "" {
			cache, _ = remote.LoadCache(m.Config.StateDir, name)
		}
		listed := map[string]bool{}
		for _, w := range l.view.Factories {
			listed[w.ID] = true
			if slices.Contains(cache.Forgotten, w.Incarnation) {
				continue
			}
			it := remoteItem(name, l, w)
			if l.state(now) == unreachable {
				it.Status, it.Unblocked = factory.Unreachable, factory.Unreachable
			}
			m.wires[it.Factory.Key()] = w
			items = append(items, it)
		}
		for _, r := range cache.Reservations {
			if !listed[r.ID] {
				items = append(items, creatingItem(name, l.server, r))
			}
		}
	}
	return items
}

func remoteItem(name string, l *link, w remote.Factory) Item {
	f := factory.Factory{
		Repo: w.Repo, Slug: w.Slug, ID: w.ID, Worktree: w.Worktree, State: w.State, StateMod: w.StateMod, Entry: w.Entry,
		Remote: &factory.Location{Name: name, Server: l.server, Incarnation: w.Incarnation, Home: l.view.Home, Repo: w.RepoName},
	}
	if w.StateError != "" {
		f.StateErr = errors.New(w.StateError)
	}
	it := Item{Factory: f, Status: w.Status, Unblocked: w.Unblocked, Hook: w.Hook}
	if w.Stage != nil {
		it.Stage = &host.StageView{Run: w.Stage.Run}
	}
	return it
}

func creatingItem(name, server string, r remote.Reservation) Item {
	repo := strings.TrimSuffix(filepath.Base(r.GitURL), ".git")
	f := factory.Factory{Slug: r.ID, ID: r.ID, Remote: &factory.Location{Name: name, Server: server, Incarnation: r.Op, Repo: repo, Creating: r.At}}
	return Item{Factory: f, Status: factory.Starting, Unblocked: factory.Starting}
}

func localOnly(s Snapshot) Snapshot {
	s.Items = slices.DeleteFunc(slices.Clone(s.Items), func(it Item) bool { return it.Factory.Remote != nil })
	return s
}

func remotePath(f factory.Factory, path string) string {
	if path == "" {
		return ""
	}
	home := f.Remote.Home
	if home != "" && (path == home || strings.HasPrefix(path, home+"/")) {
		path = "~" + strings.TrimPrefix(path, home)
	}
	return f.Remote.Name + ":" + path
}

func (m *Model) linkOf(f factory.Factory) *link { return m.link(f.Remote.Name) }

func (m *Model) unreachable(f factory.Factory) bool {
	return f.Remote != nil && m.linkOf(f).state(m.Now()) != connected
}

var pastTense = map[string]string{"wake": "woken", "pause": "paused", "archive": "archived", "unarchive": "unarchived", "delete": "deleted"}

func (m *Model) remoteAct(it Item, action string, fields map[string]any) error {
	f := it.Factory
	if m.unreachable(f) {
		return fmt.Errorf("%s is unreachable, so %s was not %s. Nothing changed.", f.Remote.Name, f.Label(), pastTense[action])
	}
	payload := map[string]any{"id": f.Label(), "incarnation": f.Remote.Incarnation}
	maps.Copy(payload, fields)
	cfg, name, label := m.Config, f.Remote.Name, f.Label()
	send := func() error { return mutate(cfg, name, action, payload, nil) }
	if action == "delete" {
		send = func() error { return deleteOn(cfg, name, remote.Target{ID: label, Incarnation: f.Remote.Incarnation}) }
	}
	m.later = append(m.later, func() tea.Msg {
		err := send()
		switch {
		case errors.Is(err, remote.ErrUncertain):
			return noticeMsg(problem(fmt.Sprintf("%s did not answer, so whether %s was %s is unknown. Wait for the list to refresh before trying again.", name, label, pastTense[action])))
		case err != nil:
			return noticeMsg(problem(err.Error()))
		}
		return noticeMsg(news(fmt.Sprintf("%s was %s on %s.", label, pastTense[action], name)))
	})
	return nil
}

func mutate(cfg *state.Config, name, action string, payload, reply any) error {
	return mutateWithin(cfg, name, action, remoteWait, payload, reply)
}

func mutateWithin(cfg *state.Config, name, action string, wait time.Duration, payload, reply any) error {
	client, err := remote.For(cfg, name)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()
	return client.MutateOnce(ctx, action, payload, reply)
}

func deleteOn(cfg *state.Config, name string, target remote.Target) error {
	client, err := remote.For(cfg, name)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), remoteWait)
	defer cancel()
	return client.Delete(ctx, target, nil)
}

type answeredMsg struct {
	mark sentMark
	at   time.Time
	err  error
}

func (m *Model) deliverRemote(it Item, mark sentMark, text string) {
	f := it.Factory
	key := mark.Factory + "|" + mark.ItemID
	if m.unreachable(f) {
		m.panel.refused[key] = "Not sent. " + f.Remote.Name + " is unreachable."
		return
	}
	m.putClaim(mark)
	m.report(m.ask("put-claim", mark, nil))
	payload := map[string]any{"id": f.Label(), "incarnation": f.Remote.Incarnation, "ItemID": mark.ItemID, "ItemSince": mark.ItemSince, "Text": text, "LeavesOpen": mark.LeavesOpen}
	cfg, name := m.Config, f.Remote.Name
	m.later = append(m.later, func() tea.Msg {
		var reply host.AnswerReply
		err := mutate(cfg, name, "answer", payload, &reply)
		return answeredMsg{mark: mark, at: reply.At, err: err}
	})
}

func (m *Model) answered(msg answeredMsg) {
	key := msg.mark.Factory + "|" + msg.mark.ItemID
	switch {
	case errors.Is(msg.err, remote.ErrUncertain):
		m.dropClaim(key)
		m.panel.refused[key] = "Outcome unknown: the server did not answer. Look at the session before sending again."
	case msg.err != nil:
		m.dropClaim(key)
		m.panel.refused[key] = "Not sent. " + withFullStop(msg.err.Error())
	default:
		msg.mark.At = msg.at
		m.putClaim(msg.mark)
		m.report(m.ask("record-sent", host.SentRecord{Answer: msg.mark, At: msg.at}, nil))
	}
}

func withFullStop(sentence string) string {
	if strings.HasSuffix(sentence, ".") {
		return sentence
	}
	return sentence + "."
}

func proxySlug(f factory.Factory) string { return f.Label() + "+" + f.Remote.Name }

func (m *Model) proxyPane(key, role, token string) (tmux.Pane, bool) {
	for _, p := range m.snap.Panes {
		if p.Repo == key && p.Stage == role && p.Token == token && !p.Dead && (p.UI == proxyUI || p.UI == "stage") {
			return p, true
		}
	}
	return tmux.Pane{}, false
}

func (m *Model) remoteTargets(it Item) (string, string) {
	w := m.wires[it.Factory.Key()]
	target := m.slotViewer()
	if w.Pane == nil || m.linkOf(it.Factory).state(m.Now()) == unreachable {
		return target, ""
	}
	p, ok := m.proxyPane(it.Factory.Key(), "", w.Pane.Token)
	if !ok {
		return target, ""
	}
	roomForTwo := m.window[0] == 0 || m.window[0] > NarrowAt
	if w.Stage == nil || w.Stage.Pane == nil || !roomForTwo {
		return p.ID, ""
	}
	stage, ok := m.proxyPane(it.Factory.Key(), w.Stage.Run.Role, w.Stage.Pane.Token)
	if !ok {
		return p.ID, ""
	}
	return p.ID, stage.ID
}

func (m *Model) attachSelected() tea.Cmd {
	it, ok := m.snap.Find(m.selected)
	if !ok || it.Factory.Remote == nil || m.Engine == nil || m.unreachable(it.Factory) {
		return nil
	}
	w := m.wires[it.Factory.Key()]
	var cmds []tea.Cmd
	if w.Pane != nil {
		cmds = append(cmds, m.attach(it, "", w.Pane.Token))
	}
	if w.Pane != nil && w.Stage != nil && w.Stage.Pane != nil {
		cmds = append(cmds, m.attach(it, w.Stage.Run.Role, w.Stage.Pane.Token))
	}
	return tea.Batch(cmds...)
}

func (m *Model) attach(it Item, role, token string) tea.Cmd {
	key := it.Factory.Key()
	if _, ok := m.proxyPane(key, role, token); ok {
		return nil
	}
	if m.attempts == nil {
		m.attempts = map[string]attempt{}
	}
	id := key + "|" + role + "|" + token
	a := m.attempts[id]
	if a.busy || m.Now().Sub(a.at) < attachRetry {
		return nil
	}
	m.attempts[id] = attempt{at: m.Now(), busy: true}
	target := remote.PaneTarget{Target: remote.Target{ID: it.Factory.Label(), Incarnation: it.Factory.Remote.Incarnation}, Role: role, Token: token}
	cfg, name := m.Config, it.Factory.Remote.Name
	return func() tea.Msg {
		client, err := remote.For(cfg, name)
		var view string
		if err == nil {
			ctx, cancel := context.WithTimeout(context.Background(), remoteWait)
			defer cancel()
			err = client.Read(ctx, "attach", target, &view)
		}
		return attachedMsg{key: key, role: role, token: token, view: view, err: err}
	}
}

func (m *Model) attached(msg attachedMsg) tea.Cmd {
	id := msg.key + "|" + msg.role + "|" + msg.token
	a := attempt{at: m.Now()}
	it, ok := m.snap.Find(msg.key)
	if msg.err == nil && ok && m.Engine != nil {
		msg.err = m.openProxy(it, msg)
	}
	if msg.err != nil {
		a.problem = msg.err.Error()
	}
	m.attempts[id] = a
	return m.load()
}

func (m *Model) openProxy(it Item, msg attachedMsg) error {
	client, err := remote.For(m.Config, it.Factory.Remote.Name)
	if err != nil {
		return err
	}
	words := slices.Concat([]string{"ssh"}, client.Args(true, []string{"factory", "host", "attach", msg.view}))
	quoted := make([]string, len(words))
	for i, w := range words {
		quoted[i] = factory.ShellQuote(w)
	}
	slug := proxySlug(it.Factory)
	window, tags := host.ParkName(slug), [][2]string{{"@factory_ui", proxyUI}, {"@factory_repo", msg.key}, {"@factory_slug", slug}, {"@factory_token", msg.token}}
	if msg.role != "" {
		window, tags = host.StageParkName(slug, msg.role), [][2]string{{"@factory_ui", "stage"}, {"@factory_repo", msg.key}, {"@factory_stage_of", slug}, {"@factory_stage", msg.role}, {"@factory_token", msg.token}}
	}
	pane, err := m.Engine.TM.NewWindow(tmux.Window{Session: m.Engine.Session, Name: window, Env: m.Engine.Env, Command: "exec " + strings.Join(quoted, " ")})
	if err != nil {
		return err
	}
	for _, t := range tags {
		if err := m.Engine.TM.SetPaneOption(pane, t[0], t[1]); err != nil {
			return err
		}
	}
	return m.Engine.FitParking(m.Layout)
}

func (m *Model) dropProxies() {
	if m.Engine == nil {
		return
	}
	for _, p := range m.snap.Panes {
		if !strings.HasPrefix(p.Repo, "remote:") {
			continue
		}
		if _, listed := m.snap.Find(p.Repo); !listed {
			m.Engine.TM.KillPane(p.ID)
		}
	}
}

func (m *Model) attachProblem(it Item) string {
	w := m.wires[it.Factory.Key()]
	if w.Pane == nil {
		return ""
	}
	return m.attempts[it.Factory.Key()+"||"+w.Pane.Token].problem
}

type removalMsg struct {
	key     string
	removal remote.Removal
	err     error
}

func (m *Model) remoteDeleteContext(f factory.Factory) dialog.DeleteContext {
	return dialog.DeleteContext{Key: f.Key(), Label: f.Label(), Title: f.Title(), Remote: f.Remote.Name, Repo: f.RepoName(), Unreachable: m.unreachable(f)}
}

func (m *Model) askRemoteDelete(it Item) tea.Cmd {
	f := it.Factory
	if !f.Remote.Creating.IsZero() {
		m.notice = problem(f.Label() + " is still being created on " + f.Remote.Name + "; wait until it starts.")
		return nil
	}
	if m.unreachable(f) {
		return m.openDialog("delete", m.remoteDeleteContext(f))
	}
	cfg, name, key := m.Config, f.Remote.Name, f.Key()
	target := remote.Target{ID: f.Label(), Incarnation: f.Remote.Incarnation}
	return func() tea.Msg {
		var r remote.Removal
		client, err := remote.For(cfg, name)
		if err == nil {
			ctx, cancel := context.WithTimeout(context.Background(), remoteWait)
			defer cancel()
			err = client.Read(ctx, "removal", target, &r)
		}
		return removalMsg{key: key, removal: r, err: err}
	}
}

func (m *Model) removalArrived(msg removalMsg) tea.Cmd {
	it, ok := m.snap.Find(msg.key)
	if !ok {
		return nil
	}
	if msg.err != nil {
		m.notice = problem(msg.err.Error())
		return nil
	}
	c := m.remoteDeleteContext(it.Factory)
	r := msg.removal
	c.Worktree, c.WorktreeGone, c.Branches, c.Dirty, c.Unpushed = remotePath(it.Factory, r.Worktree), r.WorktreeGone, r.Branches, r.Dirty, r.Unpushed
	return m.openDialog("delete", c)
}

func (m *Model) forgetRemote(key string) error {
	it, ok := m.snap.Find(key)
	if !ok || it.Factory.Remote == nil {
		return fmt.Errorf("the hub does not list %s", key)
	}
	f := it.Factory
	err := remote.UpdateCache(m.Config.StateDir, f.Remote.Name, func(c *remote.Cache) error {
		c.Forgotten = append(c.Forgotten, f.Remote.Incarnation)
		return nil
	})
	if err == nil {
		m.notice = news(f.Label() + " is hidden here and keeps running on " + f.Remote.Name + ". factory open " + f.Label() + " --remote " + f.Remote.Name + " lists it again.")
		m.refresh()
	}
	return err
}

func (m *Model) groupOf(f factory.Factory) selector.Group {
	if f.Remote == nil {
		return selector.Group{Name: f.RepoName(), Path: tilde(f.Repo)}
	}
	l := m.linkOf(f)
	g := selector.Group{Name: f.RepoName(), Remote: f.Remote.Name, Link: l.state(m.Now()).String()}
	if g.Link == "unreachable" {
		g.Lost = theme.Age(l.lost(m.Now()))
	}
	return g
}

func besideLocal(groups []selector.Group) []selector.Group {
	var local, remotes []selector.Group
	for _, g := range groups {
		if g.Remote == "" {
			local = append(local, g)
		} else {
			remotes = append(remotes, g)
		}
	}
	var ordered []selector.Group
	for _, g := range local {
		ordered = append(ordered, g)
		for _, r := range remotes {
			if r.Name == g.Name {
				ordered = append(ordered, r)
			}
		}
	}
	for _, r := range remotes {
		if !slices.ContainsFunc(local, func(g selector.Group) bool { return g.Name == r.Name }) {
			ordered = append(ordered, r)
		}
	}
	return ordered
}

func (m *Model) keysContext() dialog.KeysContext {
	return dialog.KeysContext{Remotes: len(m.Config.Remotes) > 0}
}

func (m *Model) remotesContext() dialog.RemotesContext {
	c := dialog.RemotesContext{Default: m.Config.DefaultRemote, Push: m.Config.Push}
	for _, it := range localOnly(m.snap).Items {
		if !it.Factory.Archived() {
			c.Local++
		}
	}
	for _, name := range m.remoteNames() {
		row := dialog.RemoteRow{Name: name, Endpoint: remote.Transport{Remote: m.Config.Remotes[name]}.Endpoint(), Link: m.link(name).state(m.Now()).String()}
		row.PushPending = m.Config.StateDir != "" && remote.PushPending(m.Config.StateDir, name, m.Config.Push)
		for _, it := range m.snap.Items {
			if it.Factory.Where() != name || it.Factory.Archived() {
				continue
			}
			row.Factories++
			if it.Status == factory.NeedsYou {
				row.NeedsYou++
			}
		}
		c.Remotes = append(c.Remotes, row)
	}
	return c
}

func (m *Model) reloadRemotes() {
	fresh, err := state.LoadConfig()
	if err != nil {
		m.notice = problem(err.Error())
		return
	}
	m.Config.Machine = fresh.Machine
	m.refresh()
}

func (m *Model) linkLabels() string {
	var b strings.Builder
	now := m.Now()
	for _, name := range m.remoteNames() {
		l := m.link(name)
		switch l.state(now) {
		case connected:
			fmt.Fprintf(&b, "#[fg=#7dcfff]%s%s  ", m.Icons.Icon("remote"), name)
		case connecting:
			fmt.Fprintf(&b, "#[fg=#7dcfff]%s%s #[fg=#565f89]connecting…  ", m.Icons.Icon("remote"), name)
		default:
			fmt.Fprintf(&b, "#[fg=#565f89]%s%s unreachable %s · retrying  ", m.Icons.Icon("lost"), name, theme.Age(l.lost(now)))
		}
	}
	return b.String()
}

func (m *Model) drawRemote(c *theme.Canvas, it Item) {
	f := it.Factory
	l := m.linkOf(f)
	now := m.Now()
	x := max((c.W-100)/2, 1)
	y := max(c.H/2-6, 1)
	width := min(98, c.W-x-1)
	put := func(text string, ink theme.Ink) {
		for _, line := range theme.Wrap(text, width) {
			c.Put(x, y, line, ink)
			y++
		}
	}
	if l.state(now) == unreachable {
		c.Put(x, y, m.Icons.Icon("lost")+f.Remote.Name+" is unreachable", theme.Bright.B())
		y += 2
		put(f.Label()+" keeps running there: its tmux session lives on "+f.Remote.Name+", not on this laptop.", theme.Text)
		put("This view reattaches by itself when the link is back. Nothing is lost while it is away.", theme.Text)
		y++
		if !l.contact.IsZero() {
			m.field(c, x, &y, "last contact", fmt.Sprintf("%s, %s ago", l.contact.Local().Format("15:04"), theme.Age(now.Sub(l.contact))))
		}
		m.field(c, x, &y, "last state", lastState(f, m.wires[f.Key()].Status))
		m.field(c, x, &y, "link", remote.Transport{Remote: m.Config.Remotes[f.Remote.Name]}.Endpoint()+" · "+l.problem)
		y++
		end := c.Put(x, y, "factory doctor --remote "+f.Remote.Name, theme.Key)
		c.Put(end+2, y, "says what is wrong with the link", theme.Muted)
		return
	}
	switch w := m.wires[f.Key()]; {
	case !f.Remote.Creating.IsZero():
		c.Put(x, y, m.Icons.Icon("starting")+"Creating "+f.Label()+" on "+f.Remote.Name, theme.Bright.B())
		y += 2
		put(f.Remote.Name+" clones or fetches the repo first, then starts the factory in its own tmux session.", theme.Text)
	case w.Pane == nil:
		c.Put(x, y, m.Icons.Icon(theme.StatusIcon(it.Status))+"No pane for "+f.Label()+" on "+f.Remote.Name, theme.Bright.B())
		y += 2
		put("Its session pane on "+f.Remote.Name+" is closed. w wakes it there.", theme.Text)
	case m.attachProblem(it) != "":
		c.Put(x, y, m.Icons.Icon("warn")+"Can't attach to "+f.Label()+" on "+f.Remote.Name, theme.Bright.B())
		y += 2
		put(m.attachProblem(it), theme.Text)
		put("The hub tries again in a few seconds.", theme.Muted)
	default:
		c.Put(x, y, m.Icons.Icon("remote")+"Attaching to "+f.Label()+" on "+f.Remote.Name+"…", theme.Bright.B())
	}
}

func (m *Model) field(c *theme.Canvas, x int, y *int, name, value string) {
	c.Put(x, *y, name, theme.Quiet)
	c.PutMax(x+14, *y, value, theme.Text, c.W-x-15)
	*y++
}

func lastState(f factory.Factory, status factory.Status) string {
	text := "no state yet"
	if f.State != nil {
		text = fmt.Sprintf("%s in %s", status, f.State.Stage)
		if n := len(f.State.OpenAttention()); n > 0 {
			text += fmt.Sprintf(", %d open item", n)
			if n > 1 {
				text += "s"
			}
		}
	}
	return text
}

func (m *Model) knownElsewhere() []dialog.Existing {
	var known []dialog.Existing
	listed := map[string]bool{}
	for _, it := range m.snap.Items {
		f := it.Factory
		stage := ""
		if f.State != nil {
			stage = f.State.Stage
		}
		listed[f.Where()+"|"+f.Label()] = true
		known = append(known, dialog.Existing{Key: f.Key(), Worktree: f.Slug, Label: f.Label(), Status: it.Status.String(), Stage: stage, Archived: f.Archived(), Retired: f.Remote == nil && retired(f), Remote: f.Where()})
	}
	for _, name := range m.remoteNames() {
		for _, id := range m.idsOn(name) {
			if !listed[name+"|"+id] {
				known = append(known, dialog.Existing{Worktree: id, Label: id, Remote: name})
			}
		}
	}
	return known
}

func (m *Model) idsOn(name string) []string {
	var ids []string
	if l := m.link(name); l.state(m.Now()) == connected {
		for _, w := range l.view.Factories {
			ids = append(ids, w.ID)
		}
		return ids
	}
	cache, _ := remote.LoadCache(m.Config.StateDir, name)
	for _, l := range cache.Inventory {
		ids = append(ids, l.ID)
	}
	return ids
}

func (m *Model) createRemote(r dialog.NewResult) error {
	if m.link(r.Remote).state(m.Now()) == unreachable {
		return fmt.Errorf("%s is unreachable, so %s was not created. Nothing changed.", r.Remote, r.Slug)
	}
	cfg := m.Config
	create := remote.Create{ID: r.Slug, Prompt: r.Prompt, Pipeline: r.Pipeline, Stages: r.Stages, Agents: r.Agents, Supervisor: r.Supervisor}
	m.notice = news("Creating " + r.Slug + " on " + r.Remote + ". It appears in the list while the server clones.")
	m.later = append(m.later, func() tea.Msg {
		var last string
		err := remote.CreateOn(cfg, r.Remote, r.Repo, "", create, func(line string) { last = line })
		if err != nil {
			return noticeMsg(problem(err.Error()))
		}
		return noticeMsg(news(last))
	})
	return nil
}
