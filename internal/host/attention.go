package host

import (
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/afonsodelgado/factory/internal/factory"
	"github.com/afonsodelgado/factory/internal/review"
	"github.com/afonsodelgado/factory/internal/state"
	"github.com/afonsodelgado/factory/internal/tmux"
)

type Question struct {
	Text        string   `json:"text"`
	Options     []string `json:"options"`
	Recommended string   `json:"recommended,omitempty"`
}

func (q Question) RecommendedIndex() int {
	for i, o := range q.Options {
		if o == q.Recommended {
			return i
		}
	}
	return -1
}

func (q Question) TopRow() int {
	if q.Recommended != "" && q.RecommendedIndex() < 0 {
		return -1
	}
	return 0
}

type ArtifactRow struct {
	Name        string    `json:"name"`
	Title       string    `json:"title"`
	URL         string    `json:"url"`
	OpenThreads *int      `json:"open_threads,omitempty"`
	CheckedAt   time.Time `json:"checked_at,omitzero"`
	Live        bool      `json:"live,omitempty"`
}

type AttentionItem struct {
	ID          string               `json:"id"`
	Kind        string               `json:"kind"`
	Title       string               `json:"title"`
	Body        string               `json:"body,omitempty"`
	Questions   []Question           `json:"questions,omitempty"`
	Artifacts   []ArtifactRow        `json:"artifacts,omitempty"`
	ArtifactURL string               `json:"artifact_url,omitempty"`
	ThreadID    string               `json:"thread_id,omitempty"`
	Stage       string               `json:"stage,omitempty"`
	Status      factory.Status       `json:"-"`
	Since       time.Time            `json:"since"`
	Detail      []string             `json:"detail,omitempty"`
	Action      string               `json:"-"`
	Role        string               `json:"role,omitempty"`
	Pane        *tmux.Pane           `json:"-"`
	Prompt      *state.PendingPrompt `json:"-"`
}

func (i AttentionItem) Answerable() bool {
	switch i.Kind {
	case "question", "gate", "comment", "cap", "failure", "handoff":
		return true
	}
	return false
}

func (i AttentionItem) AsksQuestions() bool {
	return i.Kind == "question" && len(i.Questions) > 0
}

type ThreadCounter func(url string) (int, bool)

const (
	PermissionBody  = "Claude Code is showing a permission prompt in the session. Answer it there; the panel cannot."
	AnswerInSession = "⏎ answer in the session"
	WakeKey         = "w wake"
)

func PermissionID(role string) string {
	if role == "" {
		return "permission"
	}
	return "permission-" + role
}

func AttentionItems(it Item, now time.Time, counter ThreadCounter) []AttentionItem {
	var items []AttentionItem
	f := it.Factory
	if f.StateErr != nil {
		items = append(items, unreadableItem(f))
	}
	if f.State != nil {
		for _, a := range f.State.OpenAttention() {
			items = append(items, fromAttention(f.State, a, counter))
		}
	}
	items = append(items, permissionItems(it)...)
	slotExplainsIt := it.Status == factory.Gone
	if it.Status.Asleep() && !slotExplainsIt {
		items = append(items, asleepItem(it, now))
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].Since.Before(items[j].Since) })
	return items
}

func permissionItems(it Item) []AttentionItem {
	var items []AttentionItem
	if factory.PermissionPrompt(it.Hook) {
		items = append(items, permissionItem(it.Hook.Prompt, "", it.Pane))
	}
	if it.Stage != nil && factory.PermissionPrompt(it.Stage.Hook) {
		items = append(items, permissionItem(it.Stage.Hook.Prompt, it.Stage.Run.Role, it.Stage.Pane))
	}
	return items
}

func permissionItem(prompt *state.PendingPrompt, role string, pane *tmux.Pane) AttentionItem {
	title := "waiting on a permission prompt"
	if role != "" {
		title = "the " + role + " stage is waiting on a permission prompt"
	}
	return AttentionItem{
		Kind:   "permission",
		Title:  title,
		Body:   PermissionBody,
		Since:  prompt.At,
		Action: AnswerInSession,
		Role:   role,
		Pane:   pane,
		Prompt: prompt,
	}
}

func fromAttention(st *state.State, a state.Attention, counter ThreadCounter) AttentionItem {
	item := AttentionItem{ID: a.ID, Kind: a.Kind, Title: a.Title, Body: a.Body, ArtifactURL: a.ArtifactURL, ThreadID: a.ThreadID, Since: a.CreatedAt.Time, Stage: a.Stage()}
	for i, q := range a.Questions {
		question := Question{Text: q}
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
			item.Artifacts = append(item.Artifacts, NewArtifactRow(art, counter))
		}
	}
	return item
}

func NewArtifactRow(a state.Artifact, counter ThreadCounter) ArtifactRow {
	title := a.Title
	if title == "" {
		title = a.URL
	}
	row := ArtifactRow{Name: a.URL[strings.LastIndex(a.URL, "/")+1:], Title: title, URL: a.URL, OpenThreads: a.OpenThreads, CheckedAt: a.ThreadsCheckedAt.Time}
	if counter == nil {
		return row
	}
	if open, ok := counter(a.URL); ok {
		row.OpenThreads, row.CheckedAt, row.Live = &open, time.Time{}, true
	}
	return row
}

func unreadableItem(f factory.Factory) AttentionItem {
	file := filepath.Join(f.Dir, "state.json")
	if rel, err := filepath.Rel(f.Repo, file); err == nil && !strings.HasPrefix(rel, "..") {
		file = rel
	}
	return AttentionItem{
		Kind:  "unreadable",
		Title: "state.json cannot be read",
		Body:  fmt.Sprintf("%s does not parse, so its stage, PRs and open items stay hidden until the supervisor writes it again. %s.", file, f.StateErr),
		Since: f.StateMod,
	}
}

func asleepItem(it Item, now time.Time) AttentionItem {
	f := it.Factory
	item := AttentionItem{Kind: "asleep", Status: it.Status, Since: LastSign(it), Action: WakeKey}
	switch it.Status {
	case factory.Gone:
		item.Title = "gone · no tmux pane"
		item.Body = "No tmux pane, and the factory isn't done. The worktree is intact."
	case factory.Exited:
		item.Title = "exited · the pane is a shell"
		item.Body = "The pane still exists, so wake reuses it rather than making a new one."
	default:
		item.Title = "stalled · no activity"
		item.Body = fmt.Sprintf("Idle and waiting on %s, with no hook event, state write or heartbeat for %s.", f.WaitingOn(), AgeOf(now, item.Since))
	}
	if id := f.SessionID(); id != "" {
		item.Detail = append(item.Detail, "session id   "+id)
	}
	if f.Worktree != "" {
		item.Detail = append(item.Detail, "worktree     "+Tilde(f.Worktree))
	}
	if f.State != nil && f.State.Stage != "" {
		item.Detail = append(item.Detail, "stage        "+f.State.Stage)
	}
	return item
}

func LastSign(it Item) time.Time {
	newest := it.Factory.StateMod
	if it.Factory.Heartbeat.After(newest) {
		newest = it.Factory.Heartbeat
	}
	if it.Hook != nil && it.Hook.Last.At.After(newest) {
		newest = it.Hook.Last.At
	}
	return newest
}

func AgeOf(now, then time.Time) string {
	if then.IsZero() {
		return "a while"
	}
	return now.Sub(then).Round(time.Minute).String()
}

func Tilde(path string) string {
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

const keptResolutions = 20

type Eligibility struct {
	Ready  bool   `json:"ready"`
	Queues bool   `json:"queues,omitempty"`
	Reason string `json:"reason"`
}

func Eligible(t Target, what string) Eligibility {
	if reason := Unqueueable(t); reason != "" {
		return Eligibility{Reason: reason}
	}
	if Refusal(t) != "" {
		return Eligibility{Ready: true, Queues: true, Reason: fmt.Sprintf("%s is not at its prompt yet; %s is sent once it is.", t.Label, what)}
	}
	return Eligibility{Ready: true, Reason: fmt.Sprintf("Sends 1 message to %s, which is idle at its prompt.", t.Label)}
}

type Local struct {
	At        time.Time      `json:"at"`
	Factories []LocalFactory `json:"factories"`
}

type LocalFactory struct {
	Key         string       `json:"key"`
	Route       string       `json:"route"`
	Incarnation string       `json:"incarnation"`
	ID          string       `json:"id"`
	Title       string       `json:"title,omitempty"`
	Repo        string       `json:"repo"`
	RepoName    string       `json:"repo_name"`
	Stage       string       `json:"stage,omitempty"`
	Status      string       `json:"status"`
	Paused      bool         `json:"paused"`
	NeedsSince  time.Time    `json:"needs_since,omitzero"`
	Items       []LocalItem  `json:"items"`
	Resolved    []Resolution `json:"resolved,omitempty"`
	Pages       []LocalPage  `json:"pages"`
	Walkthrough *StackStatus `json:"walkthrough,omitempty"`
	Message     Eligibility  `json:"message"`
	CanPause    bool         `json:"can_pause"`
	CanWake     bool         `json:"can_wake"`
	Pane        string       `json:"pane,omitempty"`
	StageRole   string       `json:"stage_role,omitempty"`
	StagePane   string       `json:"stage_pane,omitempty"`
	Generation  int64        `json:"generation,omitempty"`
	Unverified  string       `json:"unverified,omitempty"`
}

type LocalPage struct {
	Name        string `json:"name"`
	Title       string `json:"title"`
	OpenThreads *int   `json:"open_threads,omitempty"`
	Href        string `json:"href,omitempty"`
	Unavailable string `json:"unavailable,omitempty"`
}

func localPage(f review.Factory, row ArtifactRow) LocalPage {
	page := LocalPage{Name: row.Name, Title: row.Title, OpenThreads: row.OpenThreads}
	href, static := review.MobilePath(f, row.URL)
	switch {
	case static:
		page.Href = href
	case strings.HasPrefix(row.URL, "http"):
		page.Unavailable = "It runs on the app's dev server; the walkthrough link opens the app once factory review stack has started it."
	default:
		page.Unavailable = "It is not an HTML page in this factory's folder."
	}
	return page
}

type LocalItem struct {
	AttentionItem
	Key        string          `json:"key"`
	Answer     *Receipt        `json:"answer,omitempty"`
	Send       Eligibility     `json:"send"`
	Permission *PermissionMenu `json:"permission,omitempty"`
}

type Resolution struct {
	ID         string    `json:"id"`
	Kind       string    `json:"kind"`
	Since      time.Time `json:"since"`
	ResolvedAt time.Time `json:"resolved_at"`
	Source     string    `json:"source,omitempty"`
	At         time.Time `json:"at,omitzero"`
}

func NeedsSince(it Item) time.Time {
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

func (m *Runtime) Local() Local {
	local := Local{At: m.snap.At, Factories: []LocalFactory{}}
	for _, it := range m.snap.Items {
		if it.Factory.Remote == nil && !it.Factory.Archived() {
			local.Factories = append(local.Factories, m.localFactory(it))
		}
	}
	return local
}

func (m *Runtime) localFactory(it Item) LocalFactory {
	f := it.Factory
	target := supervisorTarget(it)
	lf := LocalFactory{
		Key: f.Key(), Route: ReviewKey(f), Incarnation: Incarnation(f), ID: f.Label(), Title: f.Title(), Repo: f.Repo, RepoName: m.repoName(f.Repo),
		Status: it.Status.String(), Paused: Paused(f), Items: []LocalItem{}, Pages: []LocalPage{}, Message: Eligible(target, "the message"),
		CanPause: it.Pane != nil && !it.Pane.Dead, CanWake: it.Status.Asleep() && it.Status != factory.Done,
		Generation: m.holdings[f.Key()].Generation, Unverified: m.holdings[f.Key()].Unverified,
	}
	if f.State != nil {
		lf.Stage = f.State.Stage
		for _, a := range f.State.Artifacts() {
			lf.Pages = append(lf.Pages, localPage(ReviewFactory(it), NewArtifactRow(a, m.threadCounter())))
		}
	}
	if it.Status == factory.NeedsYou {
		lf.NeedsSince = NeedsSince(it)
	}
	if lf.CanPause {
		lf.Pane = it.Pane.Token
	}
	if s := it.Stage; s != nil && s.Pane != nil && !s.Pane.Dead {
		lf.StageRole, lf.StagePane = s.Run.Role, s.Pane.Token
	}
	for _, item := range AttentionItems(it, m.snap.At, m.threadCounter()) {
		lf.Items = append(lf.Items, m.localItem(it, item, target))
	}
	if s, ok := m.walkthrough(f.Key()); ok {
		lf.Walkthrough = &s
		if s.State == StackFailed {
			lf.Items = append(lf.Items, m.localItem(it, stackItem(s), target))
		}
	}
	lf.Resolved = m.resolutions(f)
	return lf
}

func (m *Runtime) threadCounter() ThreadCounter {
	if m.Pages == nil {
		return nil
	}
	return m.Pages.OpenCount
}

func (m *Runtime) repoName(repo string) string {
	if name, ok := m.repoNames[repo]; ok {
		return name
	}
	m.repoNames[repo] = repoName(repo)
	return m.repoNames[repo]
}

func (m *Runtime) localItem(it Item, item AttentionItem, target Target) LocalItem {
	li := LocalItem{AttentionItem: item, Send: Eligible(target, "the answer")}
	if item.Kind == "handoff" && !li.Send.Ready {
		li.Send = Eligibility{Ready: true, Queues: true, Reason: "No supervisor is running, so this wakes a fresh one and sends it once that one is at its prompt."}
	}
	if item.Kind == "stack" {
		li.Send = Eligibility{Reason: "Fix what it names, then run factory review stack again."}
	}
	if item.Kind == "permission" {
		li.ID = PermissionID(item.Role)
		li.Send = Eligibility{Reason: "Answer it in the session."}
		if menu, ok := m.PermissionMenu(item); ok {
			li.Permission = &menu
			li.Body = "Claude Code is asking for permission in the session. Pick an answer here, or answer it there."
			li.Send = Eligibility{Ready: true, Reason: "Types the option you pick into the session."}
		}
	}
	li.Key = ItemKey(Incarnation(it.Factory), li.ID, item.Since, item.Pane)
	if claimed, ok := m.Claim(ClaimKey(it.Factory.Key(), li.ID)); ok && claimed.ItemSince.Equal(item.Since) {
		public := claimed.Public()
		li.Answer = &public
	}
	return li
}

func ItemKey(incarnation, id string, since time.Time, pane *tmux.Pane) string {
	identity := []string{incarnation, id, since.UTC().Format(time.RFC3339Nano)}
	if pane != nil {
		identity = append(identity, pane.ID)
	}
	sum := sha256.Sum256([]byte(strings.Join(identity, "\x00")))
	return base64.RawURLEncoding.EncodeToString(sum[:12])
}

func (m *Runtime) resolutions(f factory.Factory) []Resolution {
	if f.State == nil {
		return nil
	}
	var resolved []Resolution
	for _, a := range f.State.Attention {
		if a.Open() {
			continue
		}
		r := Resolution{ID: a.ID, Kind: a.Kind, Since: a.CreatedAt.Time, ResolvedAt: a.ResolvedAt.Time}
		if answer, ok := m.settledAnswer(f.Key(), a); ok {
			r.Source, r.At = sourceName(answer.Source), answer.Receipt().At
		}
		resolved = append(resolved, r)
	}
	sort.SliceStable(resolved, func(i, j int) bool { return resolved[i].ResolvedAt.After(resolved[j].ResolvedAt) })
	return resolved[:min(len(resolved), keptResolutions)]
}

func (m *Runtime) settledAnswer(key string, a state.Attention) (Answer, bool) {
	for _, s := range slices.Backward(m.receipts.Settled) {
		if s.Key() == ClaimKey(key, a.ID) && s.ItemSince.Equal(a.CreatedAt.Time) && s.Outcome == StatusDelivered {
			return s, true
		}
	}
	return Answer{}, false
}
