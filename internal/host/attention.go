package host

import (
	"cmp"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/afonsodelgado/factory/internal/factory"
	"github.com/afonsodelgado/factory/internal/gitclone"
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
	Loop        string               `json:"loop,omitempty"`
	LimitKey    string               `json:"limit_key,omitempty"`
	Revision    uint64               `json:"revision"`
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
	Revising    string               `json:"revising,omitempty"`
	Screen      []string             `json:"screen,omitempty"`
	Node        string               `json:"node,omitempty"`
	Pane        *tmux.Pane           `json:"-"`
	Prompt      *state.PendingPrompt `json:"-"`
}

type Screen struct {
	Node  string
	Role  string
	Pane  *tmux.Pane
	Lines []string
	Menu  *Menu
	Since time.Time
}

func (i AttentionItem) Answerable() bool {
	switch i.Kind {
	case "question", "commands", "design_return", "gate", "comment", "cap", "failure", "handoff":
		return true
	}
	return false
}

func (i AttentionItem) AsksQuestions() bool {
	return i.Kind == "question" && len(i.Questions) > 0
}

type ThreadCounter func(url string) (int, bool)

const (
	PermissionBody    = "Claude Code is showing a permission prompt in the session. Answer it there; the panel cannot."
	AnswerInSession   = "⏎ answer in the session"
	AnswerItInSession = "⏎ answer it in the session"
	OpenSession       = "⏎ open the session"
	WakeKey           = "w wake"
	SilentID          = "silent"
)

var toolNames = map[string]string{state.ToolClaude: "Claude Code", state.ToolCodex: "Codex", state.ToolOpenCode: "OpenCode"}

func PermissionID(role string) string {
	if role == "" {
		return "permission"
	}
	return "permission-" + role
}

func PromptID(role string) string {
	if role == "" {
		return "prompt"
	}
	return "prompt-" + role
}

func AttentionItems(it Item, now time.Time, counter ThreadCounter) []AttentionItem {
	var items []AttentionItem
	f := it.Factory
	if f.StateErr != nil {
		items = append(items, unreadableItem(f))
	}
	if f.State != nil {
		for _, a := range f.State.OpenAttention() {
			item := fromAttention(f.State, a, counter)
			item.Revising = it.Revising[a.ID]
			items = append(items, item)
		}
	}
	items = append(items, permissionItems(it)...)
	items = append(items, stuckItems(it)...)
	pausedOnPurpose := it.Status == factory.Gone && Paused(f)
	if it.Status.Asleep() && !pausedOnPurpose {
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
	for _, stage := range []*StageView{it.Stage, it.Discovery} {
		if stage != nil && factory.PermissionPrompt(stage.Hook) {
			items = append(items, permissionItem(stage.Hook.Prompt, stage.Run.Role, stage.Pane))
		}
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

func stuckItems(it Item) []AttentionItem {
	switch {
	case it.Screen != nil && it.Screen.Menu != nil:
		return []AttentionItem{promptItem(it)}
	case it.Unreported:
		return []AttentionItem{silentItem(it)}
	}
	return nil
}

func promptItem(it Item) AttentionItem {
	s := it.Screen
	item := AttentionItem{
		Kind: "prompt", Title: "waits at a prompt", Since: s.Since, Action: AnswerItInSession, Role: s.Role, Pane: s.Pane, Screen: s.Lines,
		Body: it.Factory.Label() + "'s supervisor waits at a prompt factory did not expect, and has sent no hook event since it showed it.",
	}
	if s.Role != "" && it.Stage != nil {
		stage := s.Role
		if it.Factory.State != nil && it.Factory.State.Stage != "" {
			stage = it.Factory.State.Stage
		}
		item.Title = "the " + s.Role + " waits at a prompt"
		item.Body = fmt.Sprintf("The %s stage, on %s, waits at a prompt factory did not expect. The %s stops until it is answered.", s.Role, toolNames[it.Stage.Run.Setting.ToolName()], stage)
	}
	return item
}

func silentItem(it Item) AttentionItem {
	s := it.Screen
	if s == nil {
		s = &Screen{Pane: it.Pane}
	}
	quiet := fmt.Sprintf("%dm", int(factory.StartTimeout.Minutes()))
	body := fmt.Sprintf("%s has not reported in for %s. %s started its supervisor at %s and has had no hook event and no state since.", it.Factory.Label(), quiet, cmp.Or(s.Node, "This machine"), it.Factory.CreatedAt().Local().Format("15:04"))
	if len(s.Lines) > 0 {
		body += " Its screen ends with:"
	}
	return AttentionItem{Kind: "silent", Title: "not reported in for " + quiet, Body: body, Since: it.Factory.CreatedAt(), Action: OpenSession, Pane: s.Pane, Screen: s.Lines}
}

func fromAttention(st *state.State, a state.Attention, counter ThreadCounter) AttentionItem {
	item := AttentionItem{ID: a.ID, Loop: a.Loop, LimitKey: a.LimitKey, Revision: a.Revision, Kind: a.Kind, Title: a.Title, Body: a.Body, ArtifactURL: a.ArtifactURL, ThreadID: a.ThreadID, Since: a.CreatedAt.Time, Stage: a.Stage()}
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
	if a.Kind == "gate" || a.Kind == "design_return" {
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
	Key           string       `json:"key"`
	Route         string       `json:"route"`
	Incarnation   string       `json:"incarnation"`
	ID            string       `json:"id"`
	Title         string       `json:"title,omitempty"`
	Repo          string       `json:"repo"`
	RepoName      string       `json:"repo_name"`
	GitURL        string       `json:"git_url,omitempty"`
	NoRemote      bool         `json:"no_remote,omitempty"`
	RemoteProblem string       `json:"remote_problem,omitempty"`
	Stage         string       `json:"stage,omitempty"`
	Loop          string       `json:"loop,omitempty"`
	Round         int          `json:"round,omitempty"`
	Status        string       `json:"status"`
	Paused        bool         `json:"paused"`
	NeedsSince    time.Time    `json:"needs_since,omitzero"`
	Items         []LocalItem  `json:"items"`
	Resolved      []Resolution `json:"resolved,omitempty"`
	Pages         []LocalPage  `json:"pages"`
	ReviewStack   *StackStatus `json:"review_stack,omitempty"`
	Message       Eligibility  `json:"message"`
	CanPause      bool         `json:"can_pause"`
	CanWake       bool         `json:"can_wake"`
	Pane          string       `json:"pane,omitempty"`
	StageRole     string       `json:"stage_role,omitempty"`
	StageLabel    string       `json:"stage_label,omitempty"`
	StagePane     string       `json:"stage_pane,omitempty"`
	Generation    int64        `json:"generation,omitempty"`
	Unverified    string       `json:"unverified,omitempty"`
}

type LocalPage struct {
	Name        string `json:"name"`
	Title       string `json:"title"`
	URL         string `json:"url,omitempty"`
	OpenThreads *int   `json:"open_threads,omitempty"`
	Href        string `json:"href,omitempty"`
	Unavailable string `json:"unavailable,omitempty"`
}

func localPage(f review.Factory, row ArtifactRow) LocalPage {
	page := LocalPage{Name: row.Name, Title: row.Title, URL: row.URL, OpenThreads: row.OpenThreads}
	href, static := review.MobilePath(f, row.URL)
	switch {
	case static:
		page.Href = href
	case strings.HasPrefix(row.URL, "http"):
		page.Unavailable = "It runs on the app's dev server; the review stack link opens the app once factory review stack has started it."
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
	Menu       *Menu           `json:"menu,omitempty"`
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
	consider := func(since time.Time) {
		if oldest.IsZero() || since.Before(oldest) {
			oldest = since
		}
	}
	if it.Factory.State != nil {
		for _, a := range it.Factory.State.OpenAttention() {
			consider(a.CreatedAt.Time)
		}
	}
	for _, item := range stuckItems(it) {
		consider(item.Since)
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
	for _, it := range m.verificationSnapshot().Items {
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
		Key: f.Key(), Route: ReviewKey(f), Incarnation: Incarnation(f), ID: f.Label(), Title: f.Title(), Repo: f.Repo, RepoName: m.remote(f.Repo).name, GitURL: m.remote(f.Repo).gitURL, NoRemote: m.remote(f.Repo).localOnly, RemoteProblem: m.remote(f.Repo).problem,
		Status: it.Status.String(), Paused: Paused(f), Items: []LocalItem{}, Pages: []LocalPage{}, Message: Eligible(target, "the message"),
		CanPause: it.Pane != nil && !it.Pane.Dead, CanWake: it.Status.Asleep() && it.Status != factory.Done,
		Generation: m.holdings[f.Key()].Generation, Unverified: m.holdings[f.Key()].Unverified,
	}
	if f.State != nil {
		lf.Stage = f.State.Stage
		lf.Loop, lf.Round = f.State.Loops.Active, f.State.Stages[f.State.Stage].Rounds
		if lf.Loop != "" {
			lf.Round = f.State.Loops.Used[lf.Loop]
		}
		for _, a := range f.State.Artifacts() {
			lf.Pages = append(lf.Pages, localPage(ReviewFactory(it), NewArtifactRow(a, m.threadCounter())))
		}
	}
	if lf.CanPause {
		lf.Pane = it.Pane.Token
	}
	if s := it.Stage; s != nil && s.Pane != nil && !s.Pane.Dead {
		lf.StageRole, lf.StageLabel, lf.StagePane = s.Run.Role, StageLabel(s.Run), s.Pane.Token
	}
	for _, item := range AttentionItems(it, m.snap.At, m.threadCounter()) {
		lf.Items = append(lf.Items, m.localItem(it, item, target))
	}
	if s, ok := m.reviewStack(f.Key()); ok {
		lf.ReviewStack = &s
		if s.State == StackFailed {
			lf.Items = append(lf.Items, m.localItem(it, stackItem(s), target))
		}
	}
	lf.Resolved = m.resolutions(f)
	switch {
	case it.Status == factory.NeedsYou && everyItemDismissed(lf.Items):
		lf.Status = it.Unblocked.String()
	case it.Status == factory.NeedsYou:
		lf.NeedsSince = NeedsSince(it)
	}
	return lf
}

func everyItemDismissed(items []LocalItem) bool {
	dismissed := func(li LocalItem) bool { return li.Answer != nil && li.Answer.Status == StatusDismissed }
	return len(items) > 0 && !slices.ContainsFunc(items, func(li LocalItem) bool { return !dismissed(li) })
}

func (m *Runtime) threadCounter() ThreadCounter {
	if m.Pages == nil {
		return nil
	}
	return m.Pages.OpenCount
}

const remoteRecheck = time.Minute

type repoRemote struct {
	name      string
	gitURL    string
	localOnly bool
	problem   string
	at        time.Time
}

func (m *Runtime) remote(repo string) repoRemote {
	if known, ok := m.remotes[repo]; ok && m.Now().Sub(known.at) < remoteRecheck {
		return known
	}
	gitURL, err := gitclone.RemoteURL(repo)
	known := repoRemote{name: repoName(repo), gitURL: gitURL, localOnly: errors.Is(err, gitclone.ErrNoNetworkRemote), at: m.Now()}
	if err != nil && !known.localOnly {
		known.problem = err.Error()
	}
	m.remotes[repo] = known
	return m.remotes[repo]
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
	switch item.Kind {
	case "prompt":
		li.ID, li.Node, li.Menu = PromptID(item.Role), m.Machine, it.Screen.Menu
		li.Send = Eligibility{Ready: true, Reason: "Types the choice you pick into the session."}
	case "silent":
		li.ID, li.Node = SilentID, m.Machine
		li.Send = Eligibility{Reason: "Open the session from a computer."}
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
