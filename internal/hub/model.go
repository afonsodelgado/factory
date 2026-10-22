package hub

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/afonsodelgado/factory/internal/factory"
	"github.com/afonsodelgado/factory/internal/state"
	"github.com/afonsodelgado/factory/internal/ui/footer"
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
	Now    func() time.Time
	Icons  theme.Icons

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
	if model.Now == nil {
		model.Now = time.Now
	}
	return model
}

func (m *Model) SetSnapshot(s Snapshot) {
	m.apply(s)
}

func (m *Model) Init() tea.Cmd {
	return tea.Batch(m.load(), m.poll(), m.focusPoll(), m.waitEvent(), m.waitChange())
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
		m.arrange()
	case snapshotMsg:
		m.apply(Snapshot(msg))
	case pollMsg:
		cmd = tea.Batch(m.load(), m.poll())
	case changedMsg:
		cmd = tea.Batch(m.load(), m.waitChange())
	case focusPollMsg:
		cmd = tea.Batch(m.activePane(), m.focusPoll())
	case activeMsg:
		m.focusFromPane(string(msg))
	case eventMsg:
		cmd = tea.Batch(m.handleEvent(Event(msg)), m.waitEvent())
	case tea.KeyPressMsg:
		m.focus = footer.Factories
		cmd = m.selectorKey(msg.String())
	}
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
	m.snap = s
	rows := selector.Visible(m.data())
	if _, ok := m.snap.Find(m.selected); !ok || !containsKey(rows, m.selected) {
		m.selected = ""
		if len(rows) > 0 {
			m.selected = rows[0].Key
		}
	}
	m.syncTitles()
	m.ensureSlot()
	m.showSelected()
	m.arrange()
	m.watchDirs()
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

func (m *Model) arrange() {
	if m.Engine == nil {
		return
	}
	width, height, err := m.Engine.TM.WindowSize(m.Layout.Selector)
	if err != nil {
		return
	}
	shape := m.shape(width, height)
	if shape == m.arranged {
		return
	}
	m.arranged = shape
	m.Engine.Arrange(m.Layout, shape)
}

func (m *Model) shape(width, height int) Shape {
	return Shape{Width: width, Height: height, Share: m.share, Collapsed: m.collapsed || len(m.snap.Items) == 0}
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
	case "q", "ctrl+c":
		return m.quit()
	}
	return nil
}

func (m *Model) handleEvent(e Event) tea.Cmd {
	switch e.T {
	case "hello", "resize":
		m.sizes[e.Pane] = [2]int{e.W, e.H}
		if e.T == "hello" {
			delete(m.sent, e.Pane)
		}
	case "gone":
		delete(m.sizes, e.Pane)
		delete(m.sent, e.Pane)
	case "key":
		return m.viewerKey(e.Pane, e.K, e.Text)
	}
	return nil
}

func (m *Model) viewerKey(pane, key, text string) tea.Cmd {
	if pane == "panel" {
		m.focus = footer.Panel
		if key == "esc" {
			m.focusSelector()
		}
	}
	return nil
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
	m.selected = key
	m.showSelected()
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
	}
	if f.State != nil {
		row.Stage = f.State.Stage
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
		m.send("footer", m.FooterView(m.size("footer")))
		m.send("panel", m.PanelView(m.size("panel")))
		m.send("slot", m.SlotView(m.size("slot")))
	}
	m.updateHeader()
}

func (m *Model) size(pane string) (int, int) {
	s := m.sizes[pane]
	return s[0], s[1]
}

func (m *Model) send(pane, frame string) {
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
	m.Engine.TM.SetSessionOption(m.Engine.Session, "status-left", left)
	m.Engine.TM.SetSessionOption(m.Engine.Session, "status-right", right)
}

func (m *Model) Header() (string, string) {
	ic := m.Icons
	left := fmt.Sprintf("#[fg=#15161e,bg=#7aa2f7,bold] %sfactory #[default] ", ic.Icon("app"))
	if len(m.snap.Items) == 0 {
		return left + " #[fg=#737aa2]no factories yet", "#[fg=#3b4261]#H "
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
		{"starting", "#7dcfff", "starting", []factory.Status{factory.Starting, factory.FailedToStart}},
	}
	for _, p := range parts {
		if n := count(p.statuses...); n > 0 {
			left += fmt.Sprintf(" #[fg=%s]%s%d %s#[default] ", p.color, ic.Icon(p.icon), n, p.word)
		}
	}
	return left, "#[fg=#3b4261]#H "
}

func (m *Model) FooterView(width, _ int) string {
	mode := m.focus
	context := ""
	if it, ok := m.snap.Find(m.selected); ok {
		context = it.Factory.Label()
	}
	return footer.Render(mode, context, m.hints(), width)
}

func (m *Model) hints() []footer.Hint {
	switch m.focus {
	case footer.Session:
		return []footer.Hint{{Key: m.Config.Hub.ReturnKey, Does: "back to factories"}}
	case footer.Panel:
		return []footer.Hint{{Key: "esc", Does: "back to factories"}}
	}
	if len(m.snap.Items) == 0 {
		return []footer.Hint{{Key: "q", Does: "quit the UI"}}
	}
	return []footer.Hint{
		{Key: "j/k", Does: "move"},
		{Key: "⏎", Does: "session"},
		{Key: "i", Does: "panel"},
		{Key: "g", Does: "next needing you"},
		{Key: "1-9", Does: "jump"},
		{Key: "H", Does: "archived"},
		{Key: "q", Does: "quit the UI"},
	}
}

func (m *Model) PanelView(width, height int) string {
	c := theme.NewCanvas(width, height)
	it, ok := m.snap.Find(m.selected)
	if !ok || height < 3 {
		return c.Render()
	}
	border := theme.Line
	if m.focus == footer.Panel {
		border = theme.Focused
	}
	c.Box(0, 0, width, height, theme.Box{Ink: border, TitleRight: []theme.Span{{Text: " " + it.Factory.Label() + " ", Ink: theme.Muted}}})
	return c.Render()
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
	c.Put(x, y, ic.Icon("app")+" factory-tui", theme.Focused.B())
	y += 2
	for _, line := range theme.Wrap("The selected factory's Claude Code session appears here, with its Needs attention panel underneath.", min(68, c.W-x-2)) {
		c.Put(x, y, line, theme.Text)
		y++
	}
	y++
	c.Put(x, y, "New machine?", theme.Bright)
	y++
	c.Put(x+2, y, "factory-tui doctor     ", theme.Key)
	c.Put(x+25, y, "checks claude, tmux, gh, gh-stack, revmux and the bundle", theme.Muted)
	y++
	c.Put(x+2, y, "factory-tui bootstrap  ", theme.Key)
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
	if it.Factory.Worktree != "" {
		y++
		c.Put(x, y, "worktree  ", theme.Quiet)
		c.PutMax(x+10, y, tilde(it.Factory.Worktree), theme.Text, c.W-x-12)
	}
}

func noPaneReason(it Item) string {
	if it.Status == factory.Done {
		return "This factory is done and its tmux pane is closed. Archive it to hide the row."
	}
	if it.Hook != nil {
		return fmt.Sprintf("The tmux pane is gone. Its last hook event was at %s.", it.Hook.Last.At.Local().Format("Jan 2 15:04"))
	}
	return "The tmux pane is gone."
}
