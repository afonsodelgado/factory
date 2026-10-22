package hub

import (
	"encoding/json"
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
	"github.com/afonsodelgado/factory/internal/ui/panel"
	"github.com/rivo/uniseg"
)

type panelState struct {
	tab     panel.Tab
	item    int
	row     int
	all     bool
	shown   string
	drafts  map[string]*panel.Draft
	refused map[string]string
	sent    map[string]time.Time
	prs     map[string][]panel.PR
}

func newPanelState() panelState {
	return panelState{
		drafts:  map[string]*panel.Draft{},
		refused: map[string]string{},
		sent:    map[string]time.Time{},
		prs:     map[string][]panel.PR{},
	}
}

func attentionItems(it Item, now time.Time) []panel.Item {
	var items []panel.Item
	f := it.Factory
	if f.State != nil {
		for _, a := range f.State.OpenAttention() {
			items = append(items, fromAttention(f.State, a))
		}
	}
	if factory.PermissionPrompt(it.Hook) {
		at := it.Hook.Last.At
		if it.Hook.Main != nil && it.Hook.Main.At.After(at) {
			at = it.Hook.Main.At
		}
		items = append(items, panel.Item{
			Kind:   "permission",
			Title:  "waiting on a permission prompt",
			Body:   "Claude Code is showing a permission prompt in the session. Answer it there; the panel cannot.",
			Since:  at,
			Action: "⏎ answer in the session",
		})
	}
	if it.Status.Asleep() {
		items = append(items, asleepItem(it, now))
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].Since.Before(items[j].Since) })
	return items
}

func fromAttention(st *state.State, a state.Attention) panel.Item {
	item := panel.Item{ID: a.ID, Kind: a.Kind, Title: a.Title, Body: a.Body, ArtifactURL: a.ArtifactURL, ThreadID: a.ThreadID, Since: a.CreatedAt.Time, Stage: a.Stage()}
	for i, q := range a.Questions {
		question := panel.Question{Text: q}
		if i < len(a.Options) {
			question.Options = a.Options[i]
		}
		if i < len(a.Recommended) {
			question.Recommended = a.Recommended[i]
		}
		item.Questions = append(item.Questions, question)
	}
	if a.Kind == "gate" {
		for _, art := range st.Stages[item.Stage].Artifacts {
			item.Artifacts = append(item.Artifacts, artifactRow(art))
		}
	}
	return item
}

func artifactRow(a state.Artifact) panel.ArtifactRow {
	title := a.Title
	if title == "" {
		title = a.URL
	}
	return panel.ArtifactRow{Name: path(a.URL), Title: title, URL: a.URL, OpenThreads: a.OpenThreads, CheckedAt: a.ThreadsCheckedAt.Time}
}

func path(url string) string {
	return url[strings.LastIndex(url, "/")+1:]
}

func asleepItem(it Item, now time.Time) panel.Item {
	f := it.Factory
	item := panel.Item{Kind: "asleep", Status: it.Status, Since: lastSign(it), Action: "w wake"}
	switch it.Status {
	case factory.Gone:
		item.Title = "gone · no tmux pane"
		item.Body = "No tmux pane, and the factory isn't done. The worktree is intact."
	case factory.Exited:
		item.Title = "exited · the pane is a shell"
		item.Body = "The pane still exists, so wake reuses it rather than making a new one."
	default:
		item.Title = "stalled · no activity"
		item.Body = fmt.Sprintf("Idle and waiting on %s, with no hook event, state write or heartbeat for %s.", f.WaitingOn(), ageOf(now, item.Since))
	}
	if id := f.SessionID(); id != "" {
		item.Detail = append(item.Detail, "session id   "+id)
	}
	if f.Worktree != "" {
		item.Detail = append(item.Detail, "worktree     "+tilde(f.Worktree))
	}
	if f.State != nil && f.State.Stage != "" {
		item.Detail = append(item.Detail, "stage        "+f.State.Stage)
	}
	return item
}

func lastSign(it Item) time.Time {
	newest := it.Factory.StateMod
	if it.Factory.Heartbeat.After(newest) {
		newest = it.Factory.Heartbeat
	}
	if it.Hook != nil && it.Hook.Last.At.After(newest) {
		newest = it.Hook.Last.At
	}
	return newest
}

func ageOf(now, then time.Time) string {
	if then.IsZero() {
		return "a while"
	}
	return now.Sub(then).Round(time.Minute).String()
}

func (m *Model) selectedItem() (Item, bool) {
	return m.snap.Find(m.selected)
}

func (m *Model) currentAttention() (Item, panel.Item, bool) {
	it, ok := m.selectedItem()
	if !ok {
		return Item{}, panel.Item{}, false
	}
	items := attentionItems(it, m.snap.At)
	if len(items) == 0 {
		return it, panel.Item{}, false
	}
	m.panel.item = max(0, min(m.panel.item, len(items)-1))
	return it, items[m.panel.item], true
}

func draftKey(f factory.Factory, itemID string) string {
	return f.Key() + "|" + itemID
}

func (m *Model) draftPath(f factory.Factory, itemID string) string {
	return filepath.Join(m.Config.DraftsDir(), filepath.Base(f.Repo)+"-"+f.Slug, itemID+".json")
}

func (m *Model) draft(f factory.Factory, item panel.Item) *panel.Draft {
	key := draftKey(f, item.ID)
	d, ok := m.panel.drafts[key]
	if !ok {
		d = &panel.Draft{}
		if data, err := os.ReadFile(m.draftPath(f, item.ID)); err == nil && m.Config.StateDir != "" {
			json.Unmarshal(data, d)
		}
		m.panel.drafts[key] = d
	}
	d.Fit(len(item.Questions))
	if len(item.Artifacts) > 0 {
		d.Artifact = max(0, min(d.Artifact, len(item.Artifacts)-1))
	}
	return d
}

func (m *Model) saveDraft() {
	it, item, ok := m.currentAttention()
	if !ok || item.ID == "" || m.Config.StateDir == "" {
		return
	}
	d := m.draft(it.Factory, item)
	d.Editing = false
	data, err := json.Marshal(d)
	if err == nil {
		state.WriteFileAtomic(m.draftPath(it.Factory, item.ID), data)
	}
}

func (m *Model) discardDraft() {
	it, item, ok := m.currentAttention()
	if !ok || item.ID == "" {
		return
	}
	key := draftKey(it.Factory, item.ID)
	delete(m.panel.drafts, key)
	delete(m.panel.refused, key)
	os.Remove(m.draftPath(it.Factory, item.ID))
}

func (m *Model) target(it Item) Target {
	return Target{Label: it.Factory.Label(), Pane: it.Pane, Hook: it.Hook}
}

func (m *Model) freshTarget(it Item) Target {
	t := m.target(it)
	if m.Engine != nil && it.Pane != nil {
		if panes, err := m.Engine.TM.Panes(); err == nil {
			t.Pane = nil
			if p, ok := byID(panes, it.Pane.ID); ok {
				t.Pane = &p
			}
		}
	}
	if it.Hook != nil && m.Config.StateDir != "" {
		if hook, err := state.ReadHookFile(filepath.Join(m.Config.SessionsDir(), it.Hook.SessionID+".json")); err == nil {
			t.Hook = hook
		}
	}
	return t
}

func (m *Model) message(item panel.Item, d *panel.Draft, action string) string {
	switch {
	case item.Kind == "question":
		answers := make([]string, len(item.Questions))
		for i, q := range item.Questions {
			answers[i] = d.Answer(q, i)
		}
		return QuestionAnswers(item.ID, answers)
	case item.Kind == "gate" && action == "approve":
		return GateApprove(item.Stage, item.ID, m.gateNotes(item, d))
	case item.Kind == "gate":
		return GateChanges(item.ID, d.Text)
	case item.Kind == "comment":
		return CommentReply(item.ID, d.Text)
	case action == "continue":
		return CapContinue(item.ID, LimitFor(item.Stage))
	case action == "skip":
		return Skip(item.Stage)
	case action == "retry":
		return Retry()
	}
	return Instruction(item.ID, d.Text)
}

func (m *Model) gateNotes(item panel.Item, d *panel.Draft) string {
	if len(item.Artifacts) < 2 {
		return d.Text
	}
	return strings.TrimSpace("Approve " + item.Artifacts[d.Artifact].Title + ". " + d.Text)
}

func (m *Model) deliver(action string) {
	it, item, ok := m.currentAttention()
	if !ok || !item.Answerable() {
		return
	}
	d := m.draft(it.Factory, item)
	text := m.message(item, d, action)
	key := draftKey(it.Factory, item.ID)
	if m.Typist == nil {
		return
	}
	reason, err := Deliver(m.Typist, m.freshTarget(it), text)
	if err != nil {
		reason = err.Error()
	}
	if reason != "" {
		m.panel.refused[key] = reason
		m.saveDraft()
		return
	}
	delete(m.panel.refused, key)
	m.panel.sent[key] = m.Now()
	delete(m.panel.drafts, key)
	os.Remove(m.draftPath(it.Factory, item.ID))
	if m.Loader != nil {
		m.Loader.Note(it.Factory, "answered "+item.ID+" from the panel")
	}
}

func (m *Model) answering() bool {
	if m.focus != footer.Panel || m.panel.tab != panel.Attention {
		return false
	}
	_, item, ok := m.currentAttention()
	return ok && item.Answerable()
}

func (m *Model) panelView() panel.View {
	v := panel.View{Tab: m.panel.tab, Focused: m.focus == footer.Panel, Collapsed: m.collapsed, Now: m.Now(), Row: m.panel.row, All: m.panel.all}
	it, ok := m.selectedItem()
	if !ok {
		return v
	}
	f := it.Factory
	v.Label = f.Label()
	v.Items = attentionItems(it, m.snap.At)
	v.Item = max(0, min(m.panel.item, len(v.Items)-1))
	if f.State != nil {
		for _, a := range f.State.Artifacts() {
			v.Artifacts = append(v.Artifacts, artifactRow(a))
		}
	}
	v.PRs = m.prsFor(f)
	if m.GitHub != nil {
		v.Checked = m.GitHub.Checked()
	}
	v.Counts = [3]int{len(v.Items), len(v.PRs), len(v.Artifacts)}
	v.Activity = m.activity(it)
	if v.All {
		v.Label = "all factories"
	}
	if len(v.Items) > 0 {
		item := v.Items[v.Item]
		if item.ID != "" {
			d := m.draft(f, item)
			v.Draft = *d
			v.Send = m.sendState(it, item, d)
		}
	}
	return v
}

func (m *Model) sendState(it Item, item panel.Item, d *panel.Draft) panel.Send {
	key := draftKey(it.Factory, item.ID)
	reason := Refusal(m.target(it))
	s := panel.Send{Ready: reason == "", Reason: reason, Refused: m.panel.refused[key], SentAt: m.panel.sent[key], Preview: m.message(item, d, "approve")}
	if s.Ready {
		s.Reason = fmt.Sprintf("Sends 1 message to %s, which is idle at its prompt.", it.Factory.Label())
	}
	return s
}

func (m *Model) prsFor(f factory.Factory) []panel.PR {
	if f.State == nil {
		return nil
	}
	cached := m.panel.prs[f.Key()]
	prs := make([]panel.PR, len(f.State.PRs))
	for i, pr := range f.State.PRs {
		prs[i] = panel.PR{Number: pr.Number, Title: pr.Title}
		for _, c := range cached {
			if c.Number == pr.Number && c.Loaded {
				prs[i] = c
			}
		}
	}
	return prs
}

func (m *Model) activity(it Item) []panel.ActivityRow {
	items := []Item{it}
	if m.panel.all {
		items = m.snap.Items
	}
	var rows []panel.ActivityRow
	for _, i := range items {
		var logged []state.ActivityEntry
		if m.Config.StateDir != "" {
			logged, _ = state.ReadActivity(state.ActivityPath(m.Config.ActivityDir(), i.Factory.Repo, i.Factory.Slug))
		}
		for _, a := range factory.Activity(i.Factory, logged) {
			rows = append(rows, panel.ActivityRow{At: a.At, Factory: i.Factory.Label(), Key: i.Factory.Key(), Stage: a.Stage, Source: a.Source, Text: a.Text})
		}
	}
	sort.SliceStable(rows, func(a, b int) bool { return rows[a].At.After(rows[b].At) })
	return rows
}

func (m *Model) PanelView(width, height int) string {
	return panel.Render(m.panelView(), m.Icons, width, height)
}

func (m *Model) panelKey(key, text string) tea.Cmd {
	it, item, hasItem := m.currentAttention()
	var d *panel.Draft
	if hasItem && item.ID != "" {
		d = m.draft(it.Factory, item)
	}
	if d != nil && d.Editing {
		m.edit(item, d, key, text)
		return nil
	}
	switch key {
	case "esc":
		m.saveDraft()
		m.focusSelector()
		return nil
	case "tab":
		m.switchTab(1)
		return nil
	case "shift+tab":
		m.switchTab(-1)
		return nil
	case "z":
		m.collapsed = !m.collapsed
		return nil
	case "+", "=":
		m.share = min(m.share+1.0/6, 5.0/6)
		return nil
	case "-":
		m.share = max(m.share-1.0/6, 1.0/6)
		return nil
	case "[":
		m.saveDraft()
		m.panel.item = max(m.panel.item-1, 0)
		return nil
	case "]":
		m.saveDraft()
		m.panel.item++
		return nil
	}
	if m.panel.tab != panel.Attention {
		return m.listKey(key)
	}
	if !hasItem {
		return nil
	}
	if item.Kind == "permission" && key == "enter" {
		m.focusSession()
		return nil
	}
	if d == nil {
		return nil
	}
	return m.formKey(item, d, key)
}

func (m *Model) switchTab(delta int) {
	m.panel.tab = m.panel.tab.Next(delta)
	m.panel.row = 0
}

func (m *Model) formKey(item panel.Item, d *panel.Draft, key string) tea.Cmd {
	switch item.Kind {
	case "question":
		q := item.Questions[d.Question]
		switch key {
		case "up", "k":
			d.Question = max(d.Question-1, 0)
		case "down", "j":
			d.Question = min(d.Question+1, len(item.Questions)-1)
		case "left", "h":
			d.Choice[d.Question] = max(choice(d, q)-1, 0)
		case "right", "l":
			d.Choice[d.Question] = min(choice(d, q)+1, len(q.Options))
		case "e", "enter":
			d.Choice[d.Question] = len(q.Options)
			d.Editing = true
		case "r":
			d.Choice[d.Question] = -1
		case "R":
			for i := range d.Choice {
				d.Choice[i] = -1
			}
		case "ctrl+s":
			m.deliver("")
		case "d":
			m.discardDraft()
		}
	case "gate":
		switch key {
		case "up", "k":
			d.Artifact = max(d.Artifact-1, 0)
		case "down", "j":
			d.Artifact = min(d.Artifact+1, max(len(item.Artifacts)-1, 0))
		case "e", "enter":
			d.Editing = true
		case "a":
			m.deliver("approve")
		case "c", "ctrl+s":
			if strings.TrimSpace(d.Text) != "" {
				m.deliver("changes")
			}
		case "o":
			return m.openCmd(gateURL(item, d))
		case "d":
			m.discardDraft()
		}
	case "comment":
		switch key {
		case "e", "enter":
			d.Editing = true
		case "ctrl+s":
			if strings.TrimSpace(d.Text) != "" {
				m.deliver("reply")
			}
		case "o":
			return m.openCmd(item.ArtifactURL)
		case "d":
			m.discardDraft()
		}
	case "cap", "failure":
		switch key {
		case "c":
			if item.Kind == "cap" {
				m.deliver("continue")
			}
		case "r":
			if item.Kind == "failure" {
				m.deliver("retry")
			}
		case "s":
			m.deliver("skip")
		case "e", "enter":
			d.Editing = true
		case "d":
			m.discardDraft()
		}
	}
	return nil
}

func choice(d *panel.Draft, q panel.Question) int {
	if c := d.Choice[d.Question]; c >= 0 {
		return c
	}
	return max(q.RecommendedIndex(), 0)
}

func gateURL(item panel.Item, d *panel.Draft) string {
	if d.Artifact < len(item.Artifacts) {
		return item.Artifacts[d.Artifact].URL
	}
	return item.ArtifactURL
}

func (m *Model) edit(item panel.Item, d *panel.Draft, key, text string) {
	field := &d.Text
	if item.Kind == "question" {
		field = &d.Typed[d.Question]
	}
	switch key {
	case "esc":
		d.Editing = false
		m.saveDraft()
	case "ctrl+s":
		d.Editing = false
		m.deliver(editAction(item.Kind))
	case "backspace":
		*field = dropLast(*field)
	case "enter":
		*field += "\n"
	case "ctrl+u":
		*field = ""
	default:
		*field += text
	}
}

func editAction(kind string) string {
	switch kind {
	case "gate":
		return "changes"
	case "comment":
		return "reply"
	}
	return ""
}

func dropLast(s string) string {
	g := uniseg.NewGraphemes(s)
	last := 0
	for g.Next() {
		from, _ := g.Positions()
		last = from
	}
	return s[:last]
}

func (m *Model) paste(text string) {
	it, item, ok := m.currentAttention()
	if !ok || item.ID == "" {
		return
	}
	d := m.draft(it.Factory, item)
	if !d.Editing {
		return
	}
	m.edit(item, d, "", text)
}

func (m *Model) listKey(key string) tea.Cmd {
	v := m.panelView()
	rows := 0
	switch m.panel.tab {
	case panel.PRs:
		rows = len(v.PRs)
	case panel.Artifacts:
		rows = len(v.Artifacts)
	case panel.Activity:
		rows = len(v.Activity)
	}
	switch key {
	case "j", "down":
		m.panel.row = min(m.panel.row+1, max(rows-1, 0))
	case "k", "up":
		m.panel.row = max(m.panel.row-1, 0)
	case "t":
		if m.panel.tab == panel.Activity {
			m.panel.all = !m.panel.all
			m.panel.row = 0
		}
	case "r":
		if m.panel.tab == panel.PRs {
			return m.refreshPRs(true)
		}
	case "o":
		return m.openRow(v)
	case "enter":
		if m.panel.tab == panel.Activity && m.panel.row < len(v.Activity) {
			m.choose(v.Activity[m.panel.row].Key)
		}
	}
	return nil
}

func (m *Model) openRow(v panel.View) tea.Cmd {
	switch {
	case m.panel.tab == panel.PRs && m.panel.row < len(v.PRs):
		return m.openCmd(v.PRs[m.panel.row].URL)
	case m.panel.tab == panel.Artifacts && m.panel.row < len(v.Artifacts):
		return m.openCmd(v.Artifacts[m.panel.row].URL)
	}
	return nil
}

func (m *Model) openCmd(url string) tea.Cmd {
	if url == "" || m.Open == nil {
		return nil
	}
	open := m.Open
	return func() tea.Msg {
		open(url)
		return nil
	}
}

type prsMsg struct {
	statuses map[string][]panel.PR
}

func (m *Model) refreshPRs(force bool) tea.Cmd {
	if m.GitHub == nil {
		return nil
	}
	gh := m.GitHub
	type job struct {
		key     string
		dir     string
		numbers []int
	}
	var jobs []job
	for _, it := range m.snap.Items {
		f := it.Factory
		if f.State == nil || len(f.State.PRs) == 0 || f.Archived() {
			continue
		}
		var numbers []int
		for _, pr := range f.State.PRs {
			numbers = append(numbers, pr.Number)
		}
		dir := f.Worktree
		if _, err := os.Stat(dir); err != nil {
			dir = f.Repo
		}
		jobs = append(jobs, job{f.Key(), dir, numbers})
	}
	now := m.Now()
	return func() tea.Msg {
		statuses := map[string][]panel.PR{}
		for _, j := range jobs {
			statuses[j.key] = gh.Statuses(j.dir, j.numbers, now, force)
		}
		return prsMsg{statuses}
	}
}

type ghPollMsg struct{}

func (m *Model) ghPoll() tea.Cmd {
	if m.GitHub == nil {
		return nil
	}
	return tea.Tick(ghEvery, func(time.Time) tea.Msg { return ghPollMsg{} })
}

func (m *Model) alert(previous map[string]factory.Status) {
	if previous == nil {
		return
	}
	for _, it := range m.snap.Items {
		key := it.Factory.Key()
		was, known := previous[key]
		if !known || was == factory.NeedsYou || it.Status != factory.NeedsYou || key == m.selected {
			continue
		}
		label := it.Factory.Label()
		if m.Config.Alerts.Bell && m.Bell != nil {
			m.Bell.Write([]byte("\a"))
		}
		if m.Config.Alerts.MacOS && m.Notify != nil {
			go m.Notify(label + " needs you")
		}
	}
}
