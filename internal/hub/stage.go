package hub

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/goccy/go-yaml"
	"github.com/google/uuid"

	"github.com/afonsodelgado/factory/bundle"
	"github.com/afonsodelgado/factory/internal/factory"
	"github.com/afonsodelgado/factory/internal/state"
	"github.com/afonsodelgado/factory/internal/tmux"
)

const (
	resumeSettle = 5 * time.Second
	submitWait   = time.Minute
)

type StageRequest struct {
	Action string   `json:"action"`
	Token  string   `json:"token"`
	Cwd    string   `json:"cwd"`
	Role   string   `json:"role"`
	Text   string   `json:"text"`
	PRs    []string `json:"prs,omitempty"`
}

type StageReply struct {
	Result string `json:"result"`
}

type queuedType struct {
	Pane      string
	Token     string
	SessionID string
	Label     string
	Text      string
	NotBefore time.Time
	Delivers  string
	Record    string
}

type StageView struct {
	Run  state.StageRun
	Pane *tmux.Pane
	Hook *state.HookFile
}

func (m *Model) stageRequest(e Event) tea.Cmd {
	var r StageRequest
	var reply StageReply
	err := json.Unmarshal(e.Data, &r)
	if err == nil {
		reply, err = m.stage(r)
	}
	if err != nil {
		e.From.Send(Message{T: "error", Error: err.Error()})
		return m.load()
	}
	data, _ := json.Marshal(reply)
	e.From.Send(Message{T: "done", Data: data})
	return m.load()
}

func (m *Model) stage(r StageRequest) (StageReply, error) {
	if r.Action == "ci" {
		return m.waitForCI(r)
	}
	if !slices.Contains(state.AgentRoles, r.Role) {
		return StageReply{}, fmt.Errorf("%q is not a role; roles are %s", r.Role, strings.Join(state.AgentRoles, ", "))
	}
	it, ok := m.itemForStage(r.Token, r.Cwd)
	if !ok {
		return StageReply{}, errors.New("no factory owns this session; run factory stage from a factory's supervisor pane")
	}
	if it.Factory.Dir == "" || it.Factory.Worktree == "" {
		return StageReply{}, fmt.Errorf("%s has no factory directory or worktree yet; write state.json first", it.Factory.Label())
	}
	runs, err := state.LoadStageRuns(state.StageRunsPath(m.Config.StateDir, it.Factory.Repo, it.Factory.Slug))
	if err != nil {
		return StageReply{}, err
	}
	if r.Action == "send" {
		return m.sendStage(it, runs, r)
	}
	return m.startStage(it, runs, r)
}

func (m *Model) waitForCI(r StageRequest) (StageReply, error) {
	it, ok := m.itemForStage(r.Token, r.Cwd)
	if !ok || it.Factory.Dir == "" {
		return StageReply{}, errors.New("no factory owns this session; run factory stage ci from a factory's supervisor pane")
	}
	if len(r.PRs) == 0 {
		return StageReply{}, errors.New("name the pull requests to wait for")
	}
	script, err := m.ciScript()
	if err != nil {
		return StageReply{}, err
	}
	command := factory.ShellQuote(script) + " --dir " + factory.ShellQuote(it.Factory.Dir) + " --inbox"
	for _, pr := range r.PRs {
		command += " " + factory.ShellQuote(pr)
	}
	if err := m.Engine.KillStages(it.Factory.Repo, it.Factory.Slug, "ci"); err != nil {
		return StageReply{}, err
	}
	pane, err := m.Engine.TM.NewWindow(tmux.Window{Session: m.Engine.Session, Name: "c-" + it.Factory.Slug, Dir: it.Factory.Worktree, Command: command})
	if err != nil {
		return StageReply{}, err
	}
	for _, o := range [][2]string{{"@factory_ui", "ci"}, {"@factory_repo", it.Factory.Repo}, {"@factory_stage_of", it.Factory.Slug}, {"@factory_stage", "ci"}} {
		if err := m.Engine.TM.SetPaneOption(pane, o[0], o[1]); err != nil {
			return StageReply{}, err
		}
	}
	return StageReply{Result: filepath.Join(it.Factory.Dir, "inbox")}, nil
}

func (m *Model) ciScript() (string, error) {
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

func (m *Model) itemForStage(token, cwd string) (Item, bool) {
	for _, it := range m.snap.Items {
		if token != "" && it.Pane != nil && it.Pane.Token == token {
			return it, true
		}
	}
	for _, it := range m.snap.Items {
		if it.Factory.Worktree != "" && within(cwd, it.Factory.Worktree) {
			return it, true
		}
	}
	return Item{}, false
}

func (m *Model) stageSetting(it Item, role string) (state.AgentSetting, error) {
	if it.Factory.State != nil {
		if setting, ok := it.Factory.State.Agents[role]; ok {
			return setting, nil
		}
	}
	agents, err := m.Config.AgentsFor(it.Factory.Repo)
	return agents[role], err
}

func (m *Model) startStage(it Item, runs *state.StageRuns, r StageRequest) (StageReply, error) {
	setting, err := m.stageSetting(it, r.Role)
	if err != nil {
		return StageReply{}, err
	}
	if err := runnable(it.Factory.Repo, r.Role, setting); err != nil {
		return StageReply{}, err
	}
	run := m.newRun(it, runs, r.Role, setting, uuid.NewString())
	prompt, err := stagePrompt(r.Role, r.Text, run.Result, m.rules(), setting.OnClaude())
	if err != nil {
		return StageReply{}, err
	}
	if err := writeFile(run.Prompt, prompt); err != nil {
		return StageReply{}, err
	}
	if err := m.Engine.KillStages(it.Factory.Repo, it.Factory.Slug, r.Role); err != nil {
		return StageReply{}, err
	}
	launch := m.stageLaunch(it, run, "")
	if err := m.openStagePane(it, run, launch.Command()); err != nil {
		return StageReply{}, err
	}
	return m.record(it, runs, run, "started")
}

func (m *Model) sendStage(it Item, runs *state.StageRuns, r StageRequest) (StageReply, error) {
	latest, ok := runs.Latest(r.Role)
	if !ok {
		return StageReply{}, fmt.Errorf("no %s stage has run in %s; start one with factory stage start", r.Role, it.Factory.Label())
	}
	run := m.newRun(it, runs, r.Role, latest.Setting, latest.Token)
	run.Prompt = strings.TrimSuffix(run.Prompt, ".prompt.md") + ".message.md"
	if err := writeFile(run.Prompt, r.Text+"\n\n## Your result\n\n"+resultContract(run.Result)); err != nil {
		return StageReply{}, err
	}
	kickoff := kickoffFor(run)
	if pane, alive := livePane(stagePaneOf(m.snap.Panes, it.Factory, r.Role, run.Token)); alive {
		m.queueType(queuedType{Pane: pane.ID, Token: run.Token, Label: r.Role, Text: kickoff})
		return m.record(it, runs, run, "sent a follow-up to")
	}
	sessionID := sessionOfToken(run.Token, m.snap.Hooks)
	if sessionID == "" {
		return StageReply{}, fmt.Errorf("the %s pane is gone and its session is unknown; start a fresh one with factory stage start", r.Role)
	}
	if err := m.Engine.KillStages(it.Factory.Repo, it.Factory.Slug, r.Role); err != nil {
		return StageReply{}, err
	}
	launch := m.stageLaunch(it, run, sessionID)
	if err := m.openStagePane(it, run, launch.Command()); err != nil {
		return StageReply{}, err
	}
	if launch.TypesKickoffAfterStart() {
		pane, _ := stagePaneOf(m.currentPanes(), it.Factory, r.Role, run.Token)
		m.queueType(queuedType{Pane: pane.ID, Token: run.Token, Label: r.Role, Text: kickoff, NotBefore: m.Now().Add(resumeSettle)})
	}
	return m.record(it, runs, run, "resumed")
}

func (m *Model) writableDirs(dir string, setting state.AgentSetting) []string {
	if setting.ToolName() != state.ToolCodex {
		return nil
	}
	return codexWritableDirs(dir, m.Config.Codex.WritableDirs)
}

func supervisorEnv(supervisor state.AgentSetting) []string {
	env := []string{"FACTORY=1", "FACTORY_SUPERVISOR=" + supervisor.ToolName(), factoryBin()}
	if !supervisor.OnClaude() {
		env = append(env, "CLAUDE_CODE_SESSION_ID=")
	}
	return env
}

func factoryBin() string {
	self, err := os.Executable()
	if err != nil {
		return "FACTORY_BIN=factory"
	}
	return "FACTORY_BIN=" + self
}

func (m *Model) typeAfterStart(plan Wake, pane string, it Item) {
	if plan.ThenType == "" {
		return
	}
	m.queueType(queuedType{Pane: pane, SessionID: plan.Launch.SessionID, Label: it.Factory.Label(), Text: plan.ThenType, NotBefore: m.Now().Add(resumeSettle)})
}

func (m *Model) closeFinishedStages() {
	if m.Engine == nil {
		return
	}
	for _, it := range m.snap.Items {
		finished := it.Status == factory.Done || it.Status == factory.Gone || it.Factory.Archived()
		if finished && hasStagePanes(m.snap.Panes, it.Factory) {
			m.Engine.KillStages(it.Factory.Repo, it.Factory.Slug, "")
		}
	}
}

func hasStagePanes(panes []tmux.Pane, f factory.Factory) bool {
	return slices.ContainsFunc(panes, func(p tmux.Pane) bool { return p.Repo == f.Repo && p.StageOf == f.Slug })
}

func runnable(repo, role string, setting state.AgentSetting) error {
	switch {
	case role == "qa" && !setting.OnClaude():
		return errors.New("QA drives Chrome and runs only on claude")
	case setting.ToolName() == state.ToolOpenCode && setting.Model == "":
		return fmt.Errorf("%s on opencode needs a model; pick one of the free models", role)
	case setting.ToolName() == state.ToolCodex && !CodexTrusts(state.CodexHome(), repo):
		return fmt.Errorf("Codex would stop at a trust prompt: run codex once in %s, choose Trust and continue, then Trust all for the hooks, and start the stage again", repo)
	}
	return nil
}

func (m *Model) newRun(it Item, runs *state.StageRuns, role string, setting state.AgentSetting, token string) state.StageRun {
	round := runs.NextRound(role)
	base := filepath.Join(it.Factory.Dir, "stages", fmt.Sprintf("%s-%d", role, round))
	return state.StageRun{Role: role, Round: round, Setting: setting, Token: token, Prompt: base + ".prompt.md", Result: base + ".result.json", StartedAt: m.Now()}
}

func (m *Model) record(it Item, runs *state.StageRuns, run state.StageRun, verb string) (StageReply, error) {
	runs.Runs = append(runs.Runs, run)
	if err := runs.Save(); err != nil {
		return StageReply{}, err
	}
	if m.Loader != nil {
		m.Loader.Note(it.Factory, fmt.Sprintf("%s the %s stage on %s", verb, run.Role, run.Setting.ToolName()))
	}
	return StageReply{Result: run.Result}, nil
}

func (m *Model) stageLaunch(it Item, run state.StageRun, sessionID string) factory.StageLaunch {
	launch := factory.StageLaunch{Setting: run.Setting, Kickoff: kickoffFor(run), SessionID: sessionID}
	if run.Role == "qa" {
		profile, _ := state.ReadProfile(it.Factory.Repo)
		launch.Chrome = profile.BrowserQA()
	}
	if run.Setting.ToolName() == state.ToolCodex {
		launch.AddDirs = codexWritableDirs(it.Factory.Worktree, m.Config.Codex.WritableDirs)
	}
	return launch
}

func (m *Model) openStagePane(it Item, run state.StageRun, command string) error {
	pane, err := m.Engine.NewStagePane(m.Layout, StagePane{Repo: it.Factory.Repo, Slug: it.Factory.Slug, Role: run.Role, Token: run.Token, Dir: it.Factory.Worktree})
	if err != nil {
		return err
	}
	return m.typeLaunch(pane, state.FileStem(it.Factory.Repo, it.Factory.Slug)+"-"+run.Role, command)
}

func (m *Model) typeSupervisor(pane string, f factory.Factory, command string) error {
	if f.Supervisor().OnClaude() {
		return m.Engine.Type(pane, command)
	}
	return m.typeLaunch(pane, state.FileStem(f.Repo, f.Slug)+"-supervisor", command)
}

func (m *Model) typeLaunch(pane, name, command string) error {
	path := filepath.Join(m.Config.StateDir, "launch", name+".sh")
	if err := writeFile(path, command+"\n"); err != nil {
		return err
	}
	return m.Engine.Type(pane, ". "+factory.ShellQuote(path))
}

func (m *Model) rules() string {
	data, err := os.ReadFile(m.Config.RulesPath())
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func (m *Model) currentPanes() []tmux.Pane {
	panes, _ := m.Engine.TM.Panes()
	return panes
}

func livePane(pane tmux.Pane, found bool) (tmux.Pane, bool) {
	return pane, found && !pane.Dead && !factory.IsShell(pane.Command)
}

func (m *Model) queueType(q queuedType) {
	m.typeQueue = append(m.typeQueue, q)
	m.flushTypeQueue()
}

func (m *Model) flushTypeQueue() {
	var waiting []queuedType
	for _, q := range m.typeQueue {
		if m.Now().Before(q.NotBefore) {
			waiting = append(waiting, q)
			continue
		}
		pane, found := byID(m.snap.Panes, q.Pane)
		if !found || pane.Dead {
			m.notice = "The " + q.Label + " pane closed before its message was typed."
			continue
		}
		hook := queuedHook(q, m.snap.Hooks)
		if m.awaitingSubmit(q.Pane, hook) {
			waiting = append(waiting, q)
			continue
		}
		reason, err := Deliver(m.Typist, Target{Label: q.Label, Pane: &pane, Hook: hook}, q.Text)
		if err != nil {
			m.notice = err.Error()
			continue
		}
		if reason != "" {
			waiting = append(waiting, q)
			continue
		}
		m.typedAt[q.Pane] = m.Now()
		if q.Delivers != "" {
			markDelivered(q.Record, q.Delivers)
		}
	}
	m.typeQueue = waiting
}

func (m *Model) awaitingSubmit(pane string, hook *state.HookFile) bool {
	typed, ok := m.typedAt[pane]
	if !ok || m.Now().Sub(typed) > submitWait {
		return false
	}
	return hook == nil || hook.Submit == nil || hook.Submit.At.Before(typed)
}

func queuedHook(q queuedType, hooks []*state.HookFile) *state.HookFile {
	if h := paneHook(q.Token, hooks); h != nil {
		return h
	}
	for _, h := range hooks {
		if q.SessionID != "" && h.SessionID == q.SessionID {
			return h
		}
	}
	return nil
}

func kickoffFor(run state.StageRun) string {
	return "Read " + run.Prompt + " and follow it."
}

func resultContract(path string) string {
	return "End this turn with your factory-result block as the instructions above say. As your very last action of this turn, also write the same JSON object, and nothing else, to " + path + ". The supervisor waits for that file."
}

const elsewhereNote = "These instructions were written for Claude Code. Skip any skill or tool you do not have, such as artifact-design or artifact-diagramming; for HTML, write one self-contained page with its colours as tokens on :root, a dark-mode version under prefers-color-scheme, and no external assets beyond a pinned CDN script. Never publish anything: the supervisor shows the files you write."

func stagePrompt(role, task, result, rules string, onClaude bool) (string, error) {
	data, err := fs.ReadFile(bundle.Files, "agents/factory-"+role+".md")
	if err != nil {
		return "", err
	}
	front, body := splitFrontmatter(data)
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
	if !onClaude {
		b.WriteString("\n" + elsewhereNote + "\n")
	}
	if len(meta.Skills) > 0 {
		b.WriteString("\nLoad these skills before you start: " + strings.Join(meta.Skills, ", ") + ".\n")
	}
	b.WriteString("\n## Your task\n\n" + strings.TrimSpace(task) + "\n\n## Your result\n\n" + resultContract(result) + "\n")
	return b.String(), nil
}

func splitFrontmatter(data []byte) ([]byte, []byte) {
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

func writeFile(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0o644)
}

func sessionOfToken(token string, hooks []*state.HookFile) string {
	if h := paneHook(token, hooks); h != nil {
		return h.SessionID
	}
	return ""
}

func CodexTrusts(home, repo string) bool {
	data, err := os.ReadFile(filepath.Join(home, "config.toml"))
	if err != nil {
		return false
	}
	trustedRepo := regexp.MustCompile(`\[projects\."` + regexp.QuoteMeta(repo) + `"\]\s*\n\s*trust_level\s*=\s*"trusted"`)
	trustedHooks := strings.Contains(string(data), filepath.Join(home, "hooks.json")+":stop:")
	return trustedHooks && trustedRepo.Match(data)
}

func codexWritableDirs(worktree string, extra []string) []string {
	var dirs []string
	if out, err := exec.Command("git", "-C", worktree, "rev-parse", "--path-format=absolute", "--git-common-dir").Output(); err == nil {
		dirs = append(dirs, strings.TrimSpace(string(out)))
	}
	dirs = append(dirs, toolOutput("go", "env", "GOCACHE", "GOMODCACHE")...)
	dirs = append(dirs, toolOutput("npm", "config", "get", "cache")...)
	for _, dir := range extra {
		dirs = append(dirs, state.ExpandHome(dir))
	}
	var existing []string
	for _, dir := range dirs {
		if info, err := os.Stat(dir); err == nil && info.IsDir() && !slices.Contains(existing, dir) {
			existing = append(existing, dir)
		}
	}
	return existing
}

func toolOutput(tool string, args ...string) []string {
	if _, err := exec.LookPath(tool); err != nil {
		return nil
	}
	out, err := exec.Command(tool, args...).Output()
	if err != nil {
		return nil
	}
	return strings.Fields(string(out))
}
