package host

import (
	"bytes"
	"cmp"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/goccy/go-yaml"
	"github.com/google/uuid"

	"github.com/afonsodelgado/factory/bundle"
	"github.com/afonsodelgado/factory/internal/catalog"
	"github.com/afonsodelgado/factory/internal/factory"
	"github.com/afonsodelgado/factory/internal/state"
	"github.com/afonsodelgado/factory/internal/tmux"
)

const (
	resumeSettle = 5 * time.Second
	AnswerWait   = 10 * time.Minute
	submitWait   = time.Minute
)

type StageRequest struct {
	EffectID string   `json:"effect_id"`
	Action   string   `json:"action"`
	Token    string   `json:"token"`
	Cwd      string   `json:"cwd"`
	Role     string   `json:"role"`
	Text     string   `json:"text"`
	PRs      []string `json:"prs,omitempty"`
	Factory  string   `json:"factory,omitempty"`
}

type StageReply struct {
	Result string `json:"result"`
}

type QueuedType struct {
	QueuedAt  time.Time
	TypingAt  time.Time
	Pane      string
	Token     string
	SessionID string
	Label     string
	Text      string
	NotBefore time.Time
	Delivers  string
	Record    string
	Answers   *Answer
}

type StageView struct {
	Run  state.StageRun
	Pane *tmux.Pane
	Hook *state.HookFile
}

func (m *Runtime) Stage(r StageRequest) (StageReply, error) {
	if r.Action == "ci" {
		return m.WaitForCI(r)
	}
	if !slices.Contains(state.AgentRoles, r.Role) {
		return StageReply{}, fmt.Errorf("%q is not a role; roles are %s", r.Role, strings.Join(state.AgentRoles, ", "))
	}
	it, ok := m.stageItem(r)
	if !ok {
		return StageReply{}, errors.New("no factory owns this session; run factory stage from a factory's supervisor pane, or name it with --factory")
	}
	if it.Factory.Dir == "" || it.Factory.Worktree == "" {
		return StageReply{}, fmt.Errorf("%s has no factory directory or worktree yet; write state.json first", it.Factory.Label())
	}
	if err := m.unverified(it.Factory.Key()); err != nil {
		return StageReply{}, err
	}
	if it.Factory.State == nil || it.Factory.State.Workflow.ActiveRun != r.EffectID || r.EffectID == "" {
		return StageReply{}, errors.New("commit a workflow stage effect before dispatch")
	}
	if len(it.Factory.State.Workflow.Runs) == 0 {
		return StageReply{}, errors.New("committed effect has no run")
	}
	effect := it.Factory.State.Workflow.Pending
	active := it.Factory.State.Workflow.Runs[len(it.Factory.State.Workflow.Runs)-1]
	if active.ID != r.EffectID || active.Role != r.Role || (effect != nil && (effect.ID != r.EffectID || effect.Role != r.Role || effect.Kind != "stage")) {
		return StageReply{}, errors.New("stage request does not match the committed effect")
	}
	lockPath := state.StageRunsPath(m.Config.StateDir, it.Factory.Repo, it.Factory.Slug) + ".lock"
	if err := os.MkdirAll(filepath.Dir(lockPath), 0755); err != nil {
		return StageReply{}, err
	}
	lock, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return StageReply{}, err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return StageReply{}, err
	}
	runs, err := state.LoadStageRuns(state.StageRunsPath(m.Config.StateDir, it.Factory.Repo, it.Factory.Slug))
	if err != nil {
		return StageReply{}, err
	}
	for i := len(runs.Runs) - 1; i >= 0; i-- {
		previous := runs.Runs[i]
		if previous.ID != r.EffectID {
			continue
		}
		live := m.preparing[it.Factory.Key()] == previous.Token || m.leases[it.Factory.Key()].Token == previous.Token
		if _, ok := LiveStagePane(stagePaneOf(m.currentPanes(), it.Factory, r.Role, previous.Token)); ok {
			live = true
		}
		if previous.Finished() || live {
			return StageReply{Result: previous.Result}, nil
		}
		if active.Retries <= previous.Attempt {
			return StageReply{}, errors.New("stage dispatch outcome is uncertain; reconcile the saved pane intent before retrying")
		}
		break
	}
	if effect == nil {
		return StageReply{}, errors.New("acknowledged dispatch has no receipt; reconcile before retrying")
	}
	if r.Action == "send" {
		return m.SendStage(it, runs, r)
	}
	return m.StartStage(it, runs, r)
}

func (m *Runtime) WaitForCI(r StageRequest) (StageReply, error) {
	it, ok := m.stageItem(r)
	if !ok || it.Factory.Dir == "" {
		return StageReply{}, errors.New("no factory owns this session; run factory stage ci from a factory's supervisor pane")
	}
	if len(r.PRs) == 0 {
		return StageReply{}, errors.New("name the pull requests to wait for")
	}
	script, err := m.CiScript()
	if err != nil {
		return StageReply{}, err
	}
	if it.Factory.State == nil || it.Factory.State.Workflow.ActiveRun == "" {
		return StageReply{}, errors.New("CI watcher requires an active run")
	}
	command := factory.ShellQuote(script) + " --dir " + factory.ShellQuote(it.Factory.Dir) + " --run " + factory.ShellQuote(it.Factory.State.Workflow.ActiveRun)
	for _, pr := range r.PRs {
		command += " " + factory.ShellQuote(pr)
	}
	if err := m.Engine.KillStages(it.Factory.Repo, it.Factory.Slug, "ci-wait"); err != nil {
		return StageReply{}, err
	}
	pane, err := m.TM.NewWindow(tmux.Window{Session: m.Session, Name: "c-" + it.Factory.Slug, Dir: it.Factory.Worktree, Command: command})
	if err != nil {
		return StageReply{}, err
	}
	for _, o := range [][2]string{{"@factory_ui", "ci-wait"}, {"@factory_repo", it.Factory.Repo}, {"@factory_stage_of", it.Factory.Slug}, {"@factory_stage", "ci-wait"}} {
		if err := m.TM.SetPaneOption(pane, o[0], o[1]); err != nil {
			return StageReply{}, err
		}
	}
	return StageReply{Result: filepath.Join(it.Factory.Dir, "inbox")}, nil
}

func (m *Runtime) CiScript() (string, error) {
	data, err := fs.ReadFile(bundle.Files, "skills/factory/scripts/wait-for-review.sh")
	if err != nil {
		return "", err
	}
	path := filepath.Join(m.Config.StateDir, "bin", "wait-for-review.sh")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	return path, os.WriteFile(path, data, 0o755)
}

func (m *Runtime) stageItem(r StageRequest) (Item, bool) {
	if r.Factory != "" {
		return m.snap.Named(r.Factory)
	}
	return m.ItemForStage(r.Token, r.Cwd)
}

func (m *Runtime) ItemForStage(token, cwd string) (Item, bool) {
	for _, it := range m.snap.Items {
		if token != "" && it.Pane != nil && it.Pane.Token == token {
			return it, true
		}
	}
	for _, it := range m.snap.Items {
		if it.Factory.Worktree != "" && Within(cwd, it.Factory.Worktree) {
			return it, true
		}
	}
	return Item{}, false
}

func (m *Runtime) StartStage(it Item, runs *state.StageRuns, r StageRequest) (StageReply, error) {
	setting := catalog.Local(m.Config.StateDir).WithDefault(it.Factory.Agent(r.Role))
	if err := Runnable(it.Factory.Repo, r.Role, setting); err != nil {
		return StageReply{}, err
	}
	profile, browses := m.browsesForQA(it, r.Role)
	if browses {
		if err := m.checkBrowser(profile, it.Factory.Worktree); err != nil {
			return StageReply{}, err
		}
	}
	run := m.NewRun(it, runs, r.Role, setting, uuid.NewString())
	task := r.Text
	if browses {
		task += "\n\n" + evidenceNote(EvidenceDir(it.Factory, run))
	}
	prompt, err := StagePrompt(r.Role, task, run.Result, m.Rules(), setting)
	if err != nil {
		return StageReply{}, err
	}
	if err := WriteFile(run.Prompt, prompt); err != nil {
		return StageReply{}, err
	}
	if _, err := m.Record(it, runs, run, "recorded intent for"); err != nil {
		return StageReply{}, err
	}
	if err := m.Engine.KillStages(it.Factory.Repo, it.Factory.Slug, r.Role); err != nil {
		return StageReply{}, err
	}
	if browses {
		m.prepareBrowser(it, run, profile)
		return m.Record(it, runs, run, "started")
	}
	launch := m.StageLaunch(it, run, "")
	if err := m.OpenStagePane(it, run, launch.Command()); err != nil {
		return StageReply{}, err
	}
	return m.Record(it, runs, run, "started")
}

func (m *Runtime) SendStage(it Item, runs *state.StageRuns, r StageRequest) (StageReply, error) {
	latest, ok := runs.Latest(r.Role)
	if !ok {
		return StageReply{}, fmt.Errorf("no %s stage has run in %s; start one with factory stage start", r.Role, it.Factory.Label())
	}
	if _, browses := m.browsesForQA(it, r.Role); browses && m.leases[it.Factory.Key()].Token != latest.Token {
		return StageReply{}, errBrowserGone
	}
	run := m.NewRun(it, runs, r.Role, latest.Setting, latest.Token)
	run.Prompt = strings.TrimSuffix(run.Prompt, ".prompt.md") + ".message.md"
	if err := WriteFile(run.Prompt, r.Text+"\n\n## Your result\n\n"+ResultContract(run.Result)); err != nil {
		return StageReply{}, err
	}
	if _, err := m.Record(it, runs, run, "recorded intent for"); err != nil {
		return StageReply{}, err
	}
	kickoff := KickoffFor(run)
	if pane, alive := LiveStagePane(stagePaneOf(m.snap.Panes, it.Factory, r.Role, run.Token)); alive {
		m.QueueType(QueuedType{Pane: pane.ID, Token: run.Token, Label: r.Role, Text: kickoff})
		return m.Record(it, runs, run, "sent a follow-up to")
	}
	sessionID := SessionOfToken(run.Token, m.snap.Hooks)
	if sessionID == "" {
		return StageReply{}, fmt.Errorf("the %s pane is gone and its session is unknown; start a fresh one with factory stage start", r.Role)
	}
	if err := m.Engine.KillStages(it.Factory.Repo, it.Factory.Slug, r.Role); err != nil {
		return StageReply{}, err
	}
	launch := m.StageLaunch(it, run, sessionID)
	if err := m.OpenStagePane(it, run, launch.Command()); err != nil {
		return StageReply{}, err
	}
	if launch.TypesKickoffAfterStart() {
		pane, _ := stagePaneOf(m.currentPanes(), it.Factory, r.Role, run.Token)
		m.QueueType(QueuedType{Pane: pane.ID, Token: run.Token, Label: r.Role, Text: kickoff, NotBefore: m.Now().Add(resumeSettle)})
	}
	return m.Record(it, runs, run, "resumed")
}

func (m *Runtime) WritableDirs(repo, checkout string, setting state.AgentSetting) []string {
	return WritableDirsFor(m.Config, repo, checkout, setting)
}

func WritableDirsFor(config *state.Config, repo, checkout string, setting state.AgentSetting) []string {
	if setting.ToolName() != state.ToolCodex {
		return nil
	}
	profile, _ := state.ReadProfile(repo)
	return factory.CodexWritableDirs(cmp.Or(checkout, repo), config.WithProfile(profile).Codex.WritableDirs)
}

func (m *Runtime) CloseFinishedStages() {
	if m.Engine == nil {
		return
	}
	for _, it := range m.snap.Items {
		if stagesEnded(it) && HasStagePanes(m.snap.Panes, it.Factory) {
			m.Engine.KillStages(it.Factory.Repo, it.Factory.Slug, "")
		}
	}
}

func (m *Runtime) runningStages(it Item) []string {
	if it.Stage == nil {
		return nil
	}
	return []string{"its " + it.Stage.Run.Role + " stage is still running; checkpoint between stages"}
}

func HasStagePanes(panes []tmux.Pane, f factory.Factory) bool {
	return slices.ContainsFunc(panes, func(p tmux.Pane) bool { return p.Repo == f.Repo && p.StageOf == f.Slug })
}

func Runnable(repo, role string, setting state.AgentSetting) error {
	switch {
	case role == "qa" && !setting.OnClaude():
		return errors.New("QA drives Chrome and runs only on claude")
	case setting.ToolName() == state.ToolOpenCode && setting.Model == "":
		return fmt.Errorf("%s on opencode needs a model; pick one of the free models", role)
	case setting.ToolName() == state.ToolCodex && !catalog.CodexTrusts(state.CodexHome(), repo):
		return fmt.Errorf("Codex would stop at a trust prompt: run codex once in %s, choose Trust and continue, and start the stage again", repo)
	}
	return nil
}

func (m *Runtime) NewRun(it Item, runs *state.StageRuns, role string, setting state.AgentSetting, token string) state.StageRun {
	active := it.Factory.State.Workflow.Runs[len(it.Factory.State.Workflow.Runs)-1]
	return state.StageRun{ID: active.ID, Attempt: active.Retries, Role: role, Round: active.Round, Setting: setting, Token: token, Prompt: filepath.Join(it.Factory.Dir, active.Prompt), Result: filepath.Join(it.Factory.Dir, active.Result), StartedAt: m.Now()}

}

func (m *Runtime) Record(it Item, runs *state.StageRuns, run state.StageRun, verb string) (StageReply, error) {
	if !slices.ContainsFunc(runs.Runs, func(old state.StageRun) bool { return old.ID == run.ID && old.Attempt == run.Attempt }) {
		runs.Runs = append(runs.Runs, run)
	}
	if err := runs.Save(); err != nil {
		return StageReply{}, err
	}
	if m.Loader != nil {
		m.Loader.Note(it.Factory, fmt.Sprintf("%s the %s stage on %s", verb, run.Role, run.Setting.ToolName()))
	}
	return StageReply{Result: run.Result}, nil
}

func (m *Runtime) StageLaunch(it Item, run state.StageRun, sessionID string) factory.StageLaunch {
	launch := factory.StageLaunch{Setting: run.Setting, Kickoff: KickoffFor(run), SessionID: sessionID}
	cfg, _ := m.repoConfig(it.Factory.Repo)
	if run.Role == "qa" {
		launch.MCPConfig = m.leases[it.Factory.Key()].MCPConfig
	}
	if run.Setting.ToolName() == state.ToolCodex {
		launch.AddDirs = factory.CodexWritableDirs(it.Factory.Worktree, cfg.Codex.WritableDirs)
	}
	return launch
}

func (m *Runtime) OpenStagePane(it Item, run state.StageRun, command string) error {
	pane, err := m.Engine.NewStagePane(StagePane{Repo: it.Factory.Repo, Slug: it.Factory.Slug, Role: run.Role, Token: run.Token, Dir: it.Factory.Worktree})
	if err != nil {
		return err
	}
	return m.TypeLaunch(pane, state.LaunchScriptPath(m.Config.StateDir, it.Factory.Repo, it.Factory.Slug, run.Role), command)
}

func (m *Runtime) TypeLaunch(pane, path, command string) error {
	if err := WriteFile(path, command+"\n"); err != nil {
		return err
	}
	return m.Engine.Type(pane, ". "+factory.ShellQuote(path))
}

func (m *Runtime) Rules() string {
	data, err := os.ReadFile(m.Config.RulesPath())
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func LiveStagePane(pane tmux.Pane, found bool) (tmux.Pane, bool) {
	return pane, found && !pane.Dead && !factory.IsShell(pane.Command)
}

func (m *Runtime) QueueType(q QueuedType) {
	if pane, ok := byID(m.currentPanes(), q.Pane); ok && q.Token == "" {
		q.Token = pane.Token
	}
	if q.QueuedAt.IsZero() {
		q.QueuedAt = m.Now()
	}
	m.typeQueue = append(m.typeQueue, q)
	if m.saveQueue(m.typeQueue) {
		m.FlushTypeQueue()
	}
}

func (m *Runtime) FlushTypeQueue() {
	if len(m.typeQueue) == 0 {
		return
	}
	var waiting []QueuedType
	for i, q := range m.typeQueue {
		if !q.TypingAt.IsZero() {
			m.settleInterrupted(q)
			continue
		}
		if q.Answers != nil && !m.stillQueued(*q.Answers) {
			continue
		}
		if q.Answers != nil {
			if reason := m.unwanted(*q.Answers); reason != "" {
				m.dropAnswer(*q.Answers, reason)
				continue
			}
		}
		if m.Now().Before(q.NotBefore) {
			waiting = append(waiting, q)
			continue
		}
		pane, found := byID(m.snap.Panes, q.Pane)
		if !found || pane.Dead || pane.Token != q.Token {
			m.notice = "The " + q.Label + " pane closed before its message was typed."
			if q.Answers != nil {
				m.dropAnswer(*q.Answers, m.notice)
			}
			continue
		}
		hook := m.HookOf(q)
		if m.AwaitingSubmit(q.Pane, hook) {
			waiting = append(waiting, q)
			continue
		}
		target := Target{Label: q.Label, Pane: &pane, Hook: hook}
		if q.Answers != nil && Refusal(target) == "" {
			target = m.refreshed(target)
		}
		if reason := Refusal(target); reason != "" {
			if q.Answers != nil && m.waitedTooLong(q) {
				m.giveUpAnswer(*q.Answers, q.Label, reason)
				continue
			}
			waiting = append(waiting, q)
			continue
		}
		typing := q
		typing.TypingAt = m.Now()
		if !m.saveQueue(slices.Concat(waiting, []QueuedType{typing}, m.typeQueue[i+1:])) {
			waiting = append(waiting, q)
			continue
		}
		reason, err := Deliver(m.Typist, target, q.Text)
		if err != nil {
			m.notice = err.Error()
			if q.Answers != nil {
				m.dropAnswer(*q.Answers, err.Error())
			}
			continue
		}
		if reason != "" {
			waiting = append(waiting, q)
			continue
		}
		m.typedAt[q.Pane] = m.Now()
		if q.Answers != nil {
			m.RecordSent(*q.Answers, typing.TypingAt)
		}
		if q.Delivers != "" {
			MarkDelivered(q.Record, q.Delivers)
		}
	}
	m.typeQueue = waiting
	m.saveQueue(waiting)
}

func (m *Runtime) waitedTooLong(q QueuedType) bool {
	waitingSince := q.QueuedAt
	if q.NotBefore.After(waitingSince) {
		waitingSince = q.NotBefore
	}
	return !waitingSince.IsZero() && m.Now().Sub(waitingSince) > AnswerWait
}

func (m *Runtime) giveUpAnswer(a Answer, label, refusal string) {
	m.dropAnswer(a, fmt.Sprintf("%s did not come back to its prompt within %d minutes. %s Send it again once it waits at its prompt, or answer in the session.", label, int(AnswerWait.Minutes()), refusal))
	if it, ok := m.snap.Find(a.Factory); ok && m.Loader != nil {
		m.Loader.Note(it.Factory, "answer to "+a.ItemID+" was not sent: the supervisor never came back to its prompt")
	}
}

func (m *Runtime) settleInterrupted(q QueuedType) {
	if q.Answers != nil {
		m.RecordSent(*q.Answers, q.TypingAt)
		return
	}
	if q.Delivers != "" {
		MarkDelivered(q.Record, q.Delivers)
	}
	hook := m.HookOf(q)
	if hook != nil && hook.Submit != nil && !hook.Submit.At.Before(q.TypingAt) {
		return
	}
	m.notice = "Typing into the " + q.Label + " pane was interrupted, so it was not typed again. Check that session and send the message yourself if it is missing."
	if it, ok := m.ItemForStage(q.Token, ""); ok && m.Loader != nil {
		m.Loader.Note(it.Factory, "a message to "+q.Label+" was interrupted while typing and was not typed again")
	}
}

func (m *Runtime) AwaitingSubmit(pane string, hook *state.HookFile) bool {
	typed, ok := m.typedAt[pane]
	if !ok || m.Now().Sub(typed) > submitWait {
		return false
	}
	return hook == nil || hook.Submit == nil || hook.Submit.At.Before(typed)
}

func (m *Runtime) HookOf(q QueuedType) *state.HookFile {
	if q.Answers == nil {
		return QueuedHook(q, m.snap.Hooks)
	}
	if it, ok := m.snap.Find(q.Answers.Factory); ok {
		return it.Hook
	}
	return nil
}

func QueuedHook(q QueuedType, hooks []*state.HookFile) *state.HookFile {
	if h := PaneHook(q.Token, hooks); h != nil {
		return h
	}
	for _, h := range hooks {
		if q.SessionID != "" && h.SessionID == q.SessionID {
			return h
		}
	}
	return nil
}

func KickoffFor(run state.StageRun) string {
	return "Read " + run.Prompt + " and follow it."
}

const findingContract = `Each entry in "findings" is {"id":"<stable id, reused when you report the same finding again>","stage":"<the stage this run belongs to>","severity":"minor|major|blocker|regression|design","layer":"<owning stack branch>","disposition":"open|closed","description":"<the defect and its mechanism>","evidence":"<file, command or repro>"}. Any other severity or disposition rejects the whole result. major, blocker, regression and design gate the loop; minor is listed and never loops. Use design only for a finding that should send the work back to the designer. Close a finding only once it is fixed and verified.`

func ResultContract(path string) string {
	return "End this turn with your factory-result block as the instructions above say. As your very last action of this turn, also write the same JSON object, and nothing else, to " + path + ". The supervisor waits for that file.\n\n" + findingContract
}

const elsewhereNote = "These instructions were written for Claude Code. Skip any skill or tool you do not have; for HTML without the factory-artifact skill, write one self-contained page with its colours as tokens on :root, a dark-mode version under prefers-color-scheme, and no external assets beyond a pinned CDN script. Never publish anything: the factory app serves the files you write."

const codexSandboxNote = "Your commands run in Codex's sandbox, where Chrome cannot start: every launch crashes and puts a crash dialog in front of the developer. Never run anything that starts a browser, such as Playwright tests or Go tests that launch Chrome. Run the other builds, linters and tests, and name the browser tests you left out in your result: QA and CI run them."

func StagePrompt(role, task, result, rules string, setting state.AgentSetting) (string, error) {
	data, err := fs.ReadFile(bundle.Files, "agents/factory-"+role+".md")
	if err != nil {
		return "", err
	}
	front, body := SplitFrontmatter(data)
	var meta struct {
		Skills []string `yaml:"skills"`
	}
	if err := yaml.Unmarshal(front, &meta); err != nil {
		return "", err
	}
	var b strings.Builder
	if rules != "" {
		b.WriteString("## The developer's rules\n\nThese rules apply to every line of code you write or review.\n\n" + rules + "\n\n")
	}
	b.WriteString(strings.TrimSpace(string(body)) + "\n")
	if !setting.OnClaude() {
		b.WriteString("\n" + elsewhereNote + "\n")
	}
	if setting.ToolName() == state.ToolCodex {
		b.WriteString("\n" + codexSandboxNote + "\n")
	}
	if len(meta.Skills) > 0 {
		b.WriteString("\nLoad these skills before you start: " + strings.Join(meta.Skills, ", ") + ".\n")
	}
	b.WriteString("\n## Your task\n\n" + strings.TrimSpace(task) + "\n\n## Your result\n\n" + ResultContract(result) + "\n")
	return b.String(), nil
}

func SplitFrontmatter(data []byte) ([]byte, []byte) {
	rest, ok := bytes.CutPrefix(data, []byte("---\n"))
	if !ok {
		return nil, data
	}
	front, body, ok := bytes.Cut(rest, []byte("\n---\n"))
	if !ok {
		return nil, data
	}
	return front, body
}

func WriteFile(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0o644)
}

func SessionOfToken(token string, hooks []*state.HookFile) string {
	if h := PaneHook(token, hooks); h != nil {
		return h.SessionID
	}
	return ""
}
