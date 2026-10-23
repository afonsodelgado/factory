package hub

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/afonsodelgado/factory/internal/factory"
	"github.com/afonsodelgado/factory/internal/state"
	"github.com/afonsodelgado/factory/internal/ui/footer"
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
	Loader *Loader
	Events chan Event
	Watch  *Watcher
	GitHub *GitHub
	Typist Typist
	Open   func(url string)

	DialogCommand func(kind string) string
	Bell          io.Writer
	Notify        func(text string)
	Now           func() time.Time
	Icons         theme.Icons

	snap         Snapshot
	selected     string
	focus        footer.Mode
	showArchived bool
	width        int
	height       int
	sizes        map[string][2]int
	sent         map[string]string
	header       [2]string
	share        float64
	collapsed    bool
	arranged     Shape
	window       [2]int
	pending      []tea.Cmd
	panel        panelState
	statuses     map[string]factory.Status
	dialogs      map[string]json.RawMessage
	dialog       string
	notice       string
	wakes        map[string]Wake
	pipelines    map[string]string
}

type snapshotMsg Snapshot
type pollMsg struct{}
type focusPollMsg struct{}
type activeMsg string
type eventMsg Event
type changedMsg struct{}

func NewModel(m Model) *Model {
	model := &m
	model.focus = footer.Factories
	model.sizes = map[string][2]int{}
	model.sent = map[string]string{}
	model.share = PanelShare
	model.panel = newPanelState()
	model.dialogs = map[string]json.RawMessage{}
	if model.Now == nil {
		model.Now = time.Now
	}
	return model
}

func (m *Model) SetSnapshot(s Snapshot) {
	m.apply(s)
}

func (m *Model) Init() tea.Cmd {
	return tea.Batch(m.load(), m.poll(), m.focusPoll(), m.waitEvent(), m.waitChange(), m.ghPoll())
}

func (m *Model) load() tea.Cmd {
	if m.Loader == nil {
		return nil
	}
	layout := m.Layout
	return func() tea.Msg {
		if m.Engine != nil {
			m.Engine.Adopt(layout, m.Config.Repos)
		}
		return snapshotMsg(m.Loader.Load())
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
	for _, it := range m.snap.Items {
		dirs = append(dirs, it.Factory.Dir)
	}
	m.Watch.Watch(dirs)
}

func (m *Model) poll() tea.Cmd {
	if m.Loader == nil {
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
	case snapshotMsg:
		m.apply(Snapshot(msg))
		cmd = tea.Batch(m.pending...)
		m.pending = nil
	case pollMsg:
		cmd = tea.Batch(m.load(), m.poll())
	case changedMsg:
		cmd = tea.Batch(m.load(), m.waitChange())
	case prsMsg:
		for key, prs := range msg.statuses {
			m.panel.prs[key] = prs
		}
	case ghPollMsg:
		cmd = tea.Batch(m.refreshPRs(false), m.ghPoll())
	case dialogClosedMsg:
		m.dialogClosed(msg)
	case focusPollMsg:
		cmd = tea.Batch(m.activePane(), m.focusPoll())
	case activeMsg:
		m.focusFromPane(string(msg))
	case eventMsg:
		cmd = tea.Batch(m.handleEvent(Event(msg)), m.waitEvent())
	case tea.KeyPressMsg:
		m.focus = footer.Factories
		m.notice = ""
		cmd = m.selectorKey(msg.String())
	}
	m.arrange()
	m.publish()
	return m, cmd
}

func (m *Model) View() tea.View {
	v := tea.NewView(selector.Render(m.data(), m.width, m.height))
	v.AltScreen = true
	return v
}

func (m *Model) apply(s Snapshot) {
	if s.At.Before(m.snap.At) {
		return
	}
	first := len(m.snap.Items) == 0 && m.GitHub != nil
	shownBefore := m.itemsOf(m.selected)
	m.snap = s
	m.forgetClosedItems()
	m.alert(m.statuses)
	m.statuses = map[string]factory.Status{}
	for _, it := range s.Items {
		m.statuses[it.Factory.Key()] = it.Status
	}
	m.planWakes()
	m.notePipelines()
	rows := selector.Visible(m.data())
	if _, ok := m.snap.Find(m.selected); !ok || !containsKey(rows, m.selected) {
		topRow := ""
		if len(rows) > 0 {
			topRow = rows[0].Key
		}
		m.choose(topRow)
	} else if m.itemArrived(m.selected, shownBefore) {
		m.panel.tab = panel.Attention
	}
	m.syncTitles()
	m.ensureSlot()
	m.showSelected()
	m.arrange()
	m.watchDirs()
	if first {
		m.pending = append(m.pending, m.refreshPRs(false))
	}
}

func (m *Model) itemsOf(key string) map[string]bool {
	items := map[string]bool{}
	if it, ok := m.snap.Find(key); ok {
		for _, item := range attentionItems(it, m.snap.At) {
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
	width, height, err := m.Engine.TM.WindowSize(m.Layout.Selector)
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
	for _, it := range m.snap.Items {
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
	}
	for _, it := range m.snap.Items {
		if !it.Status.Asleep() {
			continue
		}
		profile, _ := state.ReadProfile(it.Factory.Repo)
		m.wakes[it.Factory.Key()] = PlanWake(it, profile, m.Config.RemoteControl(), hooks)
	}
}

func containsKey(rows []selector.Row, key string) bool {
	for _, r := range rows {
		if r.Key == key {
			return true
		}
	}
	return false
}

func (m *Model) syncTitles() {
	if m.Engine == nil {
		return
	}
	for _, it := range m.snap.Items {
		if it.Pane == nil {
			continue
		}
		if want := paneTitle(it.Factory); it.Pane.Title != want {
			m.Engine.TM.SetPaneOption(it.Pane.ID, "@factory_title", want)
		}
	}
}

func paneTitle(f factory.Factory) string {
	if f.Title() == "" {
		return f.Label()
	}
	return f.Label() + "  " + f.Title()
}

func (m *Model) slotViewer() string {
	for _, p := range m.snap.Panes {
		if p.UI == "slot" && p.Session == m.Engine.Session {
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
	viewer := m.slotViewer()
	target, title, path := viewer, "", ""
	switch {
	case ok && it.Pane != nil && !it.Pane.Dead:
		target = it.Pane.ID
	case ok:
		title, path = paneTitle(it.Factory), tilde(it.Factory.Worktree)
	}
	if viewer != "" {
		m.Engine.TM.SetPaneOption(viewer, "@factory_title", title)
		m.Engine.TM.SetPaneOption(viewer, "@factory_path", path)
	}
	if target != "" && target != m.Layout.Slot {
		m.Engine.Select(&m.Layout, target)
	}
}

func (m *Model) measureWindow() {
	if m.Engine == nil {
		return
	}
	if width, height, err := m.Engine.TM.WindowSize(m.Layout.Selector); err == nil {
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
	case id == m.Layout.Slot && id != m.slotViewer():
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
	case "o":
		return m.openRow(m.panelView())
	case "n":
		return m.openDialog("new", m.newContext())
	case "/":
		return m.openDialog("switch", m.switchContext())
	case "?":
		return m.openDialog("keys", nil)
	case "x":
		return m.askKill()
	case "a":
		return m.askArchive()
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
	case "open":
		return m.openRequest(e)
	case "paste":
		if e.Pane == "panel" {
			m.paste(e.Text)
		}
	}
	return nil
}

func (m *Model) viewerKey(pane, key, text string) tea.Cmd {
	m.notice = ""
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
	}
	return nil
}

func (m *Model) asleepItemShown() bool {
	_, item, ok := m.currentAttention()
	return ok && item.Kind == "asleep" && m.panel.tab == panel.Attention
}

func (m *Model) report(err error) {
	if err != nil {
		m.notice = err.Error()
	}
}

func (m *Model) askKill() tea.Cmd {
	it, ok := m.selectedItem()
	switch {
	case !ok:
		return nil
	case it.Pane == nil:
		m.notice = it.Factory.Label() + " has no pane to kill."
		return nil
	}
	return m.openDialog("kill", m.killContext(it))
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
		m.notice = "Only done, gone or exited factories can be archived. Kill " + it.Factory.Label() + " first."
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
	somethingOpen := len(attentionItems(it, m.snap.At)) > 0
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
		return
	}
	sort.SliceStable(candidates, func(i, j int) bool { return candidates[i].since.Before(candidates[j].since) })
	next := 0
	for i, c := range candidates {
		if c.key == m.selected {
			next = (i + 1) % len(candidates)
		}
	}
	m.choose(candidates[next].key)
}

func needsSince(it Item) time.Time {
	var oldest time.Time
	if it.Factory.State != nil {
		for _, a := range it.Factory.State.OpenAttention() {
			if oldest.IsZero() || a.CreatedAt.Before(oldest) {
				oldest = a.CreatedAt.Time
			}
		}
	}
	if oldest.IsZero() && it.Hook != nil && it.Hook.Main != nil {
		oldest = it.Hook.Main.At
	}
	if oldest.IsZero() {
		oldest = it.Factory.StateMod
	}
	return oldest
}

func (m *Model) focusSession() {
	it, ok := m.snap.Find(m.selected)
	if !ok || it.Pane == nil || m.Engine == nil {
		return
	}
	m.Engine.TM.SelectPane(m.Layout.Slot)
	m.focus = footer.Session
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
		byRepo[repo] = len(groups)
		groups = append(groups, selector.Group{Name: filepath.Base(repo), Path: tilde(repo)})
	}
	var archived []selector.Row
	for _, it := range m.snap.Items {
		row := m.row(it)
		if it.Factory.Archived() {
			row.Archived = true
			archived = append(archived, row)
			continue
		}
		i, ok := byRepo[it.Factory.Repo]
		if !ok {
			byRepo[it.Factory.Repo] = len(groups)
			groups = append(groups, selector.Group{Name: it.Factory.RepoName(), Path: tilde(it.Factory.Repo)})
			i = len(groups) - 1
		}
		groups[i].Rows = append(groups[i].Rows, row)
	}
	return selector.Data{
		Groups:       selector.Arrange(groups),
		Archived:     archived,
		ShowArchived: m.showArchived,
		Selected:     m.selected,
		Focused:      m.focus == footer.Factories,
		Icons:        m.Icons,
		ConfigFile:   tilde(filepath.Join(m.Config.ConfigDir, "config.yml")),
		Dim:          m.focus == footer.Dialog,
	}
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
		row.Items = len(f.State.OpenAttention())
	}
	if d, ok := factory.TimeInStage(f, m.snap.At); ok {
		row.Time = theme.Age(d)
	}
	return row
}

func tilde(path string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return path
	}
	if path == home {
		return "~"
	}
	if strings.HasPrefix(path, home+string(filepath.Separator)) {
		return "~" + strings.TrimPrefix(path, home)
	}
	return path
}

func (m *Model) publish() {
	if m.Server != nil {
		m.push("footer", m.FooterView(m.size("footer")))
		m.push("panel", m.PanelView(m.size("panel")))
		m.push("slot", m.SlotView(m.size("slot")))
	}
	m.updateHeader()
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
	return appBadge(ic) + " #[fg=#737aa2]hub closed · factory open brings it back", hostLabel
}

func (m *Model) Header() (string, string) {
	ic := m.Icons
	left := appBadge(ic)
	if len(m.snap.Items) == 0 {
		return left + " #[fg=#737aa2]no factories yet", hostLabel
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
		{"failstart", "#f7768e", "failed to start", []factory.Status{factory.FailedToStart}},
	}
	for _, p := range parts {
		if n := count(p.statuses...); n > 0 {
			left += fmt.Sprintf(" #[fg=%s]%s%d %s#[default] ", p.color, ic.Icon(p.icon), n, p.word)
		}
	}
	return left, m.ghLabel() + hostLabel
}

func (m *Model) ghLabel() string {
	if m.GitHub == nil {
		return ""
	}
	checked := m.GitHub.Checked()
	if checked.IsZero() {
		return ""
	}
	return fmt.Sprintf("#[fg=#565f89]%sgh %s ago  ", m.Icons.Icon("gh"), theme.Age(m.Now().Sub(checked)))
}

func (m *Model) FooterView(width, _ int) string {
	context := ""
	if it, ok := m.snap.Find(m.selected); ok {
		context = it.Factory.Label()
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
	var notice []footer.Hint
	if m.notice != "" {
		notice = hints("!", m.notice)
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
		return hints("⏎", "next", "↑↓", "repo", "esc", "cancel")
	case "switch":
		return hints("⏎", "select", "ctrl+j/k", "move", "ctrl+g", "next needing you", "esc", "close")
	case "kill":
		return hints("y", "kill", "n", "cancel", "esc", "cancel")
	case "archive":
		return hints("a", "archive", "r", "archive and remove the worktree", "esc", "cancel")
	}
	return hints("esc", "close")
}

func (m *Model) selectorHints() []footer.Hint {
	it, ok := m.selectedItem()
	switch {
	case !ok:
		return hints("n", "new factory", "?", "all keys", "q", "quit the UI")
	case it.Factory.Archived():
		return hints("a", "unarchive "+it.Factory.Label(), "H", "hide archived", "⏎", "session", "j/k", "move", "/", "find", "n", "new")
	case factory.PermissionPrompt(it.Hook):
		return hints("⏎", "answer in the session", "j/k", "move", "g", "next needing you", "/", "find", "i", "panel", "n", "new", "w", "wake", "x", "kill")
	case it.Status.Asleep():
		return hints("w", "wake "+it.Factory.Label(), "W", "wake all asleep", "j/k", "move", "g", "next needing you", "/", "find", "x", "kill", "a", "archive")
	case it.Status == factory.Done:
		return hints("a", "archive "+it.Factory.Label(), "H", "archived", "⏎", "session", "z", "fold panel", "j/k", "move", "/", "find", "n", "new")
	}
	return hints("j/k", "move", "⏎", "session", "i", "answer", "g", "next needing you", "/", "find", "1-9", "jump", "n", "new", "w", "wake", "x", "kill", "a", "archive")
}

func (m *Model) panelHints() []footer.Hint {
	switch m.panel.tab {
	case panel.PRs:
		return hints("j/k", "move", "o", "open in browser", "r", "refresh now", "tab", "next tab", "esc", "back")
	case panel.Artifacts:
		return hints("j/k", "move", "o", "open in browser", "tab", "next tab", "esc", "back")
	case panel.Activity:
		return hints("j/k", "scroll", "t", "all factories", "⏎", "jump to factory", "tab", "next tab", "esc", "back")
	}
	it, item, ok := m.currentAttention()
	if !ok {
		return hints("tab", "next tab", "esc", "back to factories")
	}
	if item.Answerable() && m.isSent(it.Factory, item) {
		return hints("ctrl+d", "clear and answer again", "[ ]", "item", "esc", "back")
	}
	back := "back, keep draft"
	if item.Answerable() && m.draft(it.Factory, item).Editing {
		back = "stop typing"
	}
	if item.Kind == "question" && len(item.Questions) == 0 {
		return hints("type", "your answer", "ctrl+s", "send", "ctrl+d", "discard", "esc", back)
	}
	switch item.Kind {
	case "question":
		return hints("↑↓", "question", "←→", "option", "type", "your answer", "ctrl+r", "recommended", "ctrl+s", "send", "ctrl+d", "discard", "esc", back)
	case "gate":
		return hints("ctrl+a", "approve", "type", "notes", "ctrl+s", "send as changes", "↑↓", "pick", "ctrl+o", "open", "[ ]", "item", "esc", back)
	case "comment":
		return hints("type", "a reply", "ctrl+s", "send", "ctrl+o", "open thread", "[ ]", "item", "esc", back)
	case "cap":
		return hints("ctrl+r", "one more round", "ctrl+x", "skip", "type", "an instruction", "ctrl+s", "send it", "[ ]", "item", "esc", back)
	case "failure":
		return hints("ctrl+r", "retry", "ctrl+x", "skip", "type", "an instruction", "ctrl+s", "send it", "[ ]", "item", "esc", back)
	case "permission":
		return hints("⏎", "answer in the session", "[ ]", "item", "esc", "back")
	}
	return hints("[ ]", "item", "tab", "next tab", "esc", "back")
}

func (m *Model) SlotView(width, height int) string {
	c := theme.NewCanvas(width, height)
	it, ok := m.snap.Find(m.selected)
	if !ok {
		m.drawIntro(c)
		return c.Render()
	}
	m.drawNoPane(c, it)
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
			end := c.PutMax(x+10, y, tilde(it.Factory.Worktree), theme.Text, c.W-x-22)
			if !dirExists(it.Factory.Worktree) {
				c.Put(end+2, y, "removed", theme.FgInk(theme.Red))
			}
		}
		return
	}
	y++
	c.Put(x, y, "Wake recreates the pane in the worktree and resumes the same conversation:", theme.Text)
	y += 2
	c.PutMax(x+2, y, "$ cd "+tilde(it.Factory.Worktree), theme.Bright, c.W-x-4)
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
		return "This factory is done and its worktree was removed. n or factory open with the same id starts a new factory; this row stays as history."
	case it.Status == factory.Done:
		return "This factory is done and its tmux pane is closed. Archive it to hide the row."
	}
	if it.Hook != nil {
		return fmt.Sprintf("The tmux pane is gone. Its last hook event was at %s.", it.Hook.Last.At.Local().Format("Jan 2 15:04"))
	}
	return "The tmux pane is gone."
}
