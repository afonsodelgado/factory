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
	sent    map[string]sentMark
	prs     map[string][]panel.PR
	press   lastPress
}

type lastPress struct {
	key string
	at  time.Time
}

type sentMark struct {
	at         time.Time
	itemSince  time.Time
	factory    string
	itemID     string
	head       string
	draft      string
	received   time.Time
	leavesOpen bool
}

const receiptWait = 10 * time.Second

const repeatWindow = time.Second

const notReceived = "Not received by the supervisor. The session is probably showing a stage agent's transcript: open the session (esc, then ⏎), leave that view, then send again."

const notResolved = "The supervisor received this answer but finished its turn without resolving the item. Send it again, or answer in the session."

func (m *Model) forgetClosedItems() {
	open := map[string]time.Time{}
	for _, it := range m.snap.Items {
		for _, item := range attentionItems(it, m.snap.At, m.threadCounter()) {
			if item.ID != "" {
				open[draftKey(it.Factory, item.ID)] = item.Since
			}
		}
	}
	for key, mark := range m.panel.sent {
		if since, stillOpen := open[key]; !stillOpen || !since.Equal(mark.itemSince) {
			m.forgetSent(key, mark)
		}
	}
	for key := range m.panel.refused {
		if _, stillOpen := open[key]; !stillOpen {
			delete(m.panel.refused, key)
		}
	}
}

func (m *Model) checkReceipts() {
	for key, mark := range m.panel.sent {
		if mark.at.IsZero() {
			continue
		}
		if !mark.received.IsZero() {
			if m.turnEndedSince(mark) {
				delete(m.panel.sent, key)
				m.panel.refused[key] = notResolved
			}
			continue
		}
		var submits []state.HookEvent
		if it, ok := m.snap.Find(mark.factory); ok {
			submits = submitsOf(it.Hook)
		}
		receipt, received := mark.receiptIn(submits)
		switch {
		case received && mark.leavesOpen:
			m.forgetSent(key, mark)
		case received:
			mark.received = receipt
			m.panel.sent[key] = mark
			os.Remove(mark.draft)
		case mark.misroutedIn(submits) || m.snap.At.Sub(mark.at) > receiptWait:
			delete(m.panel.sent, key)
			m.panel.refused[key] = notReceived
			it, ok := m.snap.Find(mark.factory)
			if ok && it.Pane != nil {
				delete(m.typedAt, it.Pane.ID)
			}
			if ok && m.Loader != nil {
				m.Loader.Note(it.Factory, "answer to "+mark.itemID+" did not reach the supervisor")
			}
		}
	}
}

func (m *Model) forgetSent(key string, mark sentMark) {
	delete(m.panel.sent, key)
	delete(m.panel.drafts, key)
	os.Remove(mark.draft)
}

func (m *Model) turnEndedSince(mark sentMark) bool {
	it, ok := m.snap.Find(mark.factory)
	return ok && factory.IdleAtPrompt(it.Hook) && it.Hook.Main.At.After(mark.received)
}

func submitsOf(hook *state.HookFile) []state.HookEvent {
	switch {
	case hook == nil:
		return nil
	case len(hook.Submits) > 0:
		return hook.Submits
	case hook.Submit != nil:
		return []state.HookEvent{*hook.Submit}
	}
	return nil
}

func (s sentMark) receiptIn(submits []state.HookEvent) (time.Time, bool) {
	for _, submit := range submits {
		if submit.AgentID != "" || submit.At.Before(s.at) {
			continue
		}
		headIsText := submit.PromptHead != "" && !strings.HasPrefix(submit.PromptHead, "[Pasted text")
		if !headIsText || submit.PromptHead == s.head {
			return submit.At, true
		}
	}
	return time.Time{}, false
}

func (s sentMark) misroutedIn(submits []state.HookEvent) bool {
	for _, submit := range submits {
		if submit.AgentID != "" && !submit.At.Before(s.at) {
			return true
		}
	}
	return false
}

func (m *Model) shownItems(it Item) (shown []panel.Item, settled []panel.Settled) {
	for _, item := range attentionItems(it, m.snap.At, m.threadCounter()) {
		mark, sent := m.panel.sent[draftKey(it.Factory, item.ID)]
		if item.ID == "" || !sent {
			shown = append(shown, item)
			continue
		}
		settled = append(settled, panel.Settled{ID: item.ID, State: mark.progress()})
	}
	return shown, settled
}

func (s sentMark) progress() panel.Progress {
	switch {
	case s.at.IsZero():
		return panel.Queued
	case s.received.IsZero():
		return panel.Sending
	}
	return panel.Answered
}

func (m *Model) everyItemAnswered(it Item) bool {
	if it.Factory.State == nil {
		return false
	}
	open := it.Factory.State.OpenAttention()
	for _, a := range open {
		if _, sent := m.panel.sent[draftKey(it.Factory, a.ID)]; !sent {
			return false
		}
	}
	return len(open) > 0
}

func (m *Model) settle() {
	for i := range m.snap.Items {
		it := &m.snap.Items[i]
		loaded, ok := m.loaded[it.Factory.Key()]
		if !ok {
			continue
		}
		it.Status = loaded
		if loaded == factory.NeedsYou && m.everyItemAnswered(*it) {
			it.Status = it.Unblocked
		}
	}
}

func newPanelState() panelState {
	return panelState{
		drafts:  map[string]*panel.Draft{},
		refused: map[string]string{},
		sent:    map[string]sentMark{},
		prs:     map[string][]panel.PR{},
	}
}

func attentionItems(it Item, now time.Time, counter threadCounter) []panel.Item {
	var items []panel.Item
	f := it.Factory
	if f.StateErr != nil {
		items = append(items, unreadableItem(f))
	}
	if f.State != nil {
		for _, a := range f.State.OpenAttention() {
			items = append(items, fromAttention(f.State, a, counter))
		}
	}
	if factory.PermissionPrompt(it.Hook) {
		items = append(items, panel.Item{
			Kind:   "permission",
			Title:  "waiting on a permission prompt",
			Body:   "Claude Code is showing a permission prompt in the session. Answer it there; the panel cannot.",
			Since:  it.Hook.Prompt.At,
			Action: "⏎ answer in the session",
		})
	}
	if it.Status.Asleep() {
		items = append(items, asleepItem(it, now))
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].Since.Before(items[j].Since) })
	return items
}

func fromAttention(st *state.State, a state.Attention, counter threadCounter) panel.Item {
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
			item.Artifacts = append(item.Artifacts, artifactRow(art, counter))
		}
	}
	return item
}

type threadCounter func(url string) (int, bool)

func artifactRow(a state.Artifact, counter threadCounter) panel.ArtifactRow {
	title := a.Title
	if title == "" {
		title = a.URL
	}
	row := panel.ArtifactRow{Name: path(a.URL), Title: title, URL: a.URL, OpenThreads: a.OpenThreads, CheckedAt: a.ThreadsCheckedAt.Time}
	if counter == nil {
		return row
	}
	if open, ok := counter(a.URL); ok {
		row.OpenThreads, row.CheckedAt, row.Live = &open, time.Time{}, true
	}
	return row
}

func path(url string) string {
	return url[strings.LastIndex(url, "/")+1:]
}

func unreadableItem(f factory.Factory) panel.Item {
	file := filepath.Join(f.Dir, "state.json")
	if rel, err := filepath.Rel(f.Repo, file); err == nil && !strings.HasPrefix(rel, "..") {
		file = rel
	}
	return panel.Item{
		Kind:  "unreadable",
		Title: "state.json cannot be read",
		Body:  fmt.Sprintf("%s does not parse, so its stage, PRs and open items stay hidden until the supervisor writes it again. %s.", file, f.StateErr),
		Since: f.StateMod,
	}
}

func stageOf(f factory.Factory) string {
	switch {
	case f.State != nil:
		return f.State.Stage
	case f.StateErr != nil:
		return "unreadable"
	}
	return ""
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
	items, _ := m.shownItems(it)
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
	return filepath.Join(m.Config.DraftsDir(), state.FileStem(f.Repo, f.Slug), itemID+".json")
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
	if !ok && asksQuestions(item) {
		enterQuestion(item, d, d.Question)
	}
	if item.Kind == "gate" {
		d.Artifact = max(0, min(d.Artifact, approveRow(item)))
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
	delete(m.panel.sent, key)
	os.Remove(m.draftPath(it.Factory, item.ID))
}

func (m *Model) target(it Item) Target {
	return Target{Label: it.Factory.Label(), Pane: it.Pane, Hook: it.Hook}
}

func (m *Model) refreshed(t Target) Target {
	if m.Engine != nil && t.Pane != nil {
		if panes, err := m.Engine.TM.Panes(); err == nil {
			id := t.Pane.ID
			t.Pane = nil
			if p, ok := byID(panes, id); ok {
				t.Pane = &p
			}
		}
	}
	if t.Hook != nil && m.Config.StateDir != "" {
		if hook, err := state.ReadHookFile(filepath.Join(m.Config.SessionsDir(), t.Hook.SessionID+".json")); err == nil {
			t.Hook = hook
		}
	}
	return t
}

func (m *Model) message(item panel.Item, d *panel.Draft, action string) string {
	switch {
	case action == "handled":
		return Handled(item.ID)
	case asksQuestions(item):
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

func approveRow(item panel.Item) int {
	return len(item.Artifacts)
}

func onApproveRow(item panel.Item, d *panel.Draft) bool {
	return item.Kind == "gate" && d.Artifact == approveRow(item)
}

func (m *Model) gateNotes(item panel.Item, d *panel.Draft) string {
	if len(item.Artifacts) < 2 || onApproveRow(item, d) {
		return d.Text
	}
	return strings.TrimSpace("Approve " + item.Artifacts[d.Artifact].Title + ". " + d.Text)
}

func (m *Model) deliver(action string) {
	it, item, ok := m.currentAttention()
	if !ok || !item.Answerable() {
		return
	}
	key := draftKey(it.Factory, item.ID)
	if m.Typist == nil {
		return
	}
	d := m.draft(it.Factory, item)
	if action != "handled" && unanswered(item, d) != "" {
		return
	}
	if reason := unqueueable(m.target(it)); reason != "" {
		m.panel.refused[key] = "Not sent. " + reason
		m.saveDraft()
		return
	}
	delete(m.panel.refused, key)
	m.saveDraft()
	text := factory.ForSupervisor(it.Factory.Supervisor().ToolName(), m.message(item, d, action))
	mark := m.answerMark(it.Factory, item.ID, item.Since, text)
	mark.leavesOpen = item.Kind == "gate" && action == "changes"
	m.queueAnswer(it, mark, text)
	m.panel.press = lastPress{key: key, at: m.Now()}
	if m.Loader != nil {
		m.Loader.Note(it.Factory, "answered "+item.ID+" from the panel")
	}
}

func (m *Model) repeatedOntoNextItem(it Item, item panel.Item, key string) bool {
	switch key {
	case "ctrl+s", "ctrl+a", "ctrl+r", "ctrl+x", "ctrl+k", "enter":
	default:
		return false
	}
	p := m.panel.press
	return p.key != "" && p.key != draftKey(it.Factory, item.ID) && m.Now().Sub(p.at) < repeatWindow
}

func (m *Model) queueAnswer(it Item, mark sentMark, text string) {
	m.panel.sent[mark.factory+"|"+mark.itemID] = mark
	m.queueType(queuedType{Pane: it.Pane.ID, Token: it.Pane.Token, Label: it.Factory.Label(), Text: text, Answers: &mark})
}

func (m *Model) stillQueued(answer sentMark) bool {
	mark, ok := m.panel.sent[answer.factory+"|"+answer.itemID]
	return ok && mark.at.IsZero() && mark.itemSince.Equal(answer.itemSince)
}

func (m *Model) dropAnswer(answer sentMark, reason string) {
	key := answer.factory + "|" + answer.itemID
	delete(m.panel.sent, key)
	m.panel.refused[key] = "Not sent. " + reason
}

func (m *Model) answerMark(f factory.Factory, itemID string, itemSince time.Time, text string) sentMark {
	return sentMark{itemSince: itemSince, factory: f.Key(), itemID: itemID, head: state.PromptHead(text), draft: m.draftPath(f, itemID)}
}

func (m *Model) recordSent(mark sentMark, at time.Time) {
	mark.at = at
	m.panel.sent[mark.factory+"|"+mark.itemID] = mark
}

func unanswered(item panel.Item, d *panel.Draft) string {
	if item.Kind != "question" {
		return ""
	}
	if i := d.Unanswered(item.Questions); i >= 0 {
		return fmt.Sprintf("Question %d has no recommended answer. Pick or type one first.", i+1)
	}
	return ""
}

func (m *Model) answering() bool {
	if m.focus != footer.Panel || m.panel.tab != panel.Attention {
		return false
	}
	_, item, ok := m.currentAttention()
	return ok && item.Answerable()
}

func (m *Model) panelView() panel.View {
	v := panel.View{Tab: m.panel.tab, Focused: m.focus == footer.Panel, Collapsed: m.collapsed, Dim: m.focus == footer.Dialog, Now: m.Now(), Row: m.panel.row, All: m.panel.all}
	it, ok := m.selectedItem()
	if !ok {
		return v
	}
	f := it.Factory
	v.Label = f.Label()
	v.Items, v.Settled = m.shownItems(it)
	v.Item = max(0, min(m.panel.item, len(v.Items)-1))
	if f.State != nil {
		for _, a := range f.State.Artifacts() {
			v.Artifacts = append(v.Artifacts, artifactRow(a, m.threadCounter()))
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
	s := panel.Send{Refused: m.panel.refused[draftKey(it.Factory, item.ID)]}
	if missing := unanswered(item, d); missing != "" {
		s.Reason = missing
		return s
	}
	t := m.target(it)
	s.Preview = factory.ForSupervisor(it.Factory.Supervisor().ToolName(), m.message(item, d, "approve"))
	s.Reason = unqueueable(t)
	switch {
	case s.Reason != "":
	case Refusal(t) != "":
		s.Ready, s.Reason = true, fmt.Sprintf("%s is not at its prompt yet; the answer is sent once it is.", it.Factory.Label())
	default:
		s.Ready, s.Reason = true, fmt.Sprintf("Sends 1 message to %s, which is idle at its prompt.", it.Factory.Label())
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
	switch key {
	case "ctrl+n":
		m.moveItem(1)
		return nil
	case "ctrl+p":
		m.moveItem(-1)
		return nil
	}
	it, item, hasItem := m.currentAttention()
	answerFormShown := hasItem && item.Answerable() && m.panel.tab == panel.Attention
	if answerFormShown {
		if cmd, handled := m.answerKey(it, item, key, text); handled {
			return cmd
		}
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
		m.moveItem(-1)
		return nil
	case "]":
		m.moveItem(1)
		return nil
	case "q", "ctrl+c":
		return m.quit()
	case "?":
		return m.openDialog("keys", nil)
	}
	if m.panel.tab != panel.Attention {
		return m.listKey(key)
	}
	if hasItem && item.Kind == "permission" && key == "enter" {
		m.focusSession()
	}
	return nil
}

func (m *Model) moveItem(delta int) {
	m.saveDraft()
	m.panel.item = max(m.panel.item+delta, 0)
}

func (m *Model) switchTab(delta int) {
	m.panel.tab = m.panel.tab.Next(delta)
	m.panel.row = 0
}

func (m *Model) answerKey(it Item, item panel.Item, key, text string) (tea.Cmd, bool) {
	d := m.draft(it.Factory, item)
	if key == "tab" || key == "shift+tab" {
		return nil, false
	}
	if m.repeatedOntoNextItem(it, item, key) {
		return nil, true
	}
	if cmd, isAction := m.answerAction(item, d, key); isAction {
		return cmd, true
	}
	switch {
	case key == "esc" && d.Editing:
		d.Editing = false
		m.saveDraft()
	case key == "esc":
		return nil, false
	case asksQuestions(item):
		m.questionKey(item, d, key, text)
	case key == "enter" && d.Editing:
		typeInto(item, d, "\n")
	case key == "enter" && onApproveRow(item, d):
		m.deliver("approve")
	case key == "enter":
		startTyping(item, d)
	case key == "backspace":
		field := answerField(item, d)
		*field = dropLast(*field)
	case text != "":
		typeInto(item, d, text)
	case !d.Editing:
		moveIn(item, d, key)
	}
	return nil, true
}

func (m *Model) questionKey(item panel.Item, d *panel.Draft, key, text string) {
	switch {
	case key == "enter" && d.Editing:
		confirmTyped(item, d)
	case key == "enter":
		m.pick(item, d)
	case onSendRow(item, d):
		moveInQuestions(item, d, key)
	case key == "backspace":
		field := answerField(item, d)
		*field = dropLast(*field)
	case text != "":
		typeInto(item, d, text)
	case !d.Editing:
		moveInQuestions(item, d, key)
	}
}

func (m *Model) pick(item panel.Item, d *panel.Draft) {
	if onSendRow(item, d) {
		m.sendAnswers(item, d)
		return
	}
	if d.Cursor == len(item.Questions[d.Question].Options) {
		startTyping(item, d)
		return
	}
	d.Choice[d.Question] = d.Cursor
	enterQuestion(item, d, d.Question+1)
}

func (m *Model) sendAnswers(item panel.Item, d *panel.Draft) {
	if i := d.Unanswered(item.Questions); i >= 0 {
		enterQuestion(item, d, i)
		return
	}
	m.deliver("")
}

func confirmTyped(item panel.Item, d *panel.Draft) {
	if strings.TrimSpace(d.Typed[d.Question]) == "" {
		return
	}
	d.Editing = false
	enterQuestion(item, d, d.Question+1)
}

func (m *Model) answerAction(item panel.Item, d *panel.Draft, key string) (tea.Cmd, bool) {
	sends := key == "ctrl+s" || key == "ctrl+a" || key == "ctrl+r" || key == "ctrl+x" || key == "ctrl+k"
	if sends {
		d.Editing = false
	}
	switch key {
	case "ctrl+s":
		m.sendTyped(item, d)
	case "ctrl+a":
		if item.Kind == "gate" {
			m.deliver("approve")
		}
	case "ctrl+r":
		m.recommendedOrRetry(item, d)
	case "ctrl+x":
		if item.Kind == "cap" || item.Kind == "failure" {
			m.deliver("skip")
		}
	case "ctrl+k":
		m.deliver("handled")
	case "ctrl+o":
		return m.openCmd(itemURL(item, d)), true
	case "ctrl+d":
		m.discardDraft()
	case "ctrl+u":
		if !onSendRow(item, d) {
			*answerField(item, d) = ""
		}
	default:
		return nil, false
	}
	return nil, true
}

func (m *Model) recommendedOrRetry(item panel.Item, d *panel.Draft) {
	switch {
	case item.Kind == "cap":
		m.deliver("continue")
	case item.Kind == "failure":
		m.deliver("retry")
	case asksQuestions(item) && !onSendRow(item, d):
		d.Choice[d.Question] = -1
		enterQuestion(item, d, d.Question+1)
	}
}

func (m *Model) sendTyped(item panel.Item, d *panel.Draft) {
	if asksQuestions(item) {
		m.deliver("")
		return
	}
	if strings.TrimSpace(d.Text) == "" {
		m.notice = nothingTyped(item.Kind)
		return
	}
	m.deliver(textAction(item.Kind))
}

func textAction(kind string) string {
	switch kind {
	case "gate":
		return "changes"
	case "comment":
		return "reply"
	}
	return ""
}

func nothingTyped(kind string) string {
	switch kind {
	case "gate":
		return "Type notes first: ctrl+s sends them as change requests, ctrl+a approves"
	case "comment":
		return "Type a reply first: ctrl+s sends it"
	case "cap":
		return "Type an instruction first, or ctrl+r for one more round, ctrl+x to skip"
	case "failure":
		return "Type an instruction first, or ctrl+r to retry, ctrl+x to skip"
	}
	return "Type an answer first: ctrl+s sends it"
}

func answerField(item panel.Item, d *panel.Draft) *string {
	if asksQuestions(item) {
		return &d.Typed[d.Question]
	}
	return &d.Text
}

func asksQuestions(item panel.Item) bool {
	return item.Kind == "question" && len(item.Questions) > 0
}

func onSendRow(item panel.Item, d *panel.Draft) bool {
	return asksQuestions(item) && d.Question == len(item.Questions)
}

func enterQuestion(item panel.Item, d *panel.Draft, i int) {
	d.Question = max(0, min(i, len(item.Questions)))
	if !onSendRow(item, d) {
		d.Cursor = d.Picked(item.Questions[d.Question], d.Question)
	}
}

func startTyping(item panel.Item, d *panel.Draft) {
	d.Editing = true
	if asksQuestions(item) {
		typeRow := len(item.Questions[d.Question].Options)
		d.Choice[d.Question] = typeRow
		d.Cursor = typeRow
	}
}

func typeInto(item panel.Item, d *panel.Draft, text string) {
	if onSendRow(item, d) {
		return
	}
	startTyping(item, d)
	*answerField(item, d) += text
}

func moveIn(item panel.Item, d *panel.Draft, key string) {
	if item.Kind != "gate" {
		return
	}
	switch key {
	case "up":
		d.Artifact = max(d.Artifact-1, 0)
	case "down":
		d.Artifact = min(d.Artifact+1, approveRow(item))
	}
}

func moveInQuestions(item panel.Item, d *panel.Draft, key string) {
	sendRow := len(item.Questions)
	atTop := d.Question == sendRow || d.Cursor == item.Questions[d.Question].TopRow()
	atBottom := d.Question < sendRow && d.Cursor == len(item.Questions[d.Question].Options)
	switch {
	case key == "left" || key == "up" && atTop:
		if d.Question > 0 {
			enterQuestion(item, d, d.Question-1)
		}
	case key == "right" || key == "down" && atBottom:
		enterQuestion(item, d, d.Question+1)
	case key == "up":
		d.Cursor--
	case key == "down" && d.Question < sendRow:
		d.Cursor++
	}
}

func itemURL(item panel.Item, d *panel.Draft) string {
	if d.Artifact < len(item.Artifacts) {
		return item.Artifacts[d.Artifact].URL
	}
	return item.ArtifactURL
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
	if !ok || !item.Answerable() || m.panel.tab != panel.Attention {
		return
	}
	typeInto(item, m.draft(it.Factory, item), text)
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
	url = m.reviewURL(url)
	if live := m.openLive(url); live != nil {
		return live
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
		if m.Config.Alerts.Notifies() && m.Notify != nil {
			go m.Notify(label + " needs you")
		}
	}
}
