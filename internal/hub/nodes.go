package hub

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/google/uuid"

	"github.com/afonsodelgado/factory/internal/factory"
	"github.com/afonsodelgado/factory/internal/gitclone"
	"github.com/afonsodelgado/factory/internal/host"
	"github.com/afonsodelgado/factory/internal/node"
	"github.com/afonsodelgado/factory/internal/state"
	"github.com/afonsodelgado/factory/internal/tmux"
	"github.com/afonsodelgado/factory/internal/ui/dialog"
	"github.com/afonsodelgado/factory/internal/ui/panel"
	"github.com/afonsodelgado/factory/internal/ui/selector"
	"github.com/afonsodelgado/factory/internal/ui/theme"
)

const (
	rosterWait       = 5 * time.Second
	nodeRetry        = 15 * time.Second
	createWait       = 10 * time.Minute
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

type attachedMsg struct {
	key, role, token string
	words            []string
	err              error
}

type attempt struct {
	at      time.Time
	busy    bool
	problem string
}

var pastTense = map[string]string{"wake": "woken", "pause": "paused", "archive": "archived", "unarchive": "unarchived", "delete": "deleted"}

func withFullStop(sentence string) string {
	if strings.HasSuffix(sentence, ".") {
		return sentence
	}
	return sentence + "."
}

func localOnly(s Snapshot) Snapshot {
	s.Items = slices.DeleteFunc(slices.Clone(s.Items), func(it Item) bool { return it.Factory.Remote != nil })
	return s
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

func (m *Model) reloadMachine() {
	fresh, err := state.LoadConfig()
	if err != nil {
		m.notice = problem(err.Error())
		return
	}
	m.Config.Machine = fresh.Machine
	m.refresh()
}

type nodeLink struct {
	member   node.RosterMember
	snap     node.Snapshot
	answered bool
	contact  time.Time
	since    time.Time
	problem  string
	stop     context.CancelFunc
}

func (l *nodeLink) short() string { return shortName(l.member.Name) }

func shortName(name string) string {
	short, _, _ := strings.Cut(name, ".")
	return short
}

func (l *nodeLink) last() time.Time {
	if l.contact.IsZero() {
		return l.since
	}
	return l.contact
}

func (l *nodeLink) state(now time.Time) linkState {
	switch {
	case l.answered && l.problem == "":
		return connected
	case now.Sub(l.last()) < unreachableAfter:
		return connecting
	}
	return unreachable
}

type rosterMsg struct {
	roster node.Roster
	err    error
}

type nodeMsg struct {
	id   string
	snap *node.Snapshot
	err  error
}

func (m *Model) peer(name string) *node.Client {
	if m.Peers != nil {
		return m.Peers(name)
	}
	return node.Remote(name, node.PeerHTTP())
}

func (m *Model) pollRoster() tea.Cmd {
	if m.Roster == nil {
		return nil
	}
	read := m.Roster
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), rosterWait)
		defer cancel()
		roster, err := read(ctx)
		return rosterMsg{roster: roster, err: err}
	}
}

func (m *Model) waitNodes() tea.Cmd {
	feed := m.nodeFeed
	return func() tea.Msg { return <-feed }
}

func (m *Model) rosterArrived(msg rosterMsg) {
	if msg.err != nil {
		return
	}
	first := m.roster == nil
	m.roster = &msg.roster
	listed := map[string]bool{}
	for _, member := range msg.roster.Members {
		if member.Self {
			continue
		}
		listed[member.ID] = true
		if l, ok := m.nodes[member.ID]; ok {
			l.member = member
			continue
		}
		l := &nodeLink{member: member, since: m.Now()}
		m.nodes[member.ID] = l
		m.follow(l)
		if !first {
			m.notice = news(l.short() + " joined your nodes. Its factories show here under ↔ " + l.short() + ".")
		}
	}
	for id, l := range m.nodes {
		if !listed[id] {
			l.stop()
			delete(m.nodes, id)
		}
	}
	m.refresh()
}

func (m *Model) follow(l *nodeLink) {
	ctx, cancel := context.WithCancel(context.Background())
	l.stop = cancel
	go followNode(ctx, l.member.ID, m.peer(l.member.Name), m.nodeFeed)
}

func followNode(ctx context.Context, id string, c *node.Client, feed chan<- nodeMsg) {
	send := func(msg nodeMsg) {
		select {
		case feed <- msg:
		case <-ctx.Done():
		}
	}
	for {
		err := c.Follow(ctx, func(s node.Snapshot) { send(nodeMsg{id: id, snap: &s}) }, func() { send(nodeMsg{id: id}) })
		if ctx.Err() != nil {
			return
		}
		send(nodeMsg{id: id, err: err})
		select {
		case <-ctx.Done():
			return
		case <-time.After(nodeRetry):
		}
	}
}

func (m *Model) nodeArrived(msg nodeMsg) {
	l, ok := m.nodes[msg.id]
	if !ok {
		return
	}
	if msg.err != nil {
		l.problem = msg.err.Error()
		m.refresh()
		return
	}
	l.contact, l.problem = m.Now(), ""
	if msg.snap != nil {
		l.snap, l.answered = *msg.snap, true
		m.refresh()
	}
	m.recheckSends(msg.id)
}

func (m *Model) sortedNodes() []*nodeLink {
	links := slices.Collect(maps.Values(m.nodes))
	slices.SortFunc(links, func(a, b *nodeLink) int { return strings.Compare(a.member.Name, b.member.Name) })
	return links
}

func (m *Model) nodeItems() []Item {
	now := m.Now()
	m.nodeFactories = map[string]host.LocalFactory{}
	var items []Item
	for _, l := range m.sortedNodes() {
		for _, lf := range l.snap.Factories {
			it := nodeItem(l, lf)
			if l.state(now) == unreachable {
				it.Status, it.Unblocked = factory.Unreachable, factory.Unreachable
			}
			m.nodeFactories[it.Factory.Key()] = lf
			items = append(items, it)
		}
	}
	return items
}

func nodeItem(l *nodeLink, lf host.LocalFactory) Item {
	slug := strings.TrimPrefix(lf.Key, lf.Repo+":")
	f := factory.Factory{
		Repo: lf.Repo, Slug: slug, ID: lf.ID, Entry: &state.Entry{Title: lf.Title},
		Remote: &factory.Location{Name: l.short(), Node: l.member.ID, Incarnation: lf.Incarnation, Repo: lf.RepoName},
	}
	status, _ := factory.ParseStatus(lf.Status)
	unreported := lf.Status == factory.LegacyFailedToStart || slices.ContainsFunc(lf.Items, func(li host.LocalItem) bool { return li.Kind == "silent" })
	return Item{Factory: f, Status: status, Unblocked: status, Unreported: unreported}
}

func (m *Model) nodeOf(f factory.Factory) (*nodeLink, bool) {
	l, ok := m.nodes[f.Remote.Node]
	return l, ok
}

func (m *Model) nodeUnreachable(f factory.Factory) bool {
	l, ok := m.nodeOf(f)
	return !ok || l.state(m.Now()) != connected
}

func (m *Model) nodeGroup(f factory.Factory) selector.Group {
	g := selector.Group{Name: f.RepoName(), Remote: f.Remote.Name, Node: f.Remote.Node, Link: unreachable.String()}
	l, ok := m.nodeOf(f)
	if ok {
		g.Link = l.state(m.Now()).String()
	}
	if ok && g.Link == "unreachable" {
		g.Lost = theme.Age(m.Now().Sub(l.last()))
	}
	return g
}

func (m *Model) nodeLabels() string {
	var b strings.Builder
	now := m.Now()
	for _, l := range m.sortedNodes() {
		switch l.state(now) {
		case connected:
			fmt.Fprintf(&b, "#[fg=#7dcfff]%s%s  ", m.Icons.Icon("remote"), l.short())
		case connecting:
			fmt.Fprintf(&b, "#[fg=#7dcfff]%s%s #[fg=#565f89]connecting…  ", m.Icons.Icon("remote"), l.short())
		default:
			fmt.Fprintf(&b, "#[fg=#565f89]%s%s lost %s  ", m.Icons.Icon("lost"), l.short(), theme.Age(now.Sub(l.last())))
		}
	}
	return b.String()
}

func (m *Model) nodesContext() dialog.NodesContext {
	var c dialog.NodesContext
	if m.roster != nil {
		c.Leaving = m.roster.Leaving
	}
	for _, l := range m.sortedNodes() {
		row := dialog.NodeRow{ID: l.member.ID, Name: l.member.Name, OS: l.member.OS, Version: l.snap.Version, Factories: len(l.snap.Factories), Contact: l.contact, Since: l.since}
		for _, f := range l.snap.Factories {
			if !f.NeedsSince.IsZero() {
				row.NeedsYou++
			}
		}
		c.Others = append(c.Others, row)
	}
	return c
}

func (m *Model) nodeChoices() []dialog.NodeChoice {
	var choices []dialog.NodeChoice
	now := m.Now()
	for _, l := range m.sortedNodes() {
		choices = append(choices, dialog.NodeChoice{Name: l.member.Name, Unreachable: l.state(now) != connected})
	}
	return choices
}

func (m *Model) nodeAct(it Item, action string) error {
	f := it.Factory
	l, ok := m.nodeOf(f)
	if !ok || l.state(m.Now()) != connected {
		return fmt.Errorf("%s is unreachable, so %s was not %s. Nothing changed.", f.Remote.Name, f.Label(), pastTense[action])
	}
	lf := m.nodeFactories[f.Key()]
	client, short, label := m.peer(l.member.Name), l.short(), f.Label()
	send := func(ctx context.Context) error { return lifecycleOn(ctx, client, lf, action) }
	switch action {
	case "archive":
		send = func(ctx context.Context) error {
			return client.Request(ctx, "archive", uuid.NewString(), host.ArchiveRequest{Key: lf.Key}, nil)
		}
	case "unarchive":
		send = func(ctx context.Context) error {
			return client.Request(ctx, "unarchive", uuid.NewString(), map[string]string{"Key": lf.Key}, nil)
		}
	case "delete":
		digest := m.confirmedWork
		send = func(ctx context.Context) error {
			return client.Request(ctx, "delete", uuid.NewString(), host.DeleteRequest{Repo: lf.Repo, Slug: it.Factory.Slug, Work: digest}, nil)
		}
	}
	m.later = append(m.later, func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), remoteWait)
		defer cancel()
		err := send(ctx)
		var uncertain node.UncertainError
		switch {
		case errors.As(err, &uncertain):
			return noticeMsg(problem(fmt.Sprintf("%s did not answer, so whether %s was %s is unknown. Wait for the list to refresh before trying again.", short, label, pastTense[action])))
		case err != nil:
			return noticeMsg(problem(fmt.Sprintf("%s was not %s on %s: %s", label, pastTense[action], short, withFullStop(err.Error()))))
		}
		return noticeMsg(news(fmt.Sprintf("%s was %s on %s.", label, pastTense[action], short)))
	})
	return nil
}

func actionsOf(lf host.LocalFactory) string {
	return "/api/factories/" + url.PathEscape(lf.Key) + "/actions"
}

func lifecycleOn(ctx context.Context, client *node.Client, lf host.LocalFactory, action string) error {
	return actOn(ctx, client, lf, node.ActionBody{Action: action})
}

func actOn(ctx context.Context, client *node.Client, lf host.LocalFactory, body node.ActionBody) error {
	var receipt host.Receipt
	body.Operation, body.Incarnation, body.Generation = uuid.NewString(), lf.Incarnation, lf.Generation
	if err := client.Do(ctx, http.MethodPost, actionsOf(lf), body, &receipt); err != nil {
		return err
	}
	if receipt.Status == host.StatusRefused || receipt.Status == host.StatusStale {
		return errors.New(receipt.Reason)
	}
	return nil
}

func (m *Model) askComments() tea.Cmd {
	it, ok := m.selectedItem()
	if !ok {
		return nil
	}
	if it.Factory.Remote != nil {
		if l, ok := m.nodeOf(it.Factory); ok && !l.snap.Supports(node.CanToggleComments) {
			m.notice = problem(fmt.Sprintf("%s runs factory %s, which cannot change comment watching on a running factory. Nothing was sent.", l.short(), l.snap.Version))
			return nil
		}
	}
	watching, can := m.commentsToggle(it)
	switch {
	case m.unverified(it) != "":
		m.notice = problem(heldUnchecked(it, m.unverified(it)))
		return nil
	case !can:
		m.notice = problem(it.Factory.Label() + " cannot change comment watching now: it needs a running factory, not done or archived, with its supervisor open.")
		return nil
	}
	return m.openDialog("comments", dialog.CommentsContext{Key: it.Factory.Key(), Label: it.Factory.Label(), On: !watching})
}

func (m *Model) commentsToggle(it Item) (watching, can bool) {
	if it.Factory.Remote == nil {
		return it.Factory.State != nil && it.Factory.State.WatchComments, host.CanToggleComments(it)
	}
	lf := m.nodeFactories[it.Factory.Key()]
	return lf.WatchComments, lf.CanToggleComments
}

func commentsOption(on bool) string {
	if on {
		return "on"
	}
	return "off"
}

func (m *Model) toggleComments(r dialog.CommentsResult) error {
	it, ok := m.snap.Find(r.Key)
	if !ok {
		return fmt.Errorf("the hub does not list %s", r.Key)
	}
	option := commentsOption(r.On)
	if it.Factory.Remote != nil {
		return m.commentsOnNode(it, option)
	}
	var receipt host.Receipt
	request := host.ActionRequest{Operation: uuid.NewString(), Device: host.SourceHub, Factory: it.Factory.Key(), Incarnation: host.Incarnation(it.Factory), Action: host.ActComments, Option: option}
	if err := m.call("act", request.Operation, request, &receipt); err != nil {
		return err
	}
	if receipt.Status == host.StatusRefused || receipt.Status == host.StatusStale {
		return errors.New(receipt.Reason)
	}
	m.notice = news("Asked " + it.Factory.Label() + "'s supervisor to turn review comment watching " + option + ".")
	return nil
}

func (m *Model) commentsOnNode(it Item, option string) error {
	f := it.Factory
	l, ok := m.nodeOf(f)
	if !ok || l.state(m.Now()) != connected {
		return fmt.Errorf("%s is unreachable, so comment watching on %s did not change. Nothing was sent.", f.Remote.Name, f.Label())
	}
	lf, client, short, label := m.nodeFactories[f.Key()], m.peer(l.member.Name), l.short(), f.Label()
	m.later = append(m.later, func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), remoteWait)
		defer cancel()
		if err := actOn(ctx, client, lf, node.ActionBody{Action: host.ActComments, Option: option}); err != nil {
			return noticeMsg(problem(fmt.Sprintf("Comment watching on %s did not change on %s: %s", label, short, withFullStop(err.Error()))))
		}
		return noticeMsg(news(fmt.Sprintf("Asked %s's supervisor on %s to turn review comment watching %s.", label, short, option)))
	})
	return nil
}

type nodeRemovalMsg struct {
	key     string
	removal host.Removal
	err     error
}

func (m *Model) askNodeDelete(it Item) tea.Cmd {
	f := it.Factory
	l, ok := m.nodeOf(f)
	if !ok || l.state(m.Now()) != connected {
		m.notice = problem(fmt.Sprintf("%s is unreachable, so %s was not deleted. Nothing changed.", f.Remote.Name, f.Label()))
		return nil
	}
	client, key := m.peer(l.member.Name), f.Key()
	request := host.DeleteRequest{Repo: f.Repo, Slug: f.Slug}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), remoteWait)
		defer cancel()
		var r host.Removal
		err := client.Request(ctx, "removal", "", request, &r)
		return nodeRemovalMsg{key: key, removal: r, err: err}
	}
}

func (m *Model) nodeRemovalArrived(msg nodeRemovalMsg) tea.Cmd {
	it, ok := m.snap.Find(msg.key)
	if !ok {
		return nil
	}
	if msg.err != nil {
		m.notice = problem(msg.err.Error())
		return nil
	}
	f, r := it.Factory, msg.removal
	m.confirmedWork = r.Digest
	c := dialog.DeleteContext{Key: f.Key(), Label: f.Label(), Title: f.Title(), Remote: f.Remote.Name, Repo: f.RepoName(), Worktree: f.Remote.Name + ":" + r.Removal.Worktree, Branches: r.Removal.Branches, Dirty: r.Work.Dirty, Unpushed: r.Work.Unpushed}
	return m.openDialog("delete", c)
}

func (m *Model) nodeNamed(name string) (*nodeLink, bool) {
	for _, l := range m.nodes {
		if l.member.Name == name {
			return l, true
		}
	}
	return nil, false
}

func (m *Model) createOnNode(r dialog.NewResult) error {
	l, ok := m.nodeNamed(r.Node)
	if !ok {
		return fmt.Errorf("%s is not in the node list any more, so %s was not created. Nothing changed.", shortName(r.Node), r.Slug)
	}
	short := l.short()
	if l.state(m.Now()) != connected {
		return fmt.Errorf("%s is unreachable, so %s was not created. Nothing changed.", short, r.Slug)
	}
	gitURL, err := gitclone.RemoteURL(r.Repo)
	if err != nil {
		return err
	}
	client := m.peer(l.member.Name)
	create := host.CreateRequest{Slug: r.Slug, Prompt: r.Prompt, Pipeline: r.Pipeline, Stages: r.Stages, Agents: r.Agents, Supervisor: r.Supervisor, WatchComments: r.WatchComments}
	creating := "Creating " + r.Slug + " on " + short + ". It appears under ↔ " + short + " once it starts."
	if r.WatchComments && !l.snap.Supports(node.CanWatchComments) {
		create.WatchComments = false
		creating += fmt.Sprintf(" %s runs factory %s, which does not record comment watching, so %s leaves review comments to you.", short, l.snap.Version, r.Slug)
	}
	m.notice = news(creating)
	m.later = append(m.later, func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), createWait)
		defer cancel()
		repo, err := repoOn(ctx, client, gitURL, short)
		if err != nil {
			return noticeMsg(problem(fmt.Sprintf("%s was not created on %s: %s", r.Slug, short, withFullStop(err.Error()))))
		}
		create.Repo = repo
		if err := client.Request(ctx, "create", uuid.NewString(), create, nil); err != nil {
			return noticeMsg(problem(fmt.Sprintf("%s was not created on %s: %s", r.Slug, short, withFullStop(err.Error()))))
		}
		return noticeMsg(news(r.Slug + " started on " + short + "."))
	})
	return nil
}

func repoOn(ctx context.Context, client *node.Client, gitURL, short string) (string, error) {
	var repos map[string]string
	if err := client.Do(ctx, http.MethodGet, "/api/repos", nil, &repos); err != nil {
		return "", err
	}
	if repo, present := gitclone.Find(repos, gitURL); present {
		return repo, nil
	}
	return "", errors.New(node.MissingRepo(gitURL, short).Sentence())
}

func (m *Model) nodeTargets(it Item) (string, string) {
	target := m.slotViewer()
	lf := m.nodeFactories[it.Factory.Key()]
	if lf.Pane == "" || m.nodeUnreachable(it.Factory) {
		return target, ""
	}
	p, ok := m.proxyPane(it.Factory.Key(), "", lf.Pane)
	if !ok {
		return target, ""
	}
	roomForTwo := m.window[0] == 0 || m.window[0] > NarrowAt
	if lf.StagePane == "" || !roomForTwo {
		return p.ID, ""
	}
	stage, ok := m.proxyPane(it.Factory.Key(), lf.StageRole, lf.StagePane)
	if !ok {
		return p.ID, ""
	}
	return p.ID, stage.ID
}

func (m *Model) attachOnNode(it Item) tea.Cmd {
	lf := m.nodeFactories[it.Factory.Key()]
	if lf.Pane == "" || m.nodeUnreachable(it.Factory) {
		return nil
	}
	cmds := []tea.Cmd{m.attachNodePane(it, lf, "", lf.Pane)}
	if lf.StagePane != "" {
		cmds = append(cmds, m.attachNodePane(it, lf, lf.StageRole, lf.StagePane))
	}
	return tea.Batch(cmds...)
}

func (m *Model) attachNodePane(it Item, lf host.LocalFactory, role, token string) tea.Cmd {
	key := it.Factory.Key()
	if !m.mayAttach(key, role, token) {
		return nil
	}
	l, _ := m.nodeOf(it.Factory)
	client := m.peer(l.member.Name)
	to := node.StreamTarget{Machine: l.member.Name, User: node.LoginUser(l.snap), Label: it.Factory.Label()}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), remoteWait)
		defer cancel()
		words, err := node.AttachCommand(ctx, client, to, host.AttachRequest{Key: lf.Key, Role: role, Token: token})
		return attachedMsg{key: key, role: role, token: token, words: words, err: err}
	}
}

func (m *Model) streamNotice() notice {
	it, ok := m.snap.Find(m.selected)
	if !ok || it.Factory.Remote == nil {
		return notice{}
	}
	l, ok := m.nodeOf(it.Factory)
	if !ok || !l.snap.Supports(node.CanStream) {
		return notice{}
	}
	return news("live from " + l.short() + " over the tailnet")
}

func (m *Model) nodeAttachProblem(it Item) string {
	lf := m.nodeFactories[it.Factory.Key()]
	if lf.Pane == "" {
		return ""
	}
	return m.attempts[it.Factory.Key()+"||"+lf.Pane].problem
}

func (m *Model) drawNode(c *theme.Canvas, it Item) {
	f := it.Factory
	l, _ := m.nodeOf(f)
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
	lf := m.nodeFactories[f.Key()]
	short := f.Remote.Name
	switch {
	case l == nil || l.state(now) == unreachable:
		c.Put(x, y, m.Icons.Icon("lost")+short+" is unreachable", theme.Bright.B())
		y += 2
		put(f.Label()+" keeps running there if "+short+" is on: its tmux session lives on "+short+", not on this machine.", theme.Text)
		put("This view reattaches by itself when "+short+" answers. Nothing is lost while it is away.", theme.Text)
		y++
		if l != nil && !l.contact.IsZero() {
			m.field(c, x, &y, "last contact", fmt.Sprintf("%s, %s ago", l.contact.Local().Format("15:04"), theme.Age(now.Sub(l.contact))))
		}
		m.field(c, x, &y, "last state", strings.TrimSpace(lf.Status+" in "+lf.Stage))
		if l != nil {
			m.field(c, x, &y, "address", "https://"+l.member.Name+" · no answer")
		}
		y++
		end := c.Put(x, y, "N", theme.Key)
		c.Put(end+2, y, "Nodes shows what Tailscale knows about "+short, theme.Muted)
	case lf.Pane == "":
		c.Put(x, y, m.Icons.Icon(theme.StatusIcon(it.Status))+"No pane for "+f.Label()+" on "+short, theme.Bright.B())
		y += 2
		put("Its session pane on "+short+" is closed. w wakes it there.", theme.Text)
	case m.nodeAttachProblem(it) != "":
		c.Put(x, y, m.Icons.Icon("warn")+"Can't attach to "+f.Label()+" on "+short, theme.Bright.B())
		y += 2
		put(m.nodeAttachProblem(it), theme.Text)
		put("The hub tries again in a few seconds.", theme.Muted)
	default:
		c.Put(x, y, m.Icons.Icon("remote")+"Attaching to "+f.Label()+" on "+short+"…", theme.Bright.B())
	}
}

type nodeSend struct {
	node      string
	name      string
	factory   string
	item      string
	since     time.Time
	draft     string
	operation string
	status    string
	checking  bool
}

type nodeAnsweredMsg struct {
	key       string
	operation string
	receipt   host.Receipt
	err       error
	lookup    bool
}

func (m *Model) nodeAttention(it Item) []panel.Item {
	lf := m.nodeFactories[it.Factory.Key()]
	status, _ := factory.ParseStatus(lf.Status)
	items := make([]panel.Item, len(lf.Items))
	for i, li := range lf.Items {
		items[i] = presented(li.AttentionItem, status)
	}
	return items
}

func presented(item panel.Item, status factory.Status) panel.Item {
	switch item.Kind {
	case "permission":
		item.ID, item.Body, item.Action = "", host.PermissionBody, host.AnswerInSession
	case "prompt":
		item.ID, item.Action = "", host.AnswerItInSession
	case "silent":
		item.ID, item.Action = "", host.OpenSession
	case "asleep":
		item.Status, item.Action = status, host.WakeKey
	}
	return item
}

func (m *Model) openOnNode(key, id string, since time.Time) (host.LocalItem, bool) {
	for _, li := range m.nodeFactories[key].Items {
		if li.ID == id && li.Since.Equal(since) {
			return li, true
		}
	}
	return host.LocalItem{}, false
}

func (m *Model) sentToNode(it Item, item panel.Item) (panel.Progress, bool) {
	if li, ok := m.openOnNode(it.Factory.Key(), item.ID, item.Since); ok && li.Answer != nil {
		return receiptProgress(li.Answer.Status), true
	}
	s, ok := m.nodeSends[draftKey(it.Factory, item.ID)]
	underway := ok && s.since.Equal(item.Since) && s.status != host.StatusUnknown
	return receiptProgress(s.status), underway
}

func receiptProgress(status string) panel.Progress {
	switch status {
	case host.StatusQueued:
		return panel.Queued
	case host.StatusDelivered:
		return panel.Answered
	case host.StatusDismissed:
		return panel.Dismissed
	}
	return panel.Sending
}

func (m *Model) nodeSendable(it Item, item panel.Item) host.Eligibility {
	if m.nodeUnreachable(it.Factory) {
		return host.Eligibility{Reason: it.Factory.Remote.Name + " is unreachable, so nothing can be sent there now."}
	}
	li, _ := m.openOnNode(it.Factory.Key(), item.ID, item.Since)
	return li.Send
}

func (m *Model) answerOnNode(it Item, body node.ActionBody) {
	f := it.Factory
	key := draftKey(f, body.Item)
	l, ok := m.nodeOf(f)
	if !ok || l.state(m.Now()) != connected {
		m.panel.refused[key] = "Not sent. " + f.Remote.Name + " is unreachable."
		return
	}
	lf := m.nodeFactories[f.Key()]
	body.Operation, body.Incarnation, body.Generation = uuid.NewString(), lf.Incarnation, lf.Generation
	m.nodeSends[key] = nodeSend{node: l.member.ID, name: l.short(), factory: f.Key(), item: body.Item, since: body.Since, draft: m.draftPath(f, body.Item), operation: body.Operation, status: host.StatusSending, checking: true}
	client := m.peer(l.member.Name)
	m.later = append(m.later, func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), remoteWait)
		defer cancel()
		var receipt host.Receipt
		err := client.Do(ctx, http.MethodPost, actionsOf(lf), body, &receipt)
		return nodeAnsweredMsg{key: key, operation: body.Operation, receipt: receipt, err: err}
	})
}

func (m *Model) nodeAnswered(msg nodeAnsweredMsg) {
	s, ok := m.nodeSends[msg.key]
	if !ok || s.operation != msg.operation {
		return
	}
	s.checking = false
	var failure node.StatusError
	answeredOverHTTP := errors.As(msg.err, &failure)
	settledThere := msg.receipt.Status == host.StatusRefused || msg.receipt.Status == host.StatusStale
	switch {
	case msg.lookup && msg.receipt.Status == host.StatusDelivered:
		m.forgetSend(msg.key, s)
	case msg.err == nil && msg.receipt.Status != host.StatusUnknown && !settledThere:
		s.status = msg.receipt.Status
		m.nodeSends[msg.key] = s
		delete(m.panel.refused, msg.key)
	case settledThere:
		m.endSend(msg.key, msg.receipt.Reason)
	case msg.err == nil:
		m.endSend(msg.key, "Outcome unknown: "+msg.receipt.Reason)
	case msg.lookup && answeredOverHTTP && failure.Status == http.StatusNotFound:
		m.endSend(msg.key, "Not sent. "+s.name+" has no record of it.")
	case !msg.lookup && answeredOverHTTP && failure.Status < http.StatusInternalServerError && failure.Status != http.StatusConflict:
		m.endSend(msg.key, "Not sent. "+withFullStop(failure.Message))
	default:
		s.status = host.StatusUnknown
		m.nodeSends[msg.key] = s
		m.panel.refused[msg.key] = "Outcome unknown: " + s.name + " did not answer, so whether it arrived is unknown. The hub asks " + s.name + " again when it answers; look at the session before sending again."
		if !msg.lookup {
			m.later = append(m.later, m.lookUpSend(msg.key, s))
		}
	}
}

func (m *Model) endSend(key, outcome string) {
	delete(m.nodeSends, key)
	m.panel.refused[key] = outcome
}

func (m *Model) lookUpSend(key string, s nodeSend) tea.Cmd {
	l, ok := m.nodes[s.node]
	if !ok {
		return nil
	}
	client := m.peer(l.member.Name)
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), remoteWait)
		defer cancel()
		var receipt host.Receipt
		err := client.Do(ctx, http.MethodGet, "/api/operations/"+s.operation, nil, &receipt)
		return nodeAnsweredMsg{key: key, operation: s.operation, receipt: receipt, err: err, lookup: true}
	}
}

func (m *Model) recheckSends(id string) {
	for key, s := range m.nodeSends {
		li, open := m.openOnNode(s.factory, s.item, s.since)
		unconfirmed := s.status == host.StatusUnknown || open && li.Answer == nil
		if s.node != id || s.checking || !unconfirmed {
			continue
		}
		s.checking = true
		m.nodeSends[key] = s
		m.later = append(m.later, m.lookUpSend(key, s))
	}
}

func (m *Model) settleNodeSends() {
	for key, s := range m.nodeSends {
		if _, open := m.openOnNode(s.factory, s.item, s.since); !open {
			m.forgetSend(key, s)
		}
	}
}

func (m *Model) forgetSend(key string, s nodeSend) {
	delete(m.nodeSends, key)
	delete(m.panel.drafts, key)
	os.Remove(s.draft)
}

func (m *Model) unverified(it Item) string {
	if lf, onNode := m.nodeFactories[it.Factory.Key()]; onNode {
		return lf.Unverified
	}
	return m.holdings[it.Factory.Key()].Unverified
}

func heldUnchecked(it Item, reason string) string {
	return it.Factory.Label() + " is held but unchecked, so nothing can be done to it until its holder is confirmed: " + withFullStop(reason)
}

func (m *Model) askMove() tea.Cmd {
	it, ok := m.selectedItem()
	if !ok {
		return nil
	}
	if reason := m.unverified(it); reason != "" {
		m.notice = problem(heldUnchecked(it, reason))
		return nil
	}
	c, err := m.moveContext(it)
	if err != nil {
		m.notice = problem(err.Error())
		return nil
	}
	return m.openDialog("move", c)
}

func (m *Model) moveContext(it Item) (dialog.MoveContext, error) {
	f := it.Factory
	c := dialog.MoveContext{Key: f.Key(), Label: f.Label(), Factory: f.Key(), HolderShort: m.Config.NodeName(), Repo: f.RepoName()}
	holder := ""
	if f.Remote == nil {
		gitURL, err := gitclone.RemoteURL(f.Repo)
		if err != nil && !errors.Is(err, gitclone.ErrNoNetworkRemote) {
			return c, fmt.Errorf("%s can't move: %w.", f.Label(), err)
		}
		c.GitURL, c.NoRemote = gitURL, err != nil
	}
	if f.Remote != nil {
		l, ok := m.nodeOf(f)
		if !ok {
			return c, fmt.Errorf("%s is not in the node list any more, so %s can't move from there.", f.Remote.Name, f.Label())
		}
		lf := m.nodeFactories[f.Key()]
		if lf.RemoteProblem != "" {
			return c, fmt.Errorf("%s can't move: %s.", f.Label(), lf.RemoteProblem)
		}
		c.Factory, c.Incarnation, c.Holder, c.HolderShort, c.GitURL, c.NoRemote = lf.Key, lf.Incarnation, l.member.Name, l.short(), lf.GitURL, lf.NoRemote
		c.Lost, holder = l.state(m.Now()) == unreachable, l.member.ID
		c.Targets = append(c.Targets, dialog.NodeChoice{Name: m.selfName(), Here: true})
	}
	now := m.Now()
	for _, l := range m.sortedNodes() {
		if l.member.ID != holder {
			c.Targets = append(c.Targets, dialog.NodeChoice{Name: l.member.Name, Unreachable: l.state(now) != connected})
		}
	}
	if len(c.Targets) == 0 {
		return c, fmt.Errorf("No other node is listed, so %s has nowhere to move. N adds one.", f.Label())
	}
	return c, nil
}

func (m *Model) selfName() string {
	if m.roster != nil {
		for _, member := range m.roster.Members {
			if member.Self {
				return member.Name
			}
		}
	}
	return m.Config.NodeHostname()
}

func (m *Model) groupOf(f factory.Factory) selector.Group {
	if f.Remote != nil {
		return m.nodeGroup(f)
	}
	return selector.Group{Name: f.RepoName(), Path: tilde(f.Repo)}
}

func besideLocal(groups []selector.Group) []selector.Group {
	var local, others []selector.Group
	for _, g := range groups {
		if g.Remote == "" {
			local = append(local, g)
		} else {
			others = append(others, g)
		}
	}
	var ordered []selector.Group
	for _, g := range local {
		ordered = append(ordered, g)
		for _, r := range others {
			if r.Name == g.Name {
				ordered = append(ordered, r)
			}
		}
	}
	for _, r := range others {
		if !slices.ContainsFunc(local, func(g selector.Group) bool { return g.Name == r.Name }) {
			ordered = append(ordered, r)
		}
	}
	return ordered
}

func (m *Model) knownElsewhere() []dialog.Existing {
	var known []dialog.Existing
	for _, it := range m.snap.Items {
		f := it.Factory
		stage := ""
		if f.State != nil {
			stage = f.State.Stage
		}
		known = append(known, dialog.Existing{Key: f.Key(), Worktree: f.Slug, Label: f.Label(), Status: it.Status.String(), Stage: stage, Archived: f.Archived(), Retired: f.Remote == nil && retired(f), Remote: f.Where()})
	}
	return known
}

func (m *Model) field(c *theme.Canvas, x int, y *int, name, value string) {
	c.Put(x, *y, name, theme.Quiet)
	c.PutMax(x+14, *y, value, theme.Text, c.W-x-15)
	*y++
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

func (m *Model) attachSelected() tea.Cmd {
	it, ok := m.snap.Find(m.selected)
	if !ok || it.Factory.Remote == nil || m.Engine == nil {
		return nil
	}
	return m.attachOnNode(it)
}

func (m *Model) mayAttach(key, role, token string) bool {
	if _, ok := m.proxyPane(key, role, token); ok {
		return false
	}
	if m.attempts == nil {
		m.attempts = map[string]attempt{}
	}
	id := key + "|" + role + "|" + token
	a := m.attempts[id]
	if a.busy || m.Now().Sub(a.at) < attachRetry {
		return false
	}
	m.attempts[id] = attempt{at: m.Now(), busy: true}
	return true
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
	quoted := make([]string, len(msg.words))
	for i, w := range msg.words {
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
		if !strings.HasPrefix(p.Repo, "node:") {
			continue
		}
		if _, listed := m.snap.Find(p.Repo); !listed {
			m.Engine.TM.KillPane(p.ID)
		}
	}
}
