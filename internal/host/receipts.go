package host

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/afonsodelgado/factory/internal/factory"
	"github.com/afonsodelgado/factory/internal/state"
)

const (
	StatusQueued    = "queued"
	StatusSending   = "sending"
	StatusDelivered = "delivered"
	StatusRefused   = "refused"
	StatusUnknown   = "unknown"
	StatusStale     = "stale"
	StatusDismissed = "dismissed"
)

const (
	SourceHub   = "hub"
	SourcePhone = "phone"
	SourcePage  = "review page"
)

const (
	keptSettled = 200
	ReceiptWait = 10 * time.Second
	ReplyWait   = 24 * time.Hour
)

const (
	NotReceived = "Not received by the supervisor. The session is probably showing a stage agent's transcript: open the session (esc, then ⏎), leave that view, then send again."
	NotResolved = "The supervisor received this answer but finished its turn without resolving the item. Send it again, or answer in the session."
	closedFirst = "The item closed before it was sent, so nothing was sent."
)

var ErrNoOperation = errors.New("no operation with that id")

type Receipt struct {
	Operation string       `json:"operation,omitempty"`
	Factory   string       `json:"factory"`
	Item      string       `json:"item,omitempty"`
	Action    string       `json:"action"`
	Status    string       `json:"status"`
	Reason    string       `json:"reason,omitempty"`
	Source    string       `json:"source,omitempty"`
	At        time.Time    `json:"at,omitzero"`
	Reply     *state.Reply `json:"reply,omitempty"`
}

type ActionRequest struct {
	Operation   string    `json:"operation"`
	Device      string    `json:"device"`
	Factory     string    `json:"factory"`
	Incarnation string    `json:"incarnation"`
	Action      string    `json:"action"`
	Item        string    `json:"item,omitempty"`
	Since       time.Time `json:"since,omitzero"`
	Answers     []string  `json:"answers,omitempty"`
	Text        string    `json:"text,omitempty"`
	Option      string    `json:"option,omitempty"`
	Fingerprint string    `json:"fingerprint,omitempty"`
	Generation  int64     `json:"generation,omitempty"`
	Source      string    `json:"source,omitempty"`
}

type ReceiptRequest struct {
	Operation string `json:"operation"`
}

type Settlement struct {
	Key     string
	Refused string
}

type receiptFile struct {
	Claims  map[string]Answer `json:"claims"`
	Settled []Answer          `json:"settled"`
}

func ClaimKey(factoryKey, item string) string { return factoryKey + "|" + item }

func (a Answer) Key() string { return ClaimKey(a.Factory, a.ItemID) }

func (a Answer) Status() string {
	switch {
	case a.Outcome != "":
		return a.Outcome
	case a.Action == ActDismiss:
		return StatusDismissed
	case a.At.IsZero():
		return StatusQueued
	case a.Received.IsZero():
		return StatusSending
	}
	return StatusDelivered
}

func (a Answer) Receipt() Receipt {
	at := a.At
	if !a.SettledAt.IsZero() {
		at = a.SettledAt
	}
	return Receipt{Operation: a.Operation, Factory: a.Factory, Item: a.ItemID, Action: a.Action, Status: a.Status(), Reason: a.Reason, Source: a.Source, At: at, Reply: a.Reply}
}

func (a Answer) Public() Receipt {
	r := a.Receipt()
	r.Operation, r.Reply = "", nil
	return r
}

func (m *Runtime) receiptsPath() string {
	return filepath.Join(m.Config.StateDir, "host", "receipts.json")
}

func (m *Runtime) loadReceipts() error {
	data, err := os.ReadFile(m.receiptsPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var loaded receiptFile
	if err := json.Unmarshal(data, &loaded); err != nil {
		return err
	}
	m.receipts.Settled = loaded.Settled
	for key, a := range loaded.Claims {
		if !a.At.IsZero() {
			m.receipts.Claims[key] = a
		}
	}
	return nil
}

func (m *Runtime) saveReceipts() {
	if m.Config == nil || m.Config.StateDir == "" {
		return
	}
	if err := SavePrivate(m.receiptsPath(), m.receipts); err != nil {
		m.notice = err.Error()
	}
}

func (m *Runtime) Claim(key string) (Answer, bool) {
	a, ok := m.receipts.Claims[key]
	return a, ok
}

func (m *Runtime) restoreClaim(a Answer) {
	if _, claimed := m.Claim(a.Key()); !claimed {
		m.receipts.Claims[a.Key()] = a
	}
}

func (m *Runtime) PutClaim(a Answer) {
	m.receipts.Claims[a.Key()] = a
	m.saveReceipts()
}

func (m *Runtime) DropClaim(key string) {
	delete(m.receipts.Claims, key)
	m.saveReceipts()
}

func (m *Runtime) ForgetClaims(match func(key string) bool) {
	maps.DeleteFunc(m.receipts.Claims, func(key string, _ Answer) bool { return match(key) })
	m.saveReceipts()
}

func (m *Runtime) RecordSent(a Answer, at time.Time) {
	if claimed, ok := m.Claim(a.Key()); ok && claimed.ItemSince.Equal(a.ItemSince) {
		a = claimed
	}
	a.At = at
	m.PutClaim(a)
}

func (m *Runtime) settle(key, outcome, reason string) {
	a, ok := m.receipts.Claims[key]
	if !ok {
		return
	}
	delete(m.receipts.Claims, key)
	a.Outcome, a.Reason, a.SettledAt = outcome, reason, m.Now()
	m.receipts.Settled = append(m.receipts.Settled, a)
	m.receipts.Settled = m.receipts.Settled[max(0, len(m.receipts.Settled)-keptSettled):]
	m.saveReceipts()
}

func (m *Runtime) stillQueued(a Answer) bool {
	claimed, ok := m.Claim(a.Key())
	return ok && claimed.At.IsZero() && claimed.ItemSince.Equal(a.ItemSince)
}

func (m *Runtime) dropAnswer(a Answer, reason string) {
	m.settle(a.Key(), StatusRefused, "Not sent. "+reason)
}

func (m *Runtime) unwanted(a Answer) string {
	it, ok := m.snap.Find(a.Factory)
	switch {
	case !ok:
		return "The factory is gone."
	case it.Factory.Archived():
		return "The factory was archived."
	case Paused(it.Factory):
		return "The factory was paused."
	case itemless(a.Action):
		return ""
	case !stillOpen(it.Factory.State, a.ItemID, a.ItemSince):
		return closedFirst
	}
	return ""
}

func Paused(f factory.Factory) bool {
	return f.Entry != nil && f.Entry.Paused
}

func (m *Runtime) QueueAnswer(it Item, mark Answer, text string) {
	m.PutClaim(mark)
	m.QueueType(QueuedType{Pane: it.Pane.ID, Token: it.Pane.Token, Label: it.Factory.Label(), Text: text, Answers: &mark})
}

func (m *Runtime) answer(it Item, item AttentionItem, mark Answer, text string) string {
	reason := Unqueueable(supervisorTarget(it))
	switch {
	case reason == "":
		m.QueueAnswer(it, mark, text)
		return ""
	case item.Kind != "handoff":
		return reason
	}
	pane, err := m.wakeForHandoff(it)
	if err != nil {
		return err.Error()
	}
	m.PutClaim(mark)
	m.QueueType(QueuedType{Pane: pane, Label: it.Factory.Label(), Text: text, Answers: &mark, NotBefore: m.Now().Add(resumeSettle)})
	return ""
}

func (m *Runtime) wakeForHandoff(it Item) (string, error) {
	switch {
	case m.Engine == nil:
		return "", errors.New(Refusal(supervisorTarget(it)))
	case it.Pane == nil || it.Pane.Dead:
		it.Status = factory.Gone
	case factory.IsShell(it.Pane.Command):
		it.Status = factory.Exited
	default:
		return "", errors.New(Refusal(supervisorTarget(it)))
	}
	return m.wake(it)
}

func (m *Runtime) SettleReceipts(items []Item, clock func(string) time.Time) []Settlement {
	open := openSince(items)
	var settled []Settlement
	for _, key := range slices.Sorted(maps.Keys(m.receipts.Claims)) {
		if s, done := m.settleClaim(m.receipts.Claims[key], open, items, clock); done {
			settled = append(settled, s)
		}
	}
	m.attachReplies(items, clock)
	return settled
}

func (m *Runtime) attachReplies(items []Item, clock func(string) time.Time) {
	attached := false
	for i, a := range m.receipts.Settled {
		if !a.awaitsReply(clock(a.Factory)) {
			continue
		}
		it, found := findItem(items, a.Factory)
		if !found {
			continue
		}
		if reply, ok := replyTo(a.Received, it.Hook); ok {
			m.receipts.Settled[i].Reply = &reply
			attached = true
		}
	}
	if attached {
		m.saveReceipts()
	}
}

func (a Answer) awaitsReply(now time.Time) bool {
	delivered := a.Action == ActMessage && a.Outcome == StatusDelivered && !a.Received.IsZero()
	return delivered && a.Reply == nil && now.Sub(a.Received) < ReplyWait
}

func replyTo(submitted time.Time, hook *state.HookFile) (state.Reply, bool) {
	if hook == nil {
		return state.Reply{}, false
	}
	ownSeen := false
	turnEnd := time.Time{}
	for _, submit := range submitsOf(hook) {
		switch {
		case submit.AgentID != "":
		case submit.At.Equal(submitted):
			ownSeen = true
		case submit.At.After(submitted) && turnEnd.IsZero():
			turnEnd = submit.At
		}
	}
	if !ownSeen {
		return state.Reply{}, false
	}
	for _, reply := range hook.Replies {
		answersThisTurn := !reply.At.Before(submitted) && (turnEnd.IsZero() || reply.At.Before(turnEnd))
		if answersThisTurn {
			return reply, true
		}
	}
	return state.Reply{}, false
}

func openSince(items []Item) map[string]time.Time {
	open := map[string]time.Time{}
	for _, it := range items {
		for _, item := range AttentionItems(it, time.Time{}, nil) {
			if id := ClaimID(item); id != "" {
				open[ClaimKey(it.Factory.Key(), id)] = item.Since
			}
		}
	}
	return open
}

func ClaimID(item AttentionItem) string {
	switch item.Kind {
	case "permission":
		return PermissionID(item.Role)
	case "prompt":
		return PromptID(item.Role)
	case "silent":
		return SilentID
	}
	return item.ID
}

func (m *Runtime) settleClaim(mark Answer, open map[string]time.Time, items []Item, clock func(string) time.Time) (Settlement, bool) {
	key := mark.Key()
	since, stillOpen := open[key]
	closed := !itemless(mark.Action) && (!stillOpen || !since.Equal(mark.ItemSince))
	it, found := findItem(items, mark.Factory)
	switch {
	case closed && mark.At.IsZero():
		m.settle(key, StatusRefused, closedFirst)
		os.Remove(mark.Draft)
		return Settlement{Key: key}, true
	case closed:
		m.settle(key, closedOutcome(mark), "")
		os.Remove(mark.Draft)
		return Settlement{Key: key}, true
	case mark.Action == ActDismiss || mark.At.IsZero() || typedIntoScreen(mark):
		return Settlement{}, false
	case !mark.Received.IsZero():
		if found && factory.IdleAtPrompt(it.Hook) && it.Hook.Main.At.After(mark.Received) {
			m.settle(key, StatusRefused, NotResolved)
			return Settlement{Key: key, Refused: NotResolved}, true
		}
		return Settlement{}, false
	}
	var submits []state.HookEvent
	if found {
		submits = submitsOf(it.Hook)
	}
	receipt, received := receiptIn(mark, submits)
	switch {
	case received && (mark.LeavesOpen || itemless(mark.Action)):
		mark.Received = receipt
		m.receipts.Claims[key] = mark
		m.settle(key, StatusDelivered, "")
		os.Remove(mark.Draft)
		return Settlement{Key: key}, true
	case received:
		mark.Received = receipt
		m.PutClaim(mark)
		os.Remove(mark.Draft)
	case misroutedIn(mark, submits) || clock(mark.Factory).Sub(mark.At) > ReceiptWait:
		m.settle(key, StatusRefused, NotReceived)
		if found && it.Pane != nil {
			m.ClearTyped(it.Pane.ID)
		}
		if found && m.Loader != nil {
			m.Loader.Note(it.Factory, "answer to "+mark.ItemID+" did not reach the supervisor")
		}
		return Settlement{Key: key, Refused: NotReceived}, true
	}
	return Settlement{}, false
}

func closedOutcome(mark Answer) string {
	if mark.Action == ActDismiss {
		return StatusDismissed
	}
	return StatusDelivered
}

func typedIntoScreen(mark Answer) bool {
	return mark.Action == ActPermission || mark.Action == ActPrompt
}

func findItem(items []Item, key string) (Item, bool) {
	for _, it := range items {
		if it.Factory.Key() == key {
			return it, true
		}
	}
	return Item{}, false
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

func receiptIn(s Answer, submits []state.HookEvent) (time.Time, bool) {
	for _, submit := range submits {
		if submit.AgentID != "" || submit.At.Before(s.At) {
			continue
		}
		headIsText := submit.PromptHead != "" && !strings.HasPrefix(submit.PromptHead, "[Pasted text")
		if !headIsText || submit.PromptHead == s.Head {
			return submit.At, true
		}
	}
	return time.Time{}, false
}

func misroutedIn(s Answer, submits []state.HookEvent) bool {
	for _, submit := range submits {
		if submit.AgentID != "" && !submit.At.Before(s.At) {
			return true
		}
	}
	return false
}

func (m *Runtime) act(op string, r ActionRequest) (Receipt, error) {
	if op == "" || op != r.Operation {
		return Receipt{}, errors.New("send act with its operation id as the request's op")
	}
	return m.Act(r)
}

func (m *Runtime) Act(r ActionRequest) (Receipt, error) {
	if uuid.Validate(r.Operation) != nil {
		return Receipt{}, errors.New("operation must be a UUID")
	}
	it, ok := m.snap.Find(r.Factory)
	if !ok || it.Factory.Remote != nil || Incarnation(it.Factory) != r.Incarnation {
		return stale(r, "This factory changed since it was loaded. Nothing was sent."), nil
	}
	if it.Factory.Archived() {
		return stale(r, it.Factory.Label()+" is archived. Nothing was sent."), nil
	}
	if held := m.holdings[r.Factory]; r.Generation != 0 && r.Generation != held.Generation {
		return stale(r, fmt.Sprintf("%s moved since this was loaded (generation %d, now %d). Nothing was sent.", it.Factory.Label(), r.Generation, held.Generation)), nil
	}
	if err := m.unverified(r.Factory); err != nil {
		return refused(r, err.Error()), nil
	}
	switch r.Action {
	case ActPause:
		return lifecycle(r, m.Pause(r.Factory)), nil
	case ActWake:
		return lifecycle(r, m.Wake(r.Factory)), nil
	case ActMessage:
		return m.sendMessage(it, r)
	case ActComments:
		return m.toggleComments(it, r)
	case ActPermission:
		return m.answerPermission(it, r)
	case ActPrompt:
		return m.answerPrompt(it, r)
	}
	return m.answerItem(it, r)
}

func stale(r ActionRequest, reason string) Receipt {
	return Receipt{Operation: r.Operation, Factory: r.Factory, Item: r.Item, Action: r.Action, Status: StatusStale, Reason: reason}
}

func refused(r ActionRequest, reason string) Receipt {
	return Receipt{Operation: r.Operation, Factory: r.Factory, Item: r.Item, Action: r.Action, Status: StatusRefused, Reason: reason, Source: SourcePhone}
}

func lifecycle(r ActionRequest, err error) Receipt {
	if err != nil {
		return refused(r, err.Error())
	}
	return Receipt{Operation: r.Operation, Factory: r.Factory, Action: r.Action, Status: StatusDelivered, Source: SourcePhone}
}

func openItem(it Item, id string, since time.Time) (AttentionItem, bool) {
	for _, item := range AttentionItems(it, time.Time{}, nil) {
		if ClaimID(item) == id && item.Since.Equal(since) {
			return item, true
		}
	}
	return AttentionItem{}, false
}

func (m *Runtime) alreadyClaimed(r ActionRequest) (Receipt, bool) {
	claimed, ok := m.Claim(ClaimKey(r.Factory, r.Item))
	if !ok {
		return Receipt{}, false
	}
	verb := "answered"
	if claimed.Action == ActDismiss {
		verb = "dismissed"
	}
	return stale(r, fmt.Sprintf("Already %s from the %s at %s. Nothing was sent.", verb, sourceName(claimed.Source), claimed.Receipt().At.Local().Format("15:04"))), true
}

func sourceName(source string) string {
	if source == "" {
		return SourceHub
	}
	return source
}

func (m *Runtime) answerItem(it Item, r ActionRequest) (Receipt, error) {
	item, ok := openItem(it, r.Item, r.Since)
	if !ok || !item.Answerable() {
		return stale(r, "This item was answered or replaced since it was loaded. Nothing was sent."), nil
	}
	if !Fits(item, r.Action) {
		return Receipt{}, fmt.Errorf("%s does not apply to a %s item", r.Action, item.Kind)
	}
	if problem := missingInput(item, r); problem != "" {
		return Receipt{}, errors.New(problem)
	}
	if receipt, claimed := m.alreadyClaimed(r); claimed {
		return receipt, nil
	}
	if r.Action == ActDismiss {
		mark := Answer{ItemSince: item.Since, Factory: r.Factory, ItemID: item.ID, Operation: r.Operation, Device: r.Device, Source: SourcePhone, Action: ActDismiss}
		m.dismiss(it, mark)
		return m.receiptOf(mark), nil
	}
	text := supervisorText(it, item, r)
	mark := Answer{ItemSince: item.Since, Factory: r.Factory, ItemID: item.ID, Head: state.PromptHead(text), LeavesOpen: r.Action == ActChanges, Operation: r.Operation, Device: r.Device, Source: SourcePhone, Action: r.Action}
	if reason := m.answer(it, item, mark, text); reason != "" {
		return refused(r, "Not sent. "+reason), nil
	}
	m.note(it.Factory, "answered "+item.ID+" from a phone")
	return m.receiptOf(mark), nil
}

func (m *Runtime) dismiss(it Item, mark Answer) {
	mark.At = m.Now()
	m.PutClaim(mark)
	m.note(it.Factory, "dismissed "+mark.ItemID+" from the "+sourceName(mark.Source)+" without telling the supervisor")
}

func supervisorText(it Item, item AttentionItem, r ActionRequest) string {
	return factory.ForSupervisor(it.Factory.Supervisor().ToolName(), ActionText(item, r.Action, r.Option, r.Answers, r.Text))
}

type Preview struct {
	Text string `json:"text"`
}

func (m *Runtime) Preview(r ActionRequest) (Preview, error) {
	it, ok := m.snap.Find(r.Factory)
	if !ok || it.Factory.Remote != nil || Incarnation(it.Factory) != r.Incarnation {
		return Preview{}, errors.New("This factory changed since it was loaded.")
	}
	if r.Action == ActMessage {
		return Preview{Text: r.Text}, nil
	}
	item, ok := openItem(it, r.Item, r.Since)
	if !ok || !Fits(item, r.Action) {
		return Preview{}, errors.New("This item was answered or replaced since it was loaded.")
	}
	if r.Action == ActDismiss {
		return Preview{}, nil
	}
	return Preview{Text: supervisorText(it, item, r)}, nil
}

func missingInput(item AttentionItem, r ActionRequest) string {
	keepsWorking := r.Action == ActContinue && item.Kind == "handoff"
	needsText := r.Action == ActChanges || r.Action == ActReply || keepsWorking || (r.Action == ActAnswer && !item.AsksQuestions())
	switch {
	case r.Action == ActAnswer && item.AsksQuestions() && !everyAnswered(r.Answers, len(item.Questions)):
		return fmt.Sprintf("answer all %d questions", len(item.Questions))
	case needsText && strings.TrimSpace(r.Text) == "":
		return "type the text to send first"
	case r.Action == ActContinue && item.Kind == "cap" && !validCapRounds(r.Option):
		return fmt.Sprintf("allow between 1 and %d more rounds", MaxCapRounds)
	}
	return ""
}

func validCapRounds(option string) bool {
	_, ok := CapRounds(option)
	return ok
}

func everyAnswered(answers []string, questions int) bool {
	return len(answers) == questions && !slices.ContainsFunc(answers, func(a string) bool { return strings.TrimSpace(a) == "" })
}

func supervisorTarget(it Item) Target {
	return Target{Label: it.Factory.Label(), Pane: it.Pane, Hook: it.Hook}
}

func (m *Runtime) receiptOf(mark Answer) Receipt {
	if claimed, ok := m.Claim(mark.Key()); ok {
		return claimed.Receipt()
	}
	for _, a := range slices.Backward(m.receipts.Settled) {
		if a.Operation == mark.Operation {
			return a.Receipt()
		}
	}
	return mark.Receipt()
}

func (m *Runtime) sendMessage(it Item, r ActionRequest) (Receipt, error) {
	switch {
	case strings.TrimSpace(r.Text) == "":
		return Receipt{}, errors.New("type a message first")
	case utf8.RuneCountInString(r.Text) > MaxMessageRunes:
		return Receipt{}, fmt.Errorf("a message holds at most %d characters", MaxMessageRunes)
	}
	if reason := Unqueueable(supervisorTarget(it)); reason != "" {
		return refused(r, "Not sent. "+reason), nil
	}
	mark := Answer{Factory: r.Factory, ItemID: "message-" + r.Operation, Head: state.PromptHead(r.Text), Operation: r.Operation, Device: r.Device, Source: SourcePhone, Action: ActMessage}
	m.QueueAnswer(it, mark, r.Text)
	m.note(it.Factory, "sent a message from a phone")
	return m.receiptOf(mark), nil
}

func (m *Runtime) toggleComments(it Item, r ActionRequest) (Receipt, error) {
	if r.Option != "on" && r.Option != "off" {
		return Receipt{}, errors.New("comment watching is turned on or off")
	}
	if reason := Unqueueable(supervisorTarget(it)); reason != "" {
		return refused(r, "Not sent. "+reason), nil
	}
	if !CanToggleComments(it) {
		return refused(r, "Not sent. Comment watching changes only on a running factory with its supervisor open."), nil
	}
	source := SourcePhone
	if r.Source == SourceHub {
		source = SourceHub
	}
	text := factory.ForSupervisor(it.Factory.Supervisor().ToolName(), CommentsCommand(r.Option))
	mark := Answer{Factory: r.Factory, ItemID: "comments-" + r.Operation, Head: state.PromptHead(text), Operation: r.Operation, Device: r.Device, Source: source, Action: ActComments}
	m.QueueAnswer(it, mark, text)
	m.note(it.Factory, "asked the supervisor to turn comment watching "+r.Option)
	return m.receiptOf(mark), nil
}

func itemless(action string) bool {
	return action == ActMessage || action == ActComments
}

func (m *Runtime) LookupReceipt(r ReceiptRequest) (Receipt, error) {
	if a, ok := m.answerFor(r); ok {
		return a.Receipt(), nil
	}
	op, ok := readOperation(m.Config.StateDir, r.Operation)
	var request ActionRequest
	if !ok || op.Action != "act" || json.Unmarshal(op.Data, &request) != nil {
		return Receipt{}, ErrNoOperation
	}
	switch {
	case op.Status == "accepted":
		return Receipt{Operation: r.Operation, Factory: request.Factory, Item: request.Item, Action: request.Action, Status: StatusUnknown, Reason: "The owner stopped while acting on this request. Look at the session before sending again."}, nil
	case op.Reply.T == "error":
		return refused(request, op.Reply.Error), nil
	}
	var receipt Receipt
	if err := json.Unmarshal(op.Reply.Data, &receipt); err != nil {
		return Receipt{}, err
	}
	return receipt, nil
}

func (m *Runtime) answerFor(r ReceiptRequest) (Answer, bool) {
	matches := func(a Answer) bool { return a.Operation == r.Operation }
	for _, a := range m.receipts.Claims {
		if matches(a) {
			return a, true
		}
	}
	for _, a := range slices.Backward(m.receipts.Settled) {
		if matches(a) {
			return a, true
		}
	}
	return Answer{}, false
}

func readOperation(stateDir, id string) (Operation, bool) {
	var op Operation
	if uuid.Validate(id) != nil {
		return op, false
	}
	data, err := os.ReadFile(filepath.Join(stateDir, "operations", id+".json"))
	return op, err == nil && json.Unmarshal(data, &op) == nil
}
