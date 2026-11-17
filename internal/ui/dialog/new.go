package dialog

import (
	"cmp"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"github.com/rivo/uniseg"

	"github.com/afonsodelgado/factory/internal/catalog"
	"github.com/afonsodelgado/factory/internal/factory"
	"github.com/afonsodelgado/factory/internal/state"
	"github.com/afonsodelgado/factory/internal/ui/theme"
)

type NewFactoryModel struct {
	Context        NewContext
	repo           int
	name           string
	repoPathEdited bool
	repoPath       string
	setupRetry     map[string]bool
	prompt         textEditor
	pipeline       string
	chosen         []string
	overrides      map[string]state.AgentSetting
	row            int
	step           int
	problem        string
	result         *NewResult
	width          int
	height         int
	place          int
	probes         map[string]probe
	ProbeNode      func(name string) (string, error)
}

const supervisorRole = "supervisor"

type probe struct {
	done bool
	text string
	err  error
}

type probedMsg struct {
	node string
	text string
	err  error
}

func NewFactory(c NewContext) *NewFactoryModel {
	return &NewFactoryModel{Context: c, repo: max(0, min(c.Repo, len(c.Repos)-1)), step: 1, row: firstStageRow, place: defaultPlace(c), probes: map[string]probe{}, repoPath: sharedRepoDirectory(c.Repos)}
}

func defaultPlace(c NewContext) int {
	if i := slices.IndexFunc(c.Nodes, func(n NodeChoice) bool { return n.Name == c.Target }); i >= 0 {
		return i + 1
	}
	return 0
}

const firstStageRow = 1

func (m *NewFactoryModel) rowStage() string {
	if m.row < firstStageRow {
		return ""
	}
	return state.StageKeys[m.row-firstStageRow]
}

func (m *NewFactoryModel) rowRole() string {
	if m.row < firstStageRow {
		return supervisorRole
	}
	return state.RoleOf(m.rowStage())
}

func (m *NewFactoryModel) stageAgents() map[string]state.AgentSetting {
	agents := map[string]state.AgentSetting{}
	for role, o := range m.overrides {
		if role != supervisorRole {
			agents[role] = o
		}
	}
	if len(agents) == 0 {
		return nil
	}
	return agents
}

func (m *NewFactoryModel) Init() tea.Cmd { return m.probe() }

func (m *NewFactoryModel) nodeChoice() (NodeChoice, bool) {
	if m.place == 0 || m.place > len(m.Context.Nodes) {
		return NodeChoice{}, false
	}
	return m.Context.Nodes[m.place-1], true
}

func (m *NewFactoryModel) node() string {
	choice, _ := m.nodeChoice()
	return choice.Name
}

func (m *NewFactoryModel) placeName() string {
	if choice, ok := m.nodeChoice(); ok {
		return shortName(choice.Name)
	}
	return ""
}

func (m *NewFactoryModel) probe() tea.Cmd {
	choice, ok := m.nodeChoice()
	if !ok || choice.Unreachable || m.ProbeNode == nil {
		return nil
	}
	name, check := choice.Name, m.ProbeNode
	if _, started := m.probes[name]; started {
		return nil
	}
	m.probes[name] = probe{}
	return func() tea.Msg {
		text, err := check(name)
		return probedMsg{node: name, text: text, err: err}
	}
}

func (m *NewFactoryModel) moveOn(delta int) tea.Cmd {
	places := len(m.Context.Nodes) + 1
	m.place = (m.place + delta + places) % places
	return m.probe()
}

func (m *NewFactoryModel) elsewhere() bool {
	return len(m.Context.Nodes) > 0
}

func (m *NewFactoryModel) choosesPlace() bool {
	return m.elsewhere() && !m.addingRepo()
}

func (m *NewFactoryModel) nodeProblem(choice NodeChoice) string {
	short, repo := shortName(choice.Name), m.selectedRepo()
	switch p := m.probes[choice.Name]; {
	case repo.NoGitRemote != "":
		return repo.Name + " has no git remote, so " + short + " has nothing to clone. Push it to one first (git remote add origin <url>, then git push), or pick this machine with ←."
	case choice.Unreachable:
		return m.id() + " can't start on " + short + " while it is unreachable. Pick another node with ←→; N shows what is known about " + short + "."
	case p.done && p.err != nil:
		return p.err.Error()
	}
	return ""
}

func (m *NewFactoryModel) placeProblem() string {
	if choice, ok := m.nodeChoice(); ok {
		return m.nodeProblem(choice)
	}
	return ""
}

func (m *NewFactoryModel) Result() (any, bool) {
	if m.result == nil {
		return nil, false
	}
	return *m.result, true
}

func (m *NewFactoryModel) selectedRepo() RepoOption {
	if m.addingRepo() {
		return RepoOption{}
	}
	return m.Context.Repos[m.repo]
}

func (m *NewFactoryModel) id() string {
	return factory.Slugify(m.name)
}

func (m *NewFactoryModel) named(e Existing) bool {
	id := m.id()
	return id != "" && (e.Worktree == id || strings.ToLower(e.Label) == id)
}

func (m *NewFactoryModel) existing() (Existing, bool) {
	for _, e := range m.selectedRepo().Existing {
		if !e.Retired && m.named(e) {
			return e, true
		}
	}
	for _, e := range m.Context.Elsewhere {
		elsewhereMatters := e.Remote != "" || m.placeName() != ""
		if elsewhereMatters && !e.Retired && m.named(e) {
			return e, true
		}
	}
	return Existing{}, false
}

func (m *NewFactoryModel) retiredMatch() (Existing, bool) {
	for _, e := range m.selectedRepo().Existing {
		if e.Retired && m.named(e) {
			return e, true
		}
	}
	return Existing{}, false
}

func (m *NewFactoryModel) slug() string {
	taken := func(s string) bool {
		for _, e := range m.selectedRepo().Existing {
			if e.Worktree == s {
				return true
			}
		}
		return false
	}
	return factory.FreshSlug(m.id(), taken)
}

func (m *NewFactoryModel) launch() factory.Launch {
	repo := m.selectedRepo()
	name, stages := m.chosenPipeline()
	launch := factory.Launch{Repo: repo.Path, Slug: m.slug(), Pipeline: name, Stages: stages, Agents: m.stageAgents(), Supervisor: m.agent(supervisorRole)}
	if launch.Supervisor.OnClaude() {
		launch.SessionID = "<uuid>"
	}
	return launch
}

func (m *NewFactoryModel) presetStages(name string) []string {
	for _, p := range m.selectedRepo().Pipelines {
		if p.Name == name {
			return p.Stages
		}
	}
	return nil
}

func (m *NewFactoryModel) runStages() []string {
	if m.chosen == nil {
		return m.presetStages(m.pipelineName())
	}
	return state.EffectiveStages(m.chosen, m.selectedRepo().Support())
}

func (m *NewFactoryModel) chosenPipeline() (string, []string) {
	stages := m.runStages()
	if m.chosen == nil || slices.Equal(stages, m.presetStages(m.pipelineName())) {
		return m.pipelineName(), nil
	}
	for _, p := range m.selectedRepo().Pipelines {
		if slices.Equal(stages, p.Stages) {
			return p.Name, nil
		}
	}
	return state.CustomPipeline, stages
}

func (m *NewFactoryModel) resetChoices() {
	m.pipeline, m.chosen, m.overrides, m.row = "", nil, nil, firstStageRow
}

func (m *NewFactoryModel) locked(stage string) bool {
	qaSkipped := state.DependsOnQA(stage) && !slices.Contains(m.runStages(), "qa")
	return state.AlwaysRuns(stage) || !m.selectedRepo().Support().Offers(stage) || qaSkipped
}

func (m *NewFactoryModel) toggle(stage string) {
	if m.locked(stage) {
		return
	}
	stages := slices.Clone(m.runStages())
	if i := slices.Index(stages, stage); i >= 0 {
		stages = slices.Delete(stages, i, i+1)
	} else {
		stages = append(stages, stage)
		if stage == "qa" {
			stages = append(stages, "qa_fixes", "test_gaps")
		}
	}
	m.chosen = stages
}

func (m *NewFactoryModel) defaultAgent(role string) state.AgentSetting {
	if role == supervisorRole {
		return m.selectedRepo().Supervisor
	}
	for _, a := range m.selectedRepo().Agents {
		if a.Role == role {
			return a.AgentSetting
		}
	}
	return state.DefaultAgents[role]
}

func (m *NewFactoryModel) agent(role string) state.AgentSetting {
	return m.overrides[role].OverRole(role, m.defaultAgent(role))
}

func (m *NewFactoryModel) tool(name string) ToolOption {
	for _, t := range m.Context.Tools {
		if t.Name == name {
			return t
		}
	}
	if name == state.ToolClaude {
		return ToolOption{Name: name, Models: catalog.Claude()}
	}
	return ToolOption{Name: name, Problem: name + " is not available"}
}

func (m *NewFactoryModel) pickableTools() []string {
	var tools []string
	for _, name := range state.Tools {
		if t := m.tool(name); t.Problem == "" && len(t.Models) > 0 {
			tools = append(tools, name)
		}
	}
	return tools
}

func (m *NewFactoryModel) unavailableTools() string {
	var problems []string
	for _, name := range state.Tools {
		if t := m.tool(name); t.Problem != "" {
			problems = append(problems, t.Problem)
		}
	}
	return strings.Join(problems, " ")
}

func (m *NewFactoryModel) models(tool string) []string {
	var ids []string
	for _, model := range m.tool(tool).Models {
		ids = append(ids, model.ID)
	}
	return ids
}

func (m *NewFactoryModel) efforts(setting state.AgentSetting) []string {
	for _, model := range m.tool(setting.ToolName()).Models {
		if model.ID == setting.Model {
			return model.Efforts
		}
	}
	if setting.OnClaude() {
		return state.AgentEfforts
	}
	return nil
}

func (m *NewFactoryModel) onDefaultTool(role string) bool {
	return m.agent(role).ToolName() == m.defaultAgent(role).ToolName()
}

func (m *NewFactoryModel) startProblem() string {
	if problem := m.selectedRepo().PipelineProblem; problem != "" {
		return problem
	}
	return m.agentProblem()
}

func (m *NewFactoryModel) agentProblem() string {
	for _, role := range append([]string{supervisorRole}, state.AgentRoles...) {
		setting := m.agent(role)
		tool := m.tool(setting.ToolName())
		if tool.Problem != "" {
			continue
		}
		if err := catalog.Check("this factory", role, setting, tool.Models, tool.Default); err != nil {
			return err.Error()
		}
	}
	return ""
}

func (m *NewFactoryModel) cycleTool(role string) {
	tools := m.pickableTools()
	if role == "qa" {
		m.problem = "QA drives Chrome, so it runs only on claude."
		return
	}
	m.problem = m.unavailableTools()
	if len(tools) == 0 {
		return
	}
	next := tools[(slices.Index(tools, m.agent(role).ToolName())+1)%len(tools)]
	if next == m.defaultAgent(role).ToolName() {
		m.setOverride(role, func(o *state.AgentSetting) { *o = state.AgentSetting{} })
		return
	}
	switched := m.switchedTo(role, next)
	m.setOverride(role, func(o *state.AgentSetting) { *o = switched })
}

func (m *NewFactoryModel) switchedTo(role, tool string) state.AgentSetting {
	fallback := state.DefaultAgents[role]
	if tool != state.ToolClaude {
		option := m.tool(tool)
		fallback = catalog.ToolDefault(option.Models, option.Default)
	}
	return state.AgentSetting{Tool: tool, Model: fallback.Model, Effort: fallback.Effort}
}

func (m *NewFactoryModel) cycleModel(role string) {
	models := m.models(m.agent(role).ToolName())
	if len(models) == 0 {
		return
	}
	if !m.onDefaultTool(role) {
		next := models[(slices.Index(models, m.agent(role).Model)+1)%len(models)]
		m.setOverride(role, func(o *state.AgentSetting) { o.Model, o.Effort = next, "" })
		return
	}
	m.setOverride(role, func(o *state.AgentSetting) {
		o.Model = nextOverride(models, m.agent(role).Model, m.defaultAgent(role).Model)
	})
}

func (m *NewFactoryModel) cycleEffort(role string) {
	efforts := m.efforts(m.agent(role))
	if len(efforts) == 0 {
		return
	}
	if !m.onDefaultTool(role) {
		next := efforts[(slices.Index(efforts, m.agent(role).Effort)+1)%len(efforts)]
		m.setOverride(role, func(o *state.AgentSetting) { o.Effort = next })
		return
	}
	m.setOverride(role, func(o *state.AgentSetting) {
		o.Effort = nextOverride(efforts, m.agent(role).Effort, m.defaultAgent(role).Effort)
	})
}

func (m *NewFactoryModel) setOverride(role string, change func(*state.AgentSetting)) {
	if role == "" {
		return
	}
	if m.overrides == nil {
		m.overrides = map[string]state.AgentSetting{}
	}
	o := m.overrides[role]
	change(&o)
	if o == (state.AgentSetting{}) {
		delete(m.overrides, role)
		return
	}
	m.overrides[role] = o
}

func nextOverride(values []string, current, inherited string) string {
	next := values[(slices.Index(values, current)+1)%len(values)]
	if next == inherited {
		return ""
	}
	return next
}

func (m *NewFactoryModel) pipelineName() string {
	repo := m.selectedRepo()
	for _, p := range repo.Pipelines {
		if p.Name == m.pipeline {
			return m.pipeline
		}
	}
	return repo.DefaultPipeline
}

func (m *NewFactoryModel) nextPipeline() {
	list := m.selectedRepo().Pipelines
	current, _ := m.chosenPipeline()
	m.chosen = nil
	for i, p := range list {
		if p.Name == current {
			m.pipeline = list[(i+1)%len(list)].Name
			return
		}
	}
}

func (m *NewFactoryModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case tea.PasteMsg:
		m.Context.Notice = ""
		m.typeText(DecodeKeys(msg.Content))
	case promptEdited:
		m.promptEdited(msg)
	case probedMsg:
		m.probes[msg.node] = probe{done: true, text: msg.text, err: msg.err}
	case tea.KeyPressMsg:
		m.Context.Notice = ""
		switch m.step {
		case 1:
			return m, m.nameKey(msg)
		case 2:
			return m, m.stagesKey(msg)
		}
		return m, m.promptKey(msg)
	}
	return m, nil
}

func (m *NewFactoryModel) typeText(text string) {
	if text == "" {
		return
	}
	switch m.step {
	case 1:
		if m.addingRepo() {
			if !m.repoPathEdited && (strings.HasPrefix(text, "/") || strings.HasPrefix(text, "~")) {
				m.repoPath = ""
			}
			m.repoPathEdited = true
			m.repoPath += strings.ReplaceAll(text, "\n", " ")
		} else {
			m.name += strings.ReplaceAll(text, "\n", " ")
		}
	case 3:
		m.prompt.insert(text)
	}
}

func (m *NewFactoryModel) nameKey(msg tea.KeyPressMsg) tea.Cmd {
	m.problem = ""
	switch msg.String() {
	case "esc", "ctrl+c":
		return tea.Quit
	case "up":
		m.repo = max(m.repo-1, 0)
		m.Context.Here = false
		m.resetChoices()
	case "tab":
		if m.addingRepo() {
			m.repoPathEdited = true
			m.repoPath += completeDirectory(m.repoPath).suffix
			return nil
		}
		fallthrough
	case "down":
		m.repo = (m.repo + 1) % (len(m.Context.Repos) + 1)
		m.Context.Here = false
		m.resetChoices()
	case "backspace":
		if m.addingRepo() {
			m.repoPathEdited = true
			m.repoPath = dropLast(m.repoPath)
		} else {
			m.name = dropLast(m.name)
		}
	case "ctrl+u":
		if m.addingRepo() {
			m.repoPathEdited = true
			m.repoPath = ""
		} else {
			m.name = ""
		}
	case "ctrl+e":
		if !m.addingRepo() {
			return m.prepareSettings(m.selectedRepo().Path)
		}
	case "left":
		if m.choosesPlace() {
			return m.moveOn(-1)
		}
	case "right":
		if m.choosesPlace() {
			return m.moveOn(1)
		}
	case "ctrl+w":
		if e, ok := m.existing(); ok && !e.Archived && e.Key != "" {
			m.result = &NewResult{Select: e.Key, Wake: true}
			return tea.Quit
		}
	case "enter":
		return m.next()
	default:
		m.typeText(DecodeKeys(msg.Text))
	}
	return nil
}

func (m *NewFactoryModel) next() tea.Cmd {
	if m.addingRepo() {
		return m.prepareSettings(m.repoPath)
	}
	if m.id() == "" {
		m.problem = "Type an id made of letters a–z, digits 0–9 and dashes, such as csv-export."
		return nil
	}
	if factory.IsReservedID(m.name) {
		m.problem = factory.ReservedIDRefusal(m.name)
		return nil
	}
	if e, ok := m.existing(); ok && e.Archived {
		return nil
	}
	if e, ok := m.existing(); ok {
		m.result = &NewResult{Select: e.Key}
		return tea.Quit
	}
	if problem := m.placeProblem(); problem != "" {
		return nil
	}
	m.pipeline = m.pipelineName()
	m.step = 2
	return nil
}

func (m *NewFactoryModel) promptKey(msg tea.KeyPressMsg) tea.Cmd {
	m.problem = ""
	switch msg.String() {
	case "esc":
		m.step = 2
	case "ctrl+c":
		return tea.Quit
	case "ctrl+s":
		return m.start()
	case "ctrl+g":
		return editExternally(m.prompt.text)
	default:
		m.prompt.key(msg, m.promptWidth())
	}
	return nil
}

func (m *NewFactoryModel) promptEdited(msg promptEdited) {
	if msg.err != nil {
		m.problem = fmt.Sprintf("The prompt is unchanged: %s could not edit it (%v).", externalEditor(), msg.err)
		return
	}
	m.prompt.replace(msg.text)
}

func (m *NewFactoryModel) promptWidth() int {
	return max(m.width-10, 1)
}

func (m *NewFactoryModel) stagesKey(msg tea.KeyPressMsg) tea.Cmd {
	m.problem = ""
	stage, role := m.rowStage(), m.rowRole()
	switch msg.String() {
	case "esc":
		m.step = 1
	case "ctrl+c":
		return tea.Quit
	case "enter":
		if problem := m.startProblem(); problem != "" {
			m.problem = problem
			return nil
		}
		m.step = 3
	case "up":
		m.row = max(m.row-1, 0)
	case "down":
		m.row = min(m.row+1, len(state.StageKeys))
	case "tab":
		m.nextPipeline()
	case "space":
		if stage != "" {
			m.toggle(stage)
		}
	case "t":
		m.cycleTool(role)
	case "m":
		m.cycleModel(role)
	case "e":
		m.cycleEffort(role)
	}
	return nil
}

func (m *NewFactoryModel) start() tea.Cmd {
	if strings.TrimSpace(m.prompt.text) == "" {
		m.problem = "The prompt is empty. Write at least a sentence about what this factory should do; it won't start without one."
		return nil
	}
	if problem := m.startProblem(); problem != "" {
		m.problem = problem
		return nil
	}
	name, stages := m.chosenPipeline()
	m.result = &NewResult{Repo: m.selectedRepo().Path, Slug: m.slug(), Prompt: m.prompt.text, Pipeline: name, Stages: stages, Agents: m.stageAgents(), Supervisor: m.overrides[supervisorRole], Node: m.node()}
	return tea.Quit
}

var newlineKeys = strings.NewReplacer("\x1b[106;5u", "\n", "\x1b[13;5u", "\n", "\x1b[13u", "\n", "\r\n", "\n", "\r", "\n")

var escapeSequence = regexp.MustCompile(`\x1b(?:\[[0-?]*[ -/]*[@-~]|\][^\x07\x1b]*(?:\x07|\x1b\\)?|[@-_])?`)

func DecodeKeys(text string) string {
	return escapeSequence.ReplaceAllString(newlineKeys.Replace(text), "")
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

func (m *NewFactoryModel) View() tea.View {
	c := theme.NewCanvas(m.width, m.height)
	switch m.step {
	case 1:
		m.drawName(c)
	case 2:
		m.drawStages(c)
	default:
		m.drawPrompt(c)
	}
	v := tea.NewView(c.Render())
	v.AltScreen = true
	return v
}

func (m *NewFactoryModel) drawName(c *theme.Canvas) {
	w := c.W - 2
	c.Put(1, 0, "Repo", theme.Bright.B())
	c.Right(c.W-2, 0, "1 of 3", theme.Muted)
	y := 1
	for i, r := range m.Context.Repos {
		if m.Context.Here && i != m.repo {
			continue
		}
		mark, ink := "○ ", theme.Text
		if i == m.repo {
			c.Put(1, y, "❯", theme.Focused.B())
			mark, ink = "● ", theme.Bright
		}
		qa, rabbit := "terminal QA", "CodeRabbit off"
		if r.Browser {
			qa = "browser QA"
		}
		if r.CodeRabbit {
			rabbit = "CodeRabbit on"
		}
		x := c.Put(3, y, mark+r.Name, ink)
		x = c.PutMax(max(x+2, 14), y, r.Display, theme.Muted, 28)
		c.Put(max(x+2, 44), y, qa+" · "+rabbit, theme.Quiet)
		y++
	}
	if m.addingRepo() {
		c.Put(1, y, "❯", theme.Focused.B())
		c.Put(5, y, "+ add a repo…", theme.Bright)
	} else {
		c.Put(5, y, "+ add a repo…", theme.Quiet)
	}
	y += 2
	if m.addingRepo() {
		m.drawRepoPath(c, y, w)
		return
	}
	if m.elsewhere() {
		y = m.drawPlaces(c, y, w)
	}
	c.Put(1, y, "Factory id", theme.Bright.B())
	y++
	drawInput(c, 1, y, w, "", []string{m.name + "█"}, true, theme.Focused)
	y += 3
	if m.Context.Notice != "" {
		c.PutMax(1, y, m.Context.Notice, theme.FgInk(theme.Green), w)
		y++
	}
	for _, l := range m.notice(w) {
		c.Spans(1, y, l)
		y++
	}
	_, matched := m.existing()
	problem := m.problem
	if !matched && m.id() != "" {
		problem = cmp.Or(problem, m.placeProblem())
	}
	if problem != "" {
		for _, l := range theme.Wrap("✗ "+problem, w) {
			c.Put(1, y, l, theme.FgInk(theme.Red))
			y++
		}
	}
	if !matched && problem == "" && m.id() != "" && !factory.IsReservedID(m.name) {
		y++
		m.drawPreview(c, 1, y, w)
	}
	hints := "⏎ next: pipeline   ↑↓ repo   ctrl+e settings   esc cancel"
	if m.Context.Here && len(m.Context.Repos) > 1 {
		hints = "⏎ next: pipeline   ↑↓ another repo   ctrl+e settings   esc cancel"
	}
	if m.elsewhere() {
		hints = "⏎ next: pipeline   ↑↓ repo   ←→ runs on   ctrl+e settings   esc cancel"
	}
	e, _ := m.existing()
	switch {
	case matched && e.Archived:
		hints = "ctrl+e settings   esc cancel"
	case matched:
		hints = "⏎ select it   ctrl+w select and wake it   ctrl+e settings   esc cancel"
	case problem != "" && m.placeProblem() != "":
		hints = "←→ runs on   ↑↓ repo   ctrl+e settings   esc cancel"
	}
	c.Put(1, c.H-1, hints, theme.Text)
}

func (m *NewFactoryModel) notice(w int) [][]theme.Span {
	var spans [][]theme.Span
	add := func(text string, ink theme.Ink) {
		for _, l := range theme.Wrap(text, w) {
			spans = append(spans, []theme.Span{{Text: l, Ink: ink}})
		}
	}
	if e, ok := m.existing(); ok && e.Archived {
		add(fmt.Sprintf("%s already has a factory, archived: %s in %s. %s", e.Label, e.Status, e.Stage, UnarchiveAdvice), theme.FgInk(theme.Yellow))
		return spans
	}
	if e, ok := m.existing(); ok {
		where := ""
		if e.Remote != "" {
			where = ", on " + e.Remote
		}
		add(fmt.Sprintf("%s already has a factory%s: %s in %s. No second factory is created.", e.Label, where, e.Status, e.Stage), theme.FgInk(theme.Yellow))
		return spans
	}
	if e, ok := m.retiredMatch(); ok {
		add(fmt.Sprintf("%s's earlier factory is %s and its worktree is gone, so this starts a new one as %s. The old row stays as history.", e.Label, e.Status, m.slug()), theme.Muted)
		return spans
	}
	switch {
	case m.name == "":
		add("A short id for this factory, such as csv-export. It names the worktree. You write what the factory should do next.", theme.Muted)
	case m.id() != "" && !factory.IsReservedID(m.name):
		add(fmt.Sprintf("%s is a new factory%s. You'll write its prompt next.", m.id(), onRemote(m.placeName())), theme.Muted)
	}
	return spans
}

func (m *NewFactoryModel) drawPlaces(c *theme.Canvas, y, w int) int {
	c.Put(1, y, "Runs on", theme.Bright.B())
	y++
	x := 3
	defaultAt := defaultPlace(m.Context)
	for i, name := range m.placeNames() {
		mark, ink := "○ ", theme.Text
		unreachable := i > 0 && i <= len(m.Context.Nodes) && m.Context.Nodes[i-1].Unreachable
		switch {
		case i == m.place:
			mark, ink = "● ", theme.Bright
		case unreachable:
			mark, ink = "↮ ", theme.Quiet
		}
		x = c.Put(x, y, mark+name, ink)
		if i == defaultAt {
			x = c.Put(x+2, y, "default", theme.Muted)
		}
		if unreachable {
			x = c.Put(x+2, y, "unreachable", theme.Muted)
		}
		x += 3
	}
	y++
	if choice, ok := m.nodeChoice(); ok {
		m.drawNodeState(c, y, w, choice)
	} else {
		c.PutMax(3, y, "Runs here, in a new worktree of "+m.selectedRepo().Display+".", theme.Muted, w-3)
	}
	return y + 2
}

func (m *NewFactoryModel) placeNames() []string {
	names := []string{"this machine"}
	for _, n := range m.Context.Nodes {
		names = append(names, shortName(n.Name))
	}
	return names
}

func (m *NewFactoryModel) drawNodeState(c *theme.Canvas, y, w int, choice NodeChoice) {
	short := shortName(choice.Name)
	p := m.probes[choice.Name]
	switch {
	case choice.Unreachable:
		c.PutMax(3, y, "✗ "+short+" is unreachable, so nothing starts there now.", theme.FgInk(theme.Red), w-3)
	case !p.done:
		c.PutMax(3, y, "◌ checking "+short+"…", theme.FgInk(theme.Cyan), w-3)
	case p.err != nil:
		c.PutMax(3, y, "✗ "+p.err.Error(), theme.FgInk(theme.Red), w-3)
	default:
		c.PutMax(3, y, "↔ "+p.text, theme.FgInk(theme.Cyan), w-3)
	}
}

func (m *NewFactoryModel) drawPreview(c *theme.Canvas, x, y, w int) {
	repo := m.selectedRepo()
	if name := m.placeName(); name != "" {
		m.drawNodePreview(c, x, y, w, name)
		return
	}
	c.Put(x, y, "Will run, from "+theme.KeepTail(repo.Display, w-theme.Width("Will run, from ")), theme.Quiet)
	y++
	launch := m.launch()
	command := strings.SplitN(launch.CreateCommand(), "; cd ", 2)[0]
	if !launch.Supervisor.OnClaude() {
		_, command, _ = strings.Cut(launch.CreateCommand(), "\n")
	}
	for _, l := range theme.Wrap(command, w-4) {
		c.Put(x+2, y, l, theme.Text)
		y++
	}
	reason := "QA runs the app in a scratch tmux server: the " + repo.Name + " repo config uses terminal QA."
	switch {
	case !launch.Supervisor.OnClaude():
		reason = "in a new worktree on worktree-" + m.slug() + "; QA opens its own Claude pane."
	case repo.Browser:
		reason = "QA opens its own Claude pane on headless Chrome: the " + repo.Name + " repo config uses browser QA."
	}
	c.Put(x, y, reason, theme.Muted)
}

func (m *NewFactoryModel) drawNodePreview(c *theme.Canvas, x, y, w int, name string) {
	repo := m.selectedRepo()
	c.Put(x, y, "Will run on "+name+", from its clone of "+strings.TrimSuffix(filepath.Base(repo.GitURL), ".git"), theme.Quiet)
	y++
	launch := m.launch()
	command := strings.SplitN(launch.CreateCommand(), "; cd ", 2)[0]
	if !launch.Supervisor.OnClaude() {
		_, command, _ = strings.Cut(launch.CreateCommand(), "\n")
	}
	for _, l := range theme.Wrap(command, w-4) {
		c.Put(x+2, y, l, theme.Text)
		y++
	}
	for _, l := range theme.Wrap(name+" clones "+repo.GitURL+" there the first time, then fetches.", w) {
		c.Put(x, y, l, theme.Muted)
		y++
	}
}

func (m *NewFactoryModel) drawPrompt(c *theme.Canvas) {
	w := c.W - 2
	name, _ := m.chosenPipeline()
	c.PutMax(1, 0, "New factory · "+m.slug()+" · "+name, theme.Bright.B(), w-8)
	c.Right(c.W-2, 0, "3 of 3", theme.Muted)
	y := 2
	for _, l := range theme.Wrap("Describe what this factory should do: the change, for whom, and anything you already know you want. The supervisor writes it to brief.md word for word, and every stage reads it as the spec. The factory reads a tracker issue only if you ask it to here.", w) {
		c.Put(1, y, l, theme.Text)
		y++
	}
	y++
	y += m.drawPromptInput(c, y, w, max(c.H-y-5, 1)) + 2
	if m.problem != "" {
		for _, l := range theme.Wrap("✗ "+m.problem, w) {
			y++
			c.Put(1, y, l, theme.FgInk(theme.Red))
		}
	}
	count := plural(strings.Count(m.prompt.text, "\n")+1, "line") + " · " + plural(uniseg.GraphemeClusterCount(m.prompt.text), "character")
	hintsStart := c.Right(c.W-2, c.H-1, "ctrl+s start   ⏎ new line   ctrl+g open in editor   esc back", theme.Text)
	if theme.Width(count)+2 < hintsStart {
		c.Put(1, c.H-1, count, theme.Muted)
	}
}

func (m *NewFactoryModel) drawPromptInput(c *theme.Canvas, y, w, room int) int {
	e := &m.prompt
	rows := wrapRows(e.text, m.promptWidth())
	row, col := e.locate(rows)
	e.scroll(row, room, len(rows))
	visible := rows[e.top:min(e.top+room, len(rows))]
	lines := make([]string, len(visible))
	for i, r := range visible {
		number := "   "
		if r.first {
			number = fmt.Sprintf("%2d ", r.line+1)
		}
		lines[i] = number + e.text[r.start:r.end]
	}
	drawInput(c, 1, y, w, "prompt", lines, true, theme.Focused)
	under := " "
	if e.cursor < rows[row].end {
		if cluster := e.text[e.cursor:nextGrapheme(e.text, e.cursor)]; theme.Width(cluster) > 0 {
			under = cluster
		}
	}
	c.Put(6+col, y+1+row-e.top, under, theme.Ink{FG: theme.Black, BG: theme.Hi})
	return len(lines)
}

func (m *NewFactoryModel) drawStages(c *theme.Canvas) {
	w := c.W - 2
	c.Put(1, 0, "Stages & agents · "+m.slug(), theme.Bright.B())
	c.Right(c.W-2, 0, "2 of 3", theme.Muted)
	hints := "⏎ next   space stage   t tool   m model   e effort   tab preset   esc back"
	if problem := m.selectedRepo().PipelineProblem; problem != "" {
		for i, l := range theme.Wrap("✗ "+problem, w) {
			c.Put(1, 2+i, l, theme.FgInk(theme.Red))
		}
		c.Put(1, c.H-1, "esc back", theme.Text)
		return
	}
	y := m.drawPresets(c, 2)
	y++
	m.drawSupervisorRow(c, y)
	y++
	running := m.runStages()
	for i, stage := range state.StageKeys {
		m.drawStageRow(c, y, i+firstStageRow, stage, slices.Contains(running, stage))
		y++
	}
	y++
	m.drawPreview(c, 1, y, w)
	if m.problem != "" {
		for _, l := range theme.Wrap("✗ "+m.problem, w) {
			c.Put(1, c.H-3, l, theme.FgInk(theme.Red))
		}
	}
	c.Put(1, c.H-1, hints, theme.Text)
}

func (m *NewFactoryModel) drawPresets(c *theme.Canvas, y int) int {
	x := c.Put(1, y, "Pipeline", theme.Bright.B()) + 2
	chosen, _ := m.chosenPipeline()
	names := m.selectedRepo().Pipelines
	options := make([]string, 0, len(names)+1)
	for _, p := range names {
		options = append(options, p.Name)
	}
	if chosen == state.CustomPipeline {
		options = append(options, chosen)
	}
	for _, name := range options {
		mark, ink := "○ ", theme.Text
		if name == chosen {
			mark, ink = "● ", theme.Bright
		}
		x = c.Put(x, y, mark+name, ink) + 2
	}
	return y + 1
}

func (m *NewFactoryModel) drawStageRow(c *theme.Canvas, y, i int, stage string, running bool) {
	if i == m.row {
		c.Put(1, y, "❯", theme.Focused.B())
	}
	check, ink := "·", theme.Muted
	if running {
		check, ink = "✓", theme.Bright
	}
	if m.locked(stage) {
		ink = theme.Quiet
	}
	c.Put(3, y, check, ink)
	c.Put(5, y, stage, ink)
	role := state.RoleOf(stage)
	switch {
	case stage == "coderabbit" && !m.selectedRepo().CodeRabbit:
		c.Put(19, y, "off in this repo", theme.Quiet)
	case stage == "walkthrough" && !m.selectedRepo().Browser:
		c.Put(19, y, "browser QA repos only", theme.Quiet)
	case role == "":
		c.Put(19, y, "—", theme.Quiet)
	default:
		c.Put(19, y, role, theme.Text)
		m.drawSetting(c, y, role)
	}
}

func (m *NewFactoryModel) drawSupervisorRow(c *theme.Canvas, y int) {
	if m.row < firstStageRow {
		c.Put(1, y, "❯", theme.Focused.B())
	}
	c.Put(5, y, supervisorRole, theme.Bright)
	m.drawSetting(c, y, supervisorRole)
}

func (m *NewFactoryModel) drawSetting(c *theme.Canvas, y int, role string) {
	setting, override := m.agent(role), m.overrides[role]
	c.Put(31, y, setting.ToolName(), overrideInk(override.Tool))
	model := setting.Model
	if model == "" {
		model = "default"
	}
	c.PutMax(41, y, model, overrideInk(override.Model), c.W-41-9)
	c.Put(c.W-9, y, setting.Effort, overrideInk(override.Effort))
}

func overrideInk(value string) theme.Ink {
	if value != "" {
		return theme.Bright.B()
	}
	return theme.Muted
}

func drawInput(c *theme.Canvas, x, y, w int, title string, lines []string, focused bool, titleInk theme.Ink) {
	border := theme.Line
	if focused {
		border = theme.Focused
	}
	head := ""
	if title != "" {
		head = "─ " + title + " "
	}
	c.Put(x, y, "╭"+head+strings.Repeat("─", max(w-2-theme.Width(head), 0))+"╮", border)
	if title != "" {
		c.Put(x+3, y, title, titleInk)
	}
	for i, l := range lines {
		c.Put(x, y+1+i, "│ ", border)
		if focused && strings.HasSuffix(l, "█") {
			for theme.Width(l) > max(w-4, 1) {
				_, size := utf8.DecodeRuneInString(l)
				l = l[size:]
			}
		}
		c.PutMax(x+2, y+1+i, l, theme.Bright, w-4)
		c.Put(x+w-1, y+1+i, "│", border)
	}
	c.Put(x, y+1+len(lines), "╰"+strings.Repeat("─", max(w-2, 0))+"╯", border)
}

func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}
