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
	"github.com/afonsodelgado/factory/internal/ui/dialog"
	"github.com/afonsodelgado/factory/internal/ui/panel"
	"github.com/afonsodelgado/factory/internal/ui/selector"
	"github.com/afonsodelgado/factory/internal/ui/theme"
)

const (
	rosterWait = 5 * time.Second
	nodeRetry  = 15 * time.Second
	createWait = 10 * time.Minute
)

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
	return Item{Factory: f, Status: status, Unblocked: status}
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
	var receipt host.Receipt
	body := node.ActionBody{Operation: uuid.NewString(), Incarnation: lf.Incarnation, Action: action}
	if err := client.Do(ctx, http.MethodPost, actionsOf(lf), body, &receipt); err != nil {
		return err
	}
	if receipt.Status == host.StatusRefused || receipt.Status == host.StatusStale {
		return errors.New(receipt.Reason)
	}
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
	gitURL, err := gitclone.RemoteURL(r.Repo, "")
	if err != nil {
		return err
	}
	client := m.peer(l.member.Name)
	create := host.CreateRequest{Slug: r.Slug, Prompt: r.Prompt, Pipeline: r.Pipeline, Stages: r.Stages, Agents: r.Agents, Supervisor: r.Supervisor}
	m.notice = news("Creating " + r.Slug + " on " + short + ". It appears under ↔ " + short + " once it starts.")
	m.later = append(m.later, func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), createWait)
		defer cancel()
		repo, err := repoOn(ctx, client, gitURL)
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

func repoOn(ctx context.Context, client *node.Client, gitURL string) (string, error) {
	var repos map[string]string
	if err := client.Do(ctx, http.MethodGet, "/api/repos", nil, &repos); err != nil {
		return "", err
	}
	if repo, present := repos[gitURL]; present {
		return repo, nil
	}
	var cloned node.Cloned
	err := client.Do(ctx, http.MethodPost, "/api/clone", node.CloneRequest{Operation: uuid.NewString(), URL: gitURL}, &cloned)
	return cloned.Repo, err
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
	client, name := m.peer(l.member.Name), l.member.Name
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), remoteWait)
		defer cancel()
		words, err := node.AttachCommand(ctx, client, name, host.AttachRequest{Key: lf.Key, Role: role, Token: token})
		return attachedMsg{key: key, role: role, token: token, words: words, err: err}
	}
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
	body.Operation, body.Incarnation = uuid.NewString(), lf.Incarnation
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
