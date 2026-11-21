package hub

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/afonsodelgado/factory/internal/factory"
	"github.com/afonsodelgado/factory/internal/host"
	"github.com/afonsodelgado/factory/internal/node"
	"github.com/afonsodelgado/factory/internal/state"
	"github.com/afonsodelgado/factory/internal/tmux"
	"github.com/afonsodelgado/factory/internal/ui/footer"
	"github.com/afonsodelgado/factory/internal/ui/frame"
	"github.com/afonsodelgado/factory/internal/ui/panel"
	"github.com/afonsodelgado/factory/internal/ui/selector"
	"github.com/afonsodelgado/factory/internal/ui/theme"
)

const (
	pollEvery  = 2 * time.Second
	focusEvery = 300 * time.Millisecond
)

type Model struct {
	Config *state.Config
	Engine *Engine
	Layout Layout
	Server *Server
	Node   Node
	Events chan Event
	Watch  *Watcher
	Open   func(url string)
	Roster func(context.Context) (node.Roster, error)
	Peers  func(name string) *node.Client

	ReviewLink func(ctx context.Context, path string) (string, error)

	DialogCommand func(kind string) string
	Bell          io.Writer
	Notify        func(text string)
	Now           func() time.Time
	Icons         theme.Icons
	Problem       string

	snap          Snapshot
	selected      string
	focus         footer.Mode
	showArchived  bool
	width         int
	height        int
	sizes         map[string][2]int
	sent          map[string]string
	header        [2]string
	share         float64
	collapsed     bool
	arranged      Shape
	window        [2]int
	pending       []tea.Cmd
	panel         panelState
	statuses      map[string]factory.Status
	loaded        map[string]factory.Status
	dialogs       map[string]json.RawMessage
	dialog        string
	dialogNext    *dialogTransition
	dialogRefused string
	notice        notice
	wakes         map[string]Wake
	pipelines     map[string]string
	toldReturnKey bool
	here          string
	local         Snapshot
	attempts      map[string]attempt
	later         []tea.Cmd
	claims        map[string]host.Answer
	threads       map[string]int
	prs           map[string][]host.PR
	prsChecked    time.Time
	lastSettled   string
	presented     bool
	parked        map[string]bool
	confirmedWork string
	roster        *node.Roster
	nodes         map[string]*nodeLink
	nodeFeed      chan nodeMsg
	nodeFactories map[string]host.LocalFactory
	nodeSends     map[string]nodeSend
	holdings      map[string]host.Holding
}

type pollMsg struct{}
type focusPollMsg struct{}
type activeMsg string
type eventMsg Event
type changedMsg struct{}
type noticeMsg notice

type notice struct {
	text string
	ok   bool
}

func news(text string) notice {
	return notice{text: text, ok: true}
}

func problem(text string) notice {
	return notice{text: text}
}

func (n notice) mark() string {
	if n.ok {
		return "✓"
	}
	return "!"
}

func NewModel(m Model) *Model {
	model := &m
	model.focus = footer.Factories
	model.sizes = map[string][2]int{}
	model.sent = map[string]string{}
	model.share = PanelShare
	model.panel = newPanelState()
	model.dialogs = map[string]json.RawMessage{}
	model.nodes = map[string]*nodeLink{}
	model.nodeFeed = make(chan nodeMsg, 64)
	model.nodeSends = map[string]nodeSend{}
	if model.Now == nil {
		model.Now = time.Now
	}
	if model.Problem != "" {
		model.notice = problem(model.Problem)
	}
	return model
}

func (m *Model) SetSnapshot(s Snapshot) {
	m.apply(s)
}

func (m *Model) Init() tea.Cmd {
	return tea.Batch(m.load(), m.poll(), m.focusPoll(), m.waitEvent(), m.waitChange(), m.refreshCatalog(), m.pollRoster(), m.waitNodes())
}

func (m *Model) load() tea.Cmd {
	if m.Node == nil {
		return nil
	}
	layout, repos := m.Layout, m.Config.Repos
	return func() tea.Msg {
		if m.Engine != nil {
			m.Engine.Adopt(layout, repos)
		}
		var p host.Presentation
		if err := m.ask("snapshot", nil, &p); err != nil {
			return nodeLostMsg{err}
		}
		return presentationMsg(p)
	}
}

func (m *Model) waitChange() tea.Cmd {
	if m.Watch == nil {
		return nil
	}
	changes := m.Watch.C
	return func() tea.Msg {
		<-changes
		return changedMsg{}
	}
}

func (m *Model) watchDirs() {
	if m.Watch == nil {
		return
	}
	dirs := []string{m.Config.SessionsDir()}
	for _, repo := range m.Config.Repos {
		dirs = append(dirs, factory.WorktreesDir(repo), filepath.Join(repo, ".claude", "factory"))
	}
	for _, it := range localOnly(m.snap).Items {
		dirs = append(dirs, it.Factory.Dir)
		if it.Stage != nil {
			dirs = append(dirs, filepath.Dir(it.Stage.Run.Result))
		}
		if !it.Factory.Supervisor().OnClaude() && it.Factory.Dir != "" {
			dirs = append(dirs, filepath.Join(it.Factory.Dir, "inbox"), filepath.Join(it.Factory.Dir, "stages"))
		}
	}
	m.Watch.Watch(dirs)
}

func (m *Model) poll() tea.Cmd {
	if m.Node == nil {
		return nil
	}
	return tea.Tick(pollEvery, func(time.Time) tea.Msg { return pollMsg{} })
}

func (m *Model) focusPoll() tea.Cmd {
	if m.Engine == nil {
		return nil
	}
	return tea.Tick(focusEvery, func(time.Time) tea.Msg { return focusPollMsg{} })
}

func (m *Model) activePane() tea.Cmd {
	return func() tea.Msg {
		id, err := m.Engine.TM.Display(m.Layout.Window, "#{pane_id}")
		if err != nil {
			return nil
		}
		return activeMsg(id)
	}
}

func (m *Model) waitEvent() tea.Cmd {
	if m.Events == nil {
		return nil
	}
	return func() tea.Msg { return eventMsg(<-m.Events) }
}

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.measureWindow()
	case presentationMsg:
		m.applyPresentation(host.Presentation(msg))
		cmd = tea.Batch(m.pending...)
		m.pending = nil
	case nodeLostMsg:
		m.notice = problem("The factory node is not answering, so this list is not updating: " + msg.err.Error() + ". Check factory node run.")
	case pollMsg:
		m.followMachine()
		cmd = tea.Batch(m.load(), m.poll(), m.pollRoster())
	case rosterMsg:
		m.rosterArrived(msg)
	case nodeMsg:
		m.nodeArrived(msg)
		cmd = m.waitNodes()
	case nodeAnsweredMsg:
		m.nodeAnswered(msg)
	case nodeRemovalMsg:
		cmd = m.nodeRemovalArrived(msg)
	case attachedMsg:
		cmd = m.attached(msg)
	case changedMsg:
		cmd = tea.Batch(m.load(), m.waitChange())
	case dialogClosedMsg:
		cmd = m.dialogClosed(msg)
	case focusPollMsg:
		cmd = tea.Batch(m.activePane(), m.focusPoll())
	case activeMsg:
		m.focusFromPane(string(msg))
	case eventMsg:
		cmd = tea.Batch(m.handleEvent(Event(msg)), m.waitEvent())
	case noticeMsg:
		m.notice = notice(msg)
	case tea.KeyPressMsg:
		m.focus = footer.Factories
		m.notice = notice{}
		cmd = m.selectorKey(msg.String())
	}
	m.settle()
	m.arrange()
	m.publish()
	return m, tea.Batch(cmd, m.attachSelected(), m.flushLater())
}

func (m *Model) flushLater() tea.Cmd {
	later := m.later
	m.later = nil
	return tea.Batch(later...)
}

func (m *Model) View() tea.View {
	v := tea.NewView(selector.Render(m.data(), m.width, m.height))
	v.AltScreen = true
	return v
}

func (m *Model) apply(s Snapshot) {
	if s.At.Before(m.local.At) {
		return
	}
	m.local = s
	m.refresh()
}

func (m *Model) refresh() {
	s := m.local
	s.Items = slices.Concat(s.Items, m.nodeItems())
	starting := m.selected == ""
	shownBefore := m.itemsOf(m.selected)
	m.snap = s
	m.loaded = map[string]factory.Status{}
	for _, it := range s.Items {
		m.loaded[it.Factory.Key()] = it.Status
	}
	m.clearRefusals()
	m.settleNodeSends()
	m.settle()
	m.alert(m.statuses)
	m.statuses = map[string]factory.Status{}
	for _, it := range s.Items {
		m.statuses[it.Factory.Key()] = it.Status
	}
	m.planWakes()
	m.notePipelines()
	rows := selector.Visible(m.data())
	if _, ok := m.snap.Find(m.selected); !ok || !containsKey(rows, m.selected) {
		m.choose(m.landing(rows, starting))
	} else if m.itemArrived(m.selected, shownBefore) {
		m.panel.tab = panel.Attention
	}
	m.ensureSlot()
	m.fitNewPanes()
	m.dropProxies()
	m.showSelected()
	m.arrange()
	m.watchDirs()
}

func (m *Model) landing(rows []selector.Row, starting bool) string {
	if key, ok := m.nextNeeding(); starting && ok {
		return key
	}
	if len(rows) > 0 {
		return rows[0].Key
	}
	return ""
}

func (m *Model) itemsOf(key string) map[string]bool {
	items := map[string]bool{}
	if it, ok := m.snap.Find(key); ok {
		shown, _ := m.shownItems(it)
		for _, item := range shown {
			items[item.Kind+"|"+item.ID] = true
		}
	}
	return items
}

func (m *Model) itemArrived(key string, shownBefore map[string]bool) bool {
	for item := range m.itemsOf(key) {
		if !shownBefore[item] {
			return true
		}
	}
	return false
}

func (m *Model) ensureSlot() {
	if m.Engine == nil || len(m.snap.Panes) == 0 {
		return
	}
	for _, p := range m.snap.Panes {
		if p.ID == m.Layout.Slot && p.WindowID == m.Layout.Window {
			return
		}
	}
	width, height, err := m.Engine.Measure(m.Layout.Selector)
	if err != nil {
		return
	}
	if layout, err := m.Engine.Build(m.Layout.Selector, m.shape(width, height)); err == nil {
		m.Layout = layout
		m.arranged = Shape{}
	}
}

func (m *Model) notePipelines() {
	m.pipelines = map[string]string{}
	defaults := map[string]string{}
	for _, it := range localOnly(m.snap).Items {
		st := it.Factory.State
		if st == nil || st.Pipeline == "" {
			continue
		}
		repo := it.Factory.Repo
		if _, ok := defaults[repo]; !ok {
			pipelines, _ := m.Config.PipelinesFor(repo)
			defaults[repo] = pipelines.Default
		}
		if st.Pipeline != defaults[repo] {
			m.pipelines[it.Factory.Key()] = st.Pipeline
		}
	}
}

func (m *Model) planWakes() {
	m.wakes = map[string]Wake{}
	var hooks []*state.HookFile
	if m.Config.StateDir != "" {
		hooks, _ = state.ReadHookFiles(m.Config.StateDir)
		hooks = withoutTokens(hooks, m.snap.StageTokens)
	}
	for _, it := range localOnly(m.snap).Items {
		if !it.Status.Asleep() {
			continue
		}
		m.wakes[it.Factory.Key()] = PlanWake(it, hooks, host.WritableDirsFor(m.Config, it.Factory.Repo, it.Factory.Worktree, it.Factory.Supervisor()))
	}
}

func (m *Model) repoConfig(repo string) (*state.Config, state.Profile) {
	profile, _ := state.ReadProfile(repo)
	return m.Config.WithProfile(profile), profile
}

func containsKey(rows []selector.Row, key string) bool {
	for _, r := range rows {
		if r.Key == key {
			return true
		}
	}
	return false
}

func paneTitle(f factory.Factory) string {
	if f.Title() == "" {
		return f.Label()
	}
	return f.Label() + "  " + f.Title()
}

func (m *Model) slotViewer() string {
	for _, p := range m.snap.Panes {
		if p.UI == "slot" && m.Engine.Owns(p) {
			return p.ID
		}
	}
	return ""
}

func (m *Model) showSelected() {
	if m.Engine == nil {
		return
	}
	it, ok := m.snap.Find(m.selected)
	target := m.slotViewer()
	if ok && it.Pane != nil && !it.Pane.Dead {
		target = it.Pane.ID
	}
	stage := m.shownStage(it, ok && target != m.slotViewer())
	if ok && it.Factory.Remote != nil {
		target, stage = m.nodeTargets(it)
	}
	if target == m.Layout.Slot && stage == m.Layout.Stage {
		return
	}
	target, stage = m.shareWithViews(target, stage)
	if target == m.Layout.Slot && stage == m.Layout.Stage {
		return
	}
	m.Engine.HideStage(&m.Layout)
	if target != "" && target != m.Layout.Slot {
		m.Engine.Select(&m.Layout, target)
	}
	if stage != "" {
		m.Engine.ShowStage(&m.Layout, stage)
	}
}

func (m *Model) shareWithViews(target, stage string) (string, string) {
	panes, err := m.Engine.TM.Panes()
	if err != nil {
		panes = m.snap.Panes
	}
	if m.onScreen(panes) {
		host.EndViews(m.Engine.TM, panes, target, stage)
		return target, stage
	}
	if host.Viewed(panes, target) {
		return m.slotViewer(), ""
	}
	if host.Viewed(panes, stage) {
		return target, ""
	}
	return target, stage
}

func (m *Model) onScreen(panes []tmux.Pane) bool {
	return slices.ContainsFunc(panes, func(p tmux.Pane) bool {
		return p.ID == m.Layout.Selector && m.Engine.Owns(p) && p.Attached
	})
}

func (m *Model) shownStage(it Item, supervisorShown bool) string {
	roomForTwo := m.window[0] == 0 || m.window[0] > NarrowAt
	if !supervisorShown || !roomForTwo || it.Stage == nil || it.Stage.Pane == nil || it.Stage.Pane.Dead {
		return ""
	}
	return it.Stage.Pane.ID
}

func (m *Model) measureWindow() {
	if m.Engine == nil {
		return
	}
	if width, height, err := m.Engine.Measure(m.Layout.Selector); err == nil {
		m.window = [2]int{width, height}
	}
	m.arrange()
}

func (m *Model) arrange() {
	if m.Engine == nil || m.window[0] == 0 {
		return
	}
	shape := m.shape(m.window[0], m.window[1])
	if shape == m.arranged {
		return
	}
	m.arranged = shape
	m.Engine.Arrange(m.Layout, shape)
}

func (m *Model) shape(width, height int) Shape {
	share := m.share
	if m.answering() {
		share = max(share, AnsweringShare)
	}
	return Shape{Width: width, Height: height, Share: share, Collapsed: m.collapsed || len(m.snap.Items) == 0}
}

func (m *Model) focusFromPane(id string) {
	switch {
	case m.focus == footer.Dialog:
	case id == m.Layout.Selector:
		m.focus = footer.Factories
	case id == m.Layout.Panel:
		m.focus = footer.Panel
	case m.Layout.Frame.Has(id), id == m.Layout.Split && id != "":
		m.focusSlot()
	case id == m.Layout.Slot && id != m.slotViewer(), id == m.Layout.Stage && id != "":
		m.focus = footer.Session
	}
}

func (m *Model) selectorKey(key string) tea.Cmd {
	switch key {
	case "j", "down":
		m.move(1)
	case "k", "up":
		m.move(-1)
	case "1", "2", "3", "4", "5", "6", "7", "8", "9":
		m.jump(int(key[0] - '0'))
	case "g":
		m.nextNeedingYou()
	case "enter":
		m.focusSession()
	case "i":
		m.focusPanel()
	case "H":
		m.showArchived = !m.showArchived
	case "tab":
		m.switchTab(1)
	case "shift+tab":
		m.switchTab(-1)
	case "z":
		m.collapsed = !m.collapsed
	case "+", "=":
		m.share = min(m.share+1.0/6, 5.0/6)
	case "-":
		m.share = max(m.share-1.0/6, 1.0/6)
	case "ctrl+o":
		return m.openRow(m.panelView())
	case "n":
		return m.openDialog("new", m.newContext())
	case "/":
		return m.openDialog("switch", m.switchContext())
	case "?":
		return m.openDialog("keys", struct{}{})
	case "P":
		return m.openDialog("phone", struct{}{})
	case "N":
		return m.openDialog("nodes", m.nodesContext())
	case "m":
		return m.askMove()
	case "p":
		return m.askPause()
	case "c":
		return m.askComments()
	case "a":
		return m.askArchive()
	case "d":
		return m.askDelete()
	case "w":
		m.wakeSelected()
	case "W":
		m.report(m.wakeAll())
	case "q", "ctrl+c":
		return m.quit()
	}
	return nil
}

func (m *Model) handleEvent(e Event) tea.Cmd {
	switch e.T {
	case "hello", "resize":
		if strings.HasPrefix(e.Pane, "dialog:") {
			m.dialogHello(e)
			return nil
		}
		m.sizes[e.Pane] = [2]int{e.W, e.H}
		if e.T == "hello" {
			delete(m.sent, e.Pane)
		}
	case "gone":
		delete(m.sizes, e.Pane)
		delete(m.sent, e.Pane)
	case "key":
		return m.viewerKey(e.Pane, e.K, e.Text)
	case "result":
		return m.dialogResult(e)
	case "show":
		return m.showRequest(e)
	case "paste":
		if e.Pane == "panel" {
			m.paste(e.Text)
		}
	case "mouse":
		if strings.HasPrefix(e.Pane, "frame-") {
			m.focusSlot()
		}
	default:
		if e.Pane == "cli" {
			e.Respond(Message{T: "error", Error: "the hub only shows factories; send " + e.T + " to the node over its socket"})
		}
	}
	return nil
}

func (m *Model) viewerKey(pane, key, text string) tea.Cmd {
	m.notice = notice{}
	switch {
	case pane == "panel" && key == "w" && m.asleepItemShown():
		m.wakeSelected()
	case pane == "panel":
		m.focus = footer.Panel
		return m.panelKey(key, text)
	case pane == "slot" && key == "n":
		return m.openDialog("new", m.newContext())
	case pane == "slot" && key == "w":
		m.wakeSelected()
	case pane == "slot" && (key == "q" || key == "ctrl+c"):
		return m.quit()
	case pane == "slot" && key == "?":
		return m.openDialog("keys", struct{}{})
	}
	return nil
}

func (m *Model) asleepItemShown() bool {
	_, item, ok := m.currentAttention()
	return ok && item.Kind == "asleep" && m.panel.tab == panel.Attention
}

func (m *Model) report(err error) {
	if err != nil {
		m.notice = problem(err.Error())
	}
}

func (m *Model) askPause() tea.Cmd {
	it, ok := m.selectedItem()
	switch {
	case !ok:
		return nil
	case it.Pane == nil && m.nodeFactories[it.Factory.Key()].Pane == "":
		m.notice = problem(it.Factory.Label() + " has no pane to pause.")
		return nil
	case m.unverified(it) != "":
		m.notice = problem(heldUnchecked(it, m.unverified(it)))
		return nil
	}
	return m.openDialog("pause", m.pauseContext(it))
}

func (m *Model) askDelete() tea.Cmd {
	it, ok := m.selectedItem()
	switch {
	case !ok:
		return nil
	case it.Factory.Remote != nil:
		return m.askNodeDelete(it)
	}
	return m.openDialog("delete", m.deleteContext(it))
}

func (m *Model) askArchive() tea.Cmd {
	it, ok := m.selectedItem()
	switch {
	case !ok:
		return nil
	case it.Factory.Archived():
		m.report(m.unarchive(it))
		return m.load()
	case !archivable(it):
		m.notice = problem("Only done, gone or exited factories can be archived. Pause " + it.Factory.Label() + " first.")
		return nil
	}
	return m.openDialog("archive", m.archiveContext(it))
}

func (m *Model) move(delta int) {
	rows := selector.Visible(m.data())
	if len(rows) == 0 {
		return
	}
	i := indexOf(rows, m.selected) + delta
	m.choose(rows[max(0, min(i, len(rows)-1))].Key)
}

func indexOf(rows []selector.Row, key string) int {
	for i, r := range rows {
		if r.Key == key {
			return i
		}
	}
	return 0
}

func (m *Model) jump(n int) {
	for _, r := range selector.Visible(m.data()) {
		if r.Number == n {
			m.choose(r.Key)
			return
		}
	}
}

func (m *Model) choose(key string) {
	if key == m.selected {
		return
	}
	m.saveDraft()
	m.selected = key
	m.panel.item, m.panel.row = 0, 0
	if it, ok := m.snap.Find(key); ok {
		m.panel.tab = m.openingTab(it)
	}
	m.showSelected()
}

func (m *Model) openingTab(it Item) panel.Tab {
	shown, _ := m.shownItems(it)
	somethingOpen := len(shown) > 0
	userPickedAnotherTab := m.panel.tab == panel.Artifacts || m.panel.tab == panel.Activity
	switch {
	case somethingOpen:
		return panel.Attention
	case userPickedAnotherTab:
		return m.panel.tab
	case len(m.prsFor(it.Factory)) > 0:
		return panel.PRs
	}
	return panel.Attention
}

func (m *Model) nextNeedingYou() {
	key, ok := m.nextNeeding()
	if !ok {
		m.notice = news("Nothing needs you right now.")
		return
	}
	m.choose(key)
	if _, item, open := m.currentAttention(); open && item.Answerable() && m.panel.tab == panel.Attention {
		m.focusPanel()
	}
}

func (m *Model) nextNeeding() (string, bool) {
	type candidate struct {
		key   string
		since time.Time
	}
	var candidates []candidate
	for _, it := range m.snap.Items {
		if it.Status == factory.NeedsYou && !it.Factory.Archived() {
			candidates = append(candidates, candidate{it.Factory.Key(), needsSince(it)})
		}
	}
	if len(candidates) == 0 {
		return "", false
	}
	sort.SliceStable(candidates, func(i, j int) bool { return candidates[i].since.Before(candidates[j].since) })
	next := 0
	for i, c := range candidates {
		if c.key == m.selected {
			next = (i + 1) % len(candidates)
		}
	}
	return candidates[next].key, true
}

func (m *Model) nextElsewhere() (Item, bool) {
	key, ok := m.nextNeeding()
	if !ok || key == m.selected {
		return Item{}, false
	}
	return m.snap.Find(key)
}

func needsSince(it Item) time.Time { return host.NeedsSince(it) }

func (m *Model) focusSession() {
	it, ok := m.snap.Find(m.selected)
	if !ok || m.Engine == nil {
		return
	}
	proxied := it.Factory.Remote != nil && m.Layout.Slot != m.slotViewer()
	if it.Pane == nil && !proxied {
		m.notice = problem(noSessionToEnter(it))
		return
	}
	m.Engine.TM.SelectPane(m.Layout.Slot)
	m.focus = footer.Session
	if !m.toldReturnKey {
		m.toldReturnKey = true
		m.notice = news("In the " + it.Factory.Label() + " session. " + m.Config.Hub.ReturnKey + " brings you back to the factory list from any pane.")
	}
}

func noSessionToEnter(it Item) string {
	if it.Status == factory.Done {
		return it.Factory.Label() + " is done and has no session pane."
	}
	return it.Factory.Label() + " has no session pane. w wakes it."
}

func (m *Model) focusSlot() {
	if m.Engine == nil {
		return
	}
	m.Engine.TM.SelectPane(m.Layout.Slot)
	if m.Layout.Slot != m.slotViewer() {
		m.focus = footer.Session
	}
}

func (m *Model) focusPanel() {
	if m.Engine != nil {
		m.Engine.TM.SelectPane(m.Layout.Panel)
	}
	m.focus = footer.Panel
}

func (m *Model) focusSelector() {
	if m.Engine != nil {
		m.Engine.TM.SelectPane(m.Layout.Selector)
	}
	m.focus = footer.Factories
}

func (m *Model) quit() tea.Cmd {
	m.saveDraft()
	if m.Server != nil {
		m.Server.Broadcast(Message{T: "quit"})
	}
	return tea.Quit
}

func (m *Model) data() selector.Data {
	var groups []selector.Group
	byRepo := map[string]int{}
	for _, repo := range m.Config.Repos {
		group := selector.Group{Name: filepath.Base(repo), Path: tilde(repo)}
		byRepo[groupKey(group)] = len(groups)
		groups = append(groups, group)
	}
	var archived []selector.Row
	for _, it := range m.snap.Items {
		row := m.row(it)
		if it.Factory.Archived() {
			row.Archived = true
			archived = append(archived, row)
			continue
		}
		group := m.groupOf(it.Factory)
		i, ok := byRepo[groupKey(group)]
		if !ok {
			byRepo[groupKey(group)] = len(groups)
			groups = append(groups, group)
			i = len(groups) - 1
		}
		groups[i].Rows = append(groups[i].Rows, row)
	}
	return selector.Data{
		Groups:       selector.Arrange(besideLocal(groups)),
		Archived:     archived,
		ShowArchived: m.showArchived,
		Selected:     m.selected,
		Focused:      m.focus == footer.Factories,
		Icons:        m.Icons,
		Dim:          m.focus == footer.Dialog,
	}
}

func groupKey(g selector.Group) string {
	return g.Name + "|" + g.Remote + "|" + g.Path + "|" + g.Node
}

func (m *Model) row(it Item) selector.Row {
	f := it.Factory
	row := selector.Row{
		Key:    f.Key(),
		Label:  f.Label(),
		Title:  f.Title(),
		Repo:   f.RepoName(),
		Status: it.Status,
		Track:  factory.Track(f.State),
		Stage:  stageOf(f),
	}
	if name := m.pipelines[f.Key()]; name != "" {
		row.Stage += " · " + name
	}
	if f.State != nil {
		row.Loop, row.Round = f.State.Loops.Active, f.State.Stages[f.State.Stage].Rounds
		if row.Loop != "" {
			row.Round = f.State.Loops.Used[row.Loop]
		}
		_, settled := m.shownItems(it)
		row.Items = len(f.State.OpenAttention()) - len(settled)
	}
	if d, ok := factory.TimeInStage(f, m.snap.At); ok {
		row.Time = theme.Age(d)
	}
	if lf, onNode := m.nodeFactories[f.Key()]; onNode {
		_, settled := m.shownItems(it)
		row.Stage, row.Items = lf.Stage, len(lf.Items)-len(settled)
		row.Loop, row.Round = lf.Loop, lf.Round
		if !lf.NeedsSince.IsZero() {
			row.Time = theme.Age(m.Now().Sub(lf.NeedsSince))
		}
	}
	row.Stuck = m.stuck(it)
	if m.unverified(it) != "" {
		row.Stage = "held, unchecked"
	}
	return row
}

func (m *Model) stuck(it Item) string {
	for _, item := range m.attentionItems(it, nil) {
		if item.Kind == "prompt" || item.Kind == "silent" {
			return item.Kind
		}
	}
	return ""
}

func tilde(path string) string { return host.Tilde(path) }

func (m *Model) publish() {
	if m.Server != nil {
		m.push("footer", m.FooterView(m.size("footer")))
		m.push("panel", m.PanelView(m.size("panel")))
		m.push("slot", m.SlotView(m.size("slot")))
		m.pushFrame()
	}
	m.updateHeader()
}

func (m *Model) pushFrame() {
	labels := m.frameLabels()
	focused := m.focus == footer.Session
	for _, side := range slices.Concat(FrameSides, []string{"split"}) {
		pane := "frame-" + side
		width, height := m.size(pane)
		m.push(pane, frame.Render(side, width, height, labels, focused))
	}
}

func (m *Model) frameLabels() frame.Labels {
	it, ok := m.snap.Find(m.selected)
	if !ok {
		return frame.Labels{}
	}
	labels := frame.Labels{Title: paneTitle(it.Factory), Path: tilde(it.Factory.Worktree)}
	if it.Pane != nil && !it.Pane.Dead && it.Pane.Path != "" {
		labels.Path = tilde(it.Pane.Path)
	}
	stageShown := m.Layout.Stage != ""
	switch {
	case it.Factory.Remote != nil:
		labels.Path = ""
		if lf := m.nodeFactories[it.Factory.Key()]; stageShown {
			labels.Stage = cmp.Or(lf.StageLabel, lf.StageRole)
		}
	case stageShown && it.Stage != nil:
		labels.Stage = host.StageLabel(it.Stage.Run)
	}
	return labels
}

func (m *Model) size(pane string) (int, int) {
	s := m.sizes[pane]
	return s[0], s[1]
}

func (m *Model) push(pane, frame string) {
	if _, connected := m.sizes[pane]; !connected || m.sent[pane] == frame {
		return
	}
	m.sent[pane] = frame
	m.Server.Send(pane, Message{T: "frame", Pane: pane, S: frame})
}

func (m *Model) updateHeader() {
	if m.Engine == nil {
		return
	}
	left, right := m.Header()
	if m.header == [2]string{left, right} {
		return
	}
	m.header = [2]string{left, right}
	m.Engine.SetHeader(left, right)
}

func appBadge(ic theme.Icons) string {
	return fmt.Sprintf("#[fg=#15161e,bg=#7aa2f7,bold] %sfactory #[default] ", ic.Icon("app"))
}

const hostLabel = "#[fg=#3b4261]#H "

func StoppedHeader(ic theme.Icons) (string, string) {
	return appBadge(ic) + " #[fg=#737aa2]hub closed · factory brings it back", hostLabel
}

func (m *Model) Header() (string, string) {
	ic := m.Icons
	left := appBadge(ic)
	if len(m.snap.Items) == 0 {
		return left + " #[fg=#737aa2]no factories yet", m.nodeLabels() + m.machineLabel()
	}
	count := func(statuses ...factory.Status) int {
		n := 0
		for _, it := range m.snap.Items {
			for _, s := range statuses {
				if it.Status == s && !it.Factory.Archived() {
					n++
				}
			}
		}
		return n
	}
	needs := count(factory.NeedsYou)
	if needs > 0 {
		left += fmt.Sprintf(" #[fg=#ff9e64,bold]%s%d need you#[default] ", ic.Icon("needs"), needs)
	} else {
		left += fmt.Sprintf(" #[fg=#737aa2]%s0 need you#[default] ", ic.Icon("needs"))
	}
	parts := []struct {
		icon, color, word string
		statuses          []factory.Status
	}{
		{"running", "#9ece6a", "running", []factory.Status{factory.Running}},
		{"ci", "#bb9af7", "waiting", []factory.Status{factory.WaitingCI, factory.WaitingAgent}},
		{"stalled", "#e0af68", "asleep", []factory.Status{factory.Stalled, factory.Exited, factory.Gone}},
		{"starting", "#7dcfff", "starting", []factory.Status{factory.Starting}},
		{"unreachable", "#565f89", "unreachable", []factory.Status{factory.Unreachable}},
	}
	for _, p := range parts {
		if n := count(p.statuses...); n > 0 {
			left += fmt.Sprintf(" #[fg=%s]%s%d %s#[default] ", p.color, ic.Icon(p.icon), n, p.word)
		}
	}
	return left, m.nodeLabels() + m.ghLabel() + m.machineLabel()
}

func (m *Model) machineLabel() string {
	return "#[fg=#3b4261]" + m.Config.NodeName() + " "
}

func (m *Model) ghLabel() string {
	checked := m.prsChecked
	if checked.IsZero() {
		return ""
	}
	return fmt.Sprintf("#[fg=#565f89]%sgh %s ago  ", m.Icons.Icon("gh"), theme.Age(m.Now().Sub(checked)))
}

func (m *Model) FooterView(width, _ int) string {
	context := ""
	if it, ok := m.snap.Find(m.selected); ok {
		context = it.Factory.Placed()
	}
	if m.focus == footer.Dialog {
		context = strings.ReplaceAll(m.dialog, "new", "new factory")
	}
	if m.focus == footer.Panel && m.panel.tab == panel.Attention {
		if _, item, ok := m.currentAttention(); ok && item.ID != "" {
			context += " · " + item.ID
		}
	}
	return footer.Render(m.focus, context, m.hints(), width)
}

func hints(pairs ...string) []footer.Hint {
	var h []footer.Hint
	for i := 0; i+1 < len(pairs); i += 2 {
		h = append(h, footer.Hint{Key: pairs[i], Does: pairs[i+1]})
	}
	return h
}

func (m *Model) hints() []footer.Hint {
	shown := m.notice
	if shown.text == "" && m.focus == footer.Session {
		shown = m.streamNotice()
	}
	var notice []footer.Hint
	if shown.text != "" {
		notice = []footer.Hint{{Key: shown.mark(), Does: shown.text, Good: shown.ok}}
	}
	switch m.focus {
	case footer.Session:
		return append(notice, hints(m.Config.Hub.ReturnKey, "back to factories")...)
	case footer.Panel:
		return append(notice, m.panelHints()...)
	case footer.Dialog:
		return dialogHints(m.dialog)
	}
	return append(notice, m.selectorHints()...)
}

func dialogHints(kind string) []footer.Hint {
	switch kind {
	case "new":
		return hints("⏎", "next", "↑↓", "pick", "esc", "back or cancel")
	case "settings":
		return hints("ctrl+s", "save", "↑↓", "field", "esc", "cancel")
	case "switch":
		return hints("⏎", "select", "ctrl+j/k", "move", "ctrl+g", "next needing you", "esc", "close")
	case "pause":
		return hints("y", "pause", "n", "cancel", "esc", "cancel")
	case "comments":
		return hints("y", "confirm", "n", "cancel", "esc", "cancel")
	case "archive":
		return hints("a", "archive", "r", "archive and remove the worktree", "esc", "cancel")
	case "delete":
		return hints("y", "delete", "n", "cancel", "esc", "cancel")
	case "move":
		return hints("⏎", "move", "j/k", "pick", "x", "cancel the move", "esc", "close")
	}
	return hints("esc", "close")
}

func (m *Model) selectorHints() []footer.Hint {
	it, ok := m.selectedItem()
	switch {
	case !ok:
		return hints("n", "new factory", "?", "all keys", "q", "quit the UI")
	case it.Factory.Archived():
		return hints("a", "unarchive "+it.Factory.Label(), "H", "hide archived", "⏎", "session", "j/k", "move", "/", "find", "n", "new", "d", "delete")
	case factory.PermissionPrompt(it.Hook) || m.stuck(it) == "prompt":
		return stuckHints("answer in the session")
	case m.stuck(it) == "silent":
		return stuckHints("open the session")
	case it.Status.Asleep():
		return hints("w", "wake "+it.Factory.Label(), "W", "wake all asleep", "j/k", "move", "g", "next needing you", "/", "find", "p", "pause", "a", "archive", "d", "delete")
	case it.Status == factory.Done:
		return hints("a", "archive "+it.Factory.Label(), "H", "archived", "⏎", "session", "z", "fold panel", "j/k", "move", "/", "find", "n", "new", "d", "delete")
	}
	return hints("j/k", "move", "⏎", "session", "i", "answer", "N", "nodes", "g", "next needing you", "/", "find", "1-9", "jump", "n", "new", "w", "wake", "p", "pause", "c", "comments", "m", "move to a node", "a", "archive", "d", "delete")
}

func stuckHints(enter string) []footer.Hint {
	return hints("⏎", enter, "j/k", "move", "g", "next needing you", "/", "find", "i", "panel", "n", "new", "w", "wake", "p", "pause", "N", "nodes")
}

func (m *Model) panelHints() []footer.Hint {
	switch m.panel.tab {
	case panel.PRs:
		return hints("j/k", "move", "ctrl+o", "open in browser", "r", "refresh now", "tab", "next tab", "esc", "back")
	case panel.Artifacts:
		return hints("j/k", "move", "ctrl+o", "open in browser", "tab", "next tab", "esc", "back")
	case panel.Activity:
		return hints("j/k", "scroll", "t", "all factories", "⏎", "jump to factory", "tab", "next tab", "esc", "back")
	}
	it, item, ok := m.currentAttention()
	if next, found := m.nextElsewhere(); !ok && found {
		return hints("⏎", "next: "+next.Factory.Label(), "tab", "next tab", "esc", "back to factories")
	}
	if !ok {
		return hints("tab", "next tab", "esc", "back to factories")
	}
	editing := item.Answerable() && m.draft(it.Factory, item).Editing
	back := "back, keep draft"
	if editing {
		back = "stop typing"
	}
	if panel.TextAnswered(item) && len(item.Artifacts) > 0 {
		return hints("type", panel.TextPrompt(item.Kind), "ctrl+s", "send", "↑↓", "pick", "ctrl+o", "open", "ctrl+k", "already handled", "ctrl+x", "dismiss", "ctrl+d", "discard", "esc", back)
	}
	if panel.TextAnswered(item) {
		return hints("type", panel.TextPrompt(item.Kind), "ctrl+s", "send", "ctrl+k", "already handled", "ctrl+x", "dismiss", "ctrl+d", "discard", "esc", back)
	}
	d := &panel.Draft{}
	if item.Answerable() {
		d = m.draft(it.Factory, item)
	}
	approve := "approve"
	if item.Kind == "gate" && len(item.Artifacts) > 1 && d.Artifact < len(item.Artifacts) {
		approve = "approve " + item.Artifacts[d.Artifact].Title
	}
	approving := item.Answerable() && !editing && onApproveRow(item, d)
	switch {
	case item.Kind == "question" && editing:
		return hints("⏎", "confirm", "ctrl+s", "send", "esc", back)
	case onSendRow(item, d):
		return hints("⏎", "send", "↑", "back to the questions", "ctrl+k", "already handled", "ctrl+x", "dismiss", "ctrl+d", "discard", "ctrl+n/p", "item", "esc", back)
	case item.Kind == "question":
		return hints("↑↓", "move", "⏎", "pick", "←→", "question", "type", "your answer", "ctrl+r", "recommended", "ctrl+s", "send", "ctrl+k", "already handled", "ctrl+x", "dismiss", "ctrl+d", "discard", "ctrl+n/p", "item", "esc", back)
	case approving:
		return hints("⏎", "approve", "ctrl+a", "approve", "type", "notes", "ctrl+s", "send as changes", "↑↓", "pick", "ctrl+k", "already handled", "ctrl+x", "dismiss", "ctrl+n/p", "item", "esc", back)
	case item.Kind == "gate":
		return hints("ctrl+a", approve, "type", "notes", "ctrl+s", "send as changes", "↑↓", "pick", "ctrl+o", "open", "ctrl+k", "already handled", "ctrl+x", "dismiss", "ctrl+n/p", "item", "esc", back)
	case item.Kind == "comment":
		return hints("type", "a reply", "ctrl+s", "send", "ctrl+o", "open thread", "ctrl+k", "already handled", "ctrl+x", "dismiss", "ctrl+n/p", "item", "esc", back)
	case item.Kind == "cap":
		return hints("↑↓", "pick", "⏎", "allow", "ctrl+r", "one more round", "type", "an instruction", "ctrl+s", "send it", "ctrl+k", "already handled", "ctrl+x", "dismiss", "ctrl+n/p", "item", "esc", back)
	case item.Kind == "failure":
		return hints("ctrl+r", "retry", "type", "an instruction", "ctrl+s", "send it", "ctrl+k", "already handled", "ctrl+x", "dismiss", "ctrl+n/p", "item", "esc", back)
	case item.Kind == "handoff":
		return hints("ctrl+k", "mark done", "type", "what to do next", "ctrl+s", "continue working", "ctrl+x", "dismiss", "ctrl+n/p", "item", "esc", back)
	case item.Kind == "permission" || item.Kind == "prompt":
		return hints("⏎", "answer in the session", "[ ]", "item", "esc", "back")
	case item.Kind == "silent":
		return hints("⏎", "open the session", "[ ]", "item", "esc", "back")
	}
	return hints("[ ]", "item", "tab", "next tab", "esc", "back")
}

func (m *Model) SlotView(width, height int) string {
	c := theme.NewCanvas(width, height)
	it, ok := m.snap.Find(m.selected)
	switch {
	case !ok:
		m.drawIntro(c)
	case it.Factory.Remote != nil:
		m.drawNode(c, it)
	default:
		m.drawNoPane(c, it)
	}
	return c.Render()
}

func (m *Model) drawIntro(c *theme.Canvas) {
	ic := m.Icons
	x := max((c.W-72)/2, 2)
	y := max(c.H/2-5, 1)
	c.Put(x, y, ic.Icon("app")+" factory", theme.Focused.B())
	y += 2
	for _, line := range theme.Wrap("The selected factory's Claude Code session appears here, with its Needs attention panel underneath.", min(68, c.W-x-2)) {
		c.Put(x, y, line, theme.Text)
		y++
	}
	y++
	c.Put(x, y, "New machine?", theme.Bright)
	y++
	c.Put(x+2, y, "factory doctor     ", theme.Key)
	c.Put(x+25, y, "checks claude, tmux, gh, gh-stack, revmux and the bundle", theme.Muted)
	y++
	c.Put(x+2, y, "factory bootstrap  ", theme.Key)
	c.Put(x+25, y, "installs the bundled skill, agents and hooks", theme.Muted)
}

func (m *Model) drawNoPane(c *theme.Canvas, it Item) {
	ic := m.Icons
	x := max((c.W-80)/2, 2)
	y := max(c.H/2-6, 1)
	ink := theme.StatusInk(it.Status)
	c.Put(x, y, ic.Icon(theme.StatusIcon(it.Status))+" ", ink)
	c.Put(x+3, y, "No pane for "+it.Factory.Label(), theme.Bright.B())
	y += 2
	for _, line := range theme.Wrap(noPaneReason(it), min(76, c.W-x-2)) {
		c.Put(x, y, line, theme.Text)
		y++
	}
	wake, asleep := m.wakes[it.Factory.Key()]
	if !asleep || wake.Kind != WakeNewPane {
		if it.Factory.Worktree != "" {
			y++
			c.Put(x, y, "worktree  ", theme.Quiet)
			end := c.Put(x+10, y, theme.KeepTail(tilde(it.Factory.Worktree), c.W-x-22), theme.Text)
			if !dirExists(it.Factory.Worktree) {
				c.Put(end+2, y, "removed", theme.FgInk(theme.Red))
			}
		}
		return
	}
	y++
	c.Put(x, y, "Wake recreates the pane in the worktree and resumes the same conversation:", theme.Text)
	y += 2
	c.Put(x+2, y, "$ cd "+theme.KeepTail(tilde(it.Factory.Worktree), c.W-x-9), theme.Bright)
	y++
	for i, l := range theme.Wrap(wake.Command, c.W-x-6) {
		lead := "$ "
		if i > 0 {
			lead = "    "
		}
		c.Put(x+2, y, lead+l, theme.Bright)
		y++
	}
	y++
	source := "No session id was recorded, so wake falls back to --continue."
	if wake.Source != NoSession {
		source = "The session id comes from " + string(wake.Source) + "."
	}
	c.Put(x, y, source, theme.Muted)
	y += 2
	x = c.Put(x, y, "w", theme.Key)
	x = c.Put(x+1, y, "wake "+it.Factory.Label()+"   ", theme.Text)
	x = c.Put(x, y, "W", theme.Key)
	c.Put(x+1, y, "wake all asleep", theme.Text)
}

func noPaneReason(it Item) string {
	switch {
	case it.Status == factory.Done && retired(it.Factory):
		return "This factory is done and its worktree was removed. n with the same id starts a new factory; this row stays as history."
	case it.Status == factory.Done:
		return "This factory is done and its tmux pane is closed. Archive it to hide the row."
	}
	if it.Hook != nil {
		return fmt.Sprintf("The tmux pane is gone. Its last hook event was at %s.", it.Hook.Last.At.Local().Format("Jan 2 15:04"))
	}
	return "The tmux pane is gone."
}

func loopStage(f factory.Factory) string {
	if f.State != nil {
		round := f.State.Stages[f.State.Stage].Rounds
		if f.State.Loops.Active != "" {
			round = f.State.Loops.Used[f.State.Loops.Active]
		}
		return workflowStage(f.State.Stage, f.State.Loops.Active, round)
	}
	return stageOf(f)
}

func workflowStage(stage, loop string, round int) string {
	if round == 0 {
		return stage
	}
	if loop != "" {
		stage += " · " + loop
	}
	return fmt.Sprintf("%s · round %d", stage, round)
}
