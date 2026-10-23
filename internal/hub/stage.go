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

const resumeSettle = 5 * time.Second

type StageRequest struct {
	Action string `json:"action"`
	Token  string `json:"token"`
	Cwd    string `json:"cwd"`
	Role   string `json:"role"`
	Text   string `json:"text"`
}

type StageReply struct {
	Result string `json:"result"`
}

type queuedType struct {
	Pane      string
	Token     string
	Label     string
	Text      string
	NotBefore time.Time
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
	prompt, err := stagePrompt(r.Role, r.Text, run.Result, m.rules())
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
	return m.Engine.Type(pane, command)
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
		target := Target{Label: q.Label, Pane: &pane, Hook: paneHook(q.Token, m.snap.Hooks)}
		reason, err := Deliver(m.Typist, target, q.Text)
		if err != nil {
			m.notice = err.Error()
			continue
		}
		if reason != "" {
			waiting = append(waiting, q)
		}
	}
	m.typeQueue = waiting
}

func kickoffFor(run state.StageRun) string {
	return "Read " + run.Prompt + " and follow it."
}

func resultContract(path string) string {
	return "End this turn with your factory-result block as the instructions above say. As your very last action of this turn, also write the same JSON object, and nothing else, to " + path + ". The supervisor waits for that file."
}

func stagePrompt(role, task, result, rules string) (string, error) {
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
