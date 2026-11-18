package hub

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/rivo/uniseg"

	"github.com/afonsodelgado/factory/internal/factory"
	"github.com/afonsodelgado/factory/internal/host"
	"github.com/afonsodelgado/factory/internal/node"
	"github.com/afonsodelgado/factory/internal/review"
	"github.com/afonsodelgado/factory/internal/state"
	"github.com/afonsodelgado/factory/internal/ui/footer"
	"github.com/afonsodelgado/factory/internal/ui/panel"
)

type panelState struct {
	tab     panel.Tab
	item    int
	row     int
	all     bool
	shown   string
	drafts  map[string]*panel.Draft
	refused map[string]string
	press   lastPress
}

type lastPress struct {
	key string
	at  time.Time
}

type sentMark = host.Answer

const repeatWindow = time.Second

func (m *Model) clearRefusals() {
	open := map[string]bool{}
	for _, it := range m.snap.Items {
		for _, item := range m.attentionItems(it, nil) {
			open[draftKey(it.Factory, item.ID)] = item.ID != ""
		}
	}
	for key := range m.panel.refused {
		if !open[key] {
			delete(m.panel.refused, key)
		}
	}
}

func (m *Model) shownItems(it Item) (shown []panel.Item, settled []panel.Settled) {
	for _, item := range m.attentionItems(it, m.threadCounter(it.Factory)) {
		delivery, sent := m.delivery(it, item)
		if item.ID == "" || !sent {
			shown = append(shown, item)
			continue
		}
		settled = append(settled, panel.Settled{ID: item.ID, State: delivery})
	}
	return shown, settled
}

func (m *Model) delivery(it Item, item panel.Item) (panel.Progress, bool) {
	if it.Factory.Remote != nil {
		return m.sentToNode(it, item)
	}
	mark, sent := m.claim(draftKey(it.Factory, item.ID))
	return progress(mark), sent
}

func progress(s sentMark) panel.Progress {
	switch {
	case s.At.IsZero():
		return panel.Queued
	case s.Received.IsZero():
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
		if _, sent := m.claim(draftKey(it.Factory, a.ID)); !sent {
			return false
		}
	}
	return len(open) > 0
}

func (m *Model) anAnswerIsQueued(it Item) bool {
	for _, a := range it.Factory.State.OpenAttention() {
		if mark, sent := m.claim(draftKey(it.Factory, a.ID)); sent && progress(mark) == panel.Queued {
			return true
		}
	}
	return false
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
			if m.anAnswerIsQueued(*it) {
				it.Status = factory.AnswerQueued
			}
		}
	}
}

func newPanelState() panelState {
	return panelState{
		drafts:  map[string]*panel.Draft{},
		refused: map[string]string{},
	}
}

func (m *Model) attentionItems(it Item, counter threadCounter) []panel.Item {
	if it.Factory.Remote != nil {
		return m.nodeAttention(it)
	}
	return host.AttentionItems(it, m.snap.At, counter)
}

type threadCounter = host.ThreadCounter

func (m *Model) threadCounter(f factory.Factory) threadCounter {
	if f.Remote != nil {
		return nil
	}
	return func(url string) (int, bool) {
		open, counted := m.threads[url]
		return open, counted
	}
}

func artifactRow(a state.Artifact, counter threadCounter) panel.ArtifactRow {
	return host.NewArtifactRow(a, counter)
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
	repo := f.Repo
	if f.Remote != nil {
		repo = f.Key()
	}
	return filepath.Join(m.Config.DraftsDir(), state.FileStem(repo, f.Slug), itemID+".json")
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
	m.dropClaim(key)
	os.Remove(m.draftPath(it.Factory, item.ID))
}

func (m *Model) target(it Item) Target {
	return Target{Label: it.Factory.Label(), Pane: it.Pane, Hook: it.Hook}
}

func (m *Model) message(item panel.Item, d *panel.Draft, action string) string {
	r := m.answerBody(item, d, action)
	return host.ActionText(item, r.Action, r.Answers, r.Text)
}

func (m *Model) answerBody(item panel.Item, d *panel.Draft, action string) node.ActionBody {
	r := node.ActionBody{Action: hostAction(item, action), Item: item.ID, Since: item.Since, Text: d.Text}
	if r.Action == host.ActApprove {
		r.Text = m.gateNotes(item, d)
	}
	r.Answers = make([]string, len(item.Questions))
	for i, q := range item.Questions {
		r.Answers[i] = d.Answer(q, i)
	}
	return r
}

func hostAction(item panel.Item, action string) string {
	switch {
	case action == "handled":
		return host.ActHandled
	case asksQuestions(item):
		return host.ActAnswer
	case item.Kind == "gate" && action == "approve":
		return host.ActApprove
	case item.Kind == "gate":
		return host.ActChanges
	case item.Kind == "comment":
		return host.ActReply
	case action == "continue" || action == "retry":
		return action
	}
	return host.ActAnswer
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
	handoffAction := action == "handled" || action == "continue"
	if !ok || !item.Answerable() || (item.Kind == "handoff" && !handoffAction) {
		return
	}
	key := draftKey(it.Factory, item.ID)
	d := m.draft(it.Factory, item)
	if action != "handled" && unanswered(item, d) != "" {
		return
	}
	if reason := m.unverified(it); reason != "" {
		m.panel.refused[key] = "Not sent. " + heldUnchecked(it, reason)
		return
	}
	remote := it.Factory.Remote != nil
	wakesItsSupervisor := item.Kind == "handoff"
	if reason := unqueueable(m.target(it)); reason != "" && !remote && !wakesItsSupervisor {
		m.panel.refused[key] = "Not sent. " + reason
		m.saveDraft()
		return
	}
	delete(m.panel.refused, key)
	m.saveDraft()
	m.panel.press = lastPress{key: key, at: m.Now()}
	if remote {
		m.answerOnNode(it, m.answerBody(item, d, action))
		return
	}
	text := factory.ForSupervisor(it.Factory.Supervisor().ToolName(), m.message(item, d, action))
	mark := sentMark{ItemSince: item.Since, Factory: it.Factory.Key(), ItemID: item.ID, Head: state.PromptHead(text), Draft: m.draftPath(it.Factory, item.ID), Source: host.SourceHub, Action: hostAction(item, action)}
	mark.LeavesOpen = item.Kind == "gate" && action == "changes"
	m.queueAnswer(mark, text)
}

func (m *Model) repeatedOntoNextItem(it Item, item panel.Item, key string) bool {
	switch key {
	case "ctrl+s", "ctrl+a", "ctrl+r", "ctrl+k", "enter":
	default:
		return false
	}
	p := m.panel.press
	return p.key != "" && p.key != draftKey(it.Factory, item.ID) && m.Now().Sub(p.at) < repeatWindow
}

func (m *Model) queueAnswer(mark sentMark, text string) {
	var claimed sentMark
	if err := m.ask("queue-answer", host.QueuedAnswer{Answer: mark, Text: text}, &claimed); err != nil {
		m.panel.refused[mark.Key()] = withFullStop(err.Error())
		return
	}
	if claimed.Factory != "" {
		m.putClaim(claimed)
	}
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
	v.Label = f.Placed()
	v.LocalPages = f.Remote == nil && m.Config.NodeOrigin() == ""
	if f.State != nil && f.State.Loops.Active != "" {
		v.Label += " · " + loopStage(f)
	}
	if lf, ok := m.nodeFactories[f.Key()]; ok && lf.Loop != "" {
		v.Label += " · " + workflowStage(lf.Stage, lf.Loop, lf.Round)
	}
	v.Items, v.Settled = m.shownItems(it)
	if next, ok := m.nextElsewhere(); ok && len(v.Items) == 0 {
		v.Next = next.Factory.Label()
	}
	v.Item = max(0, min(m.panel.item, len(v.Items)-1))
	if f.State != nil {
		for _, a := range f.State.Artifacts() {
			v.Artifacts = append(v.Artifacts, artifactRow(a, m.threadCounter(f)))
		}
	}
	v.PRs = m.prsFor(f)
	v.Checked = m.prsChecked
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
	if it.Factory.Remote != nil {
		e := m.nodeSendable(it, item)
		s.Ready, s.Reason = e.Ready, e.Reason
		return s
	}
	e := host.Eligible(t, "the answer")
	s.Ready, s.Reason = e.Ready, e.Reason
	return s
}

func (m *Model) prsFor(f factory.Factory) []panel.PR {
	if f.State == nil {
		return nil
	}
	cached := m.prs[f.Key()]
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
		if i.Factory.Remote == nil && m.Config.StateDir != "" {
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
		return m.openDialog("keys", struct{}{})
	}
	if m.panel.tab != panel.Attention {
		return m.listKey(key)
	}
	switch {
	case hasItem && item.Kind == "permission" && key == "enter":
		m.focusSession()
	case !hasItem && key == "enter":
		m.nextNeedingYou()
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
	sends := key == "ctrl+s" || key == "ctrl+a" || key == "ctrl+r" || key == "ctrl+k"
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
	case "ctrl+k":
		m.deliver("handled")
	case "ctrl+o":
		return m.openReview(itemURL(item, d), itemTitle(item, d)), true
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
		m.notice = problem(nothingTyped(item.Kind))
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
	case "handoff":
		return "continue"
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
		return "Type an instruction first, or ctrl+r for one more round"
	case "failure":
		return "Type an instruction first, or ctrl+r to retry"
	case "handoff":
		return "Type what to do next first, or ctrl+k to mark it done"
	case "design_return":
		return "Type accept or decline first: ctrl+s sends it"
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

func itemTitle(item panel.Item, d *panel.Draft) string {
	if d.Artifact < len(item.Artifacts) {
		return item.Artifacts[d.Artifact].Title
	}
	return ""
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
			m.report(m.ask("refresh-prs", nil, nil))
		}
	case "ctrl+o":
		return m.openRow(v)
	case "enter":
		if m.panel.tab == panel.Activity && m.panel.row < len(v.Activity) && v.Activity[m.panel.row].Key != "" {
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
		return m.openReview(v.Artifacts[m.panel.row].URL, v.Artifacts[m.panel.row].Title)
	}
	return nil
}

const doorWait = 5 * time.Second

func (m *Model) openReview(url, title string) tea.Cmd {
	it, ok := m.selectedItem()
	if !ok || it.Factory.Remote != nil {
		return m.openCmd(url)
	}
	page, served := review.MobilePath(host.ReviewFactory(it), url)
	origin := m.Config.NodeOrigin()
	switch {
	case !served:
		return m.openCmd(url)
	case origin != "":
		return m.openCmd(origin + page)
	}
	return m.openThroughDoor(page, cmp.Or(title, path.Base(page)))
}

func (m *Model) openThroughDoor(page, title string) tea.Cmd {
	if m.ReviewLink == nil || m.Open == nil {
		return nil
	}
	link, open := m.ReviewLink, m.Open
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), doorWait)
		defer cancel()
		url, err := link(ctx, page)
		if err != nil {
			return noticeMsg(problem("The page did not open: " + withFullStop(err.Error())))
		}
		open(url)
		return noticeMsg(news("Opened " + title + " in your browser. The link works once."))
	}
}

func (m *Model) openCmd(url string) tea.Cmd {
	if url == "" || m.Open == nil {
		return nil
	}
	if it, ok := m.selectedItem(); ok && it.Factory.Remote != nil && !host.WebLink(url) {
		m.notice = problem(url + " is a page on " + it.Factory.Remote.Name + "; the factory's Pages tab in the phone app opens it.")
		return nil
	}
	open := m.Open
	return func() tea.Msg {
		open(url)
		return nil
	}
}

func (m *Model) alert(previous map[string]factory.Status) {
	if previous == nil {
		return
	}
	for _, it := range m.snap.Items {
		key := it.Factory.Key()
		was, known := previous[key]
		backFromOutage := was == factory.Unreachable
		if !known || was == factory.NeedsYou || backFromOutage || it.Status != factory.NeedsYou || key == m.selected {
			continue
		}
		label := it.Factory.Placed()
		if m.Config.Alerts.Bell && m.Bell != nil {
			m.Bell.Write([]byte("\a"))
		}
		if m.Config.Alerts.Notifies() && m.Notify != nil {
			go m.Notify(label + " needs you")
		}
	}
}
