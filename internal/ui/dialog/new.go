package dialog

import (
	"fmt"
	"regexp"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/rivo/uniseg"

	"github.com/afonsodelgado/factory/internal/factory"
	"github.com/afonsodelgado/factory/internal/state"
	"github.com/afonsodelgado/factory/internal/ui/theme"
)

type NewFactoryModel struct {
	Context   NewContext
	repo      int
	name      string
	prompt    string
	pipeline  string
	chosen    []string
	overrides map[string]state.AgentSetting
	row       int
	step      int
	problem   string
	result    *NewResult
	width     int
	height    int
}

func NewFactory(c NewContext) *NewFactoryModel {
	return &NewFactoryModel{Context: c, repo: max(0, min(c.Repo, len(c.Repos)-1)), step: 1}
}

func (m *NewFactoryModel) Init() tea.Cmd { return nil }

func (m *NewFactoryModel) Result() (any, bool) {
	if m.result == nil {
		return nil, false
	}
	return *m.result, true
}

func (m *NewFactoryModel) selectedRepo() RepoOption {
	if len(m.Context.Repos) == 0 {
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
	return factory.Launch{Repo: repo.Path, Slug: m.slug(), Pipeline: name, Stages: stages, Agents: m.overrides, SessionID: "<uuid>", Chrome: repo.Browser, RemoteControl: m.Context.RemoteControl}
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
	return state.EffectiveStages(m.chosen, !m.selectedRepo().CodeRabbit)
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
	m.pipeline, m.chosen, m.overrides, m.row = "", nil, nil, 0
}

func (m *NewFactoryModel) locked(stage string) bool {
	codeRabbitOff := stage == "coderabbit" && !m.selectedRepo().CodeRabbit
	qaSkipped := state.DependsOnQA(stage) && !slices.Contains(m.runStages(), "qa")
	return state.AlwaysRuns(stage) || codeRabbitOff || qaSkipped
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
	for _, a := range m.selectedRepo().Agents {
		if a.Role == role {
			return a.AgentSetting
		}
	}
	return state.DefaultAgents[role]
}

func (m *NewFactoryModel) agent(role string) state.AgentSetting {
	setting := m.defaultAgent(role)
	if o := m.overrides[role]; o.Model != "" {
		setting.Model = o.Model
	}
	if o := m.overrides[role]; o.Effort != "" {
		setting.Effort = o.Effort
	}
	return setting
}

func (m *NewFactoryModel) cycleModel(role string) {
	m.setOverride(role, func(o *state.AgentSetting) {
		o.Model = nextOverride(state.AgentModels, m.agent(role).Model, m.defaultAgent(role).Model)
	})
}

func (m *NewFactoryModel) cycleEffort(role string) {
	m.setOverride(role, func(o *state.AgentSetting) {
		o.Effort = nextOverride(state.AgentEfforts, m.agent(role).Effort, m.defaultAgent(role).Effort)
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
		m.typeText(DecodeKeys(msg.Content))
	case tea.KeyPressMsg:
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
	switch m.step {
	case 1:
		m.name += strings.ReplaceAll(text, "\n", " ")
	case 3:
		m.prompt += text
	}
}

func (m *NewFactoryModel) nameKey(msg tea.KeyPressMsg) tea.Cmd {
	m.problem = ""
	switch msg.String() {
	case "esc", "ctrl+c":
		return tea.Quit
	case "up":
		m.repo = max(m.repo-1, 0)
		m.resetChoices()
	case "down", "tab":
		m.repo = (m.repo + 1) % max(len(m.Context.Repos), 1)
		m.resetChoices()
	case "backspace":
		m.name = dropLast(m.name)
	case "ctrl+u":
		m.name = ""
	case "ctrl+w":
		if e, ok := m.existing(); ok && !e.Archived {
			m.result = &NewResult{Select: e.Key, Wake: true}
			return tea.Quit
		}
	case "enter":
		return m.next()
	default:
		m.name += DecodeKeys(msg.Text)
	}
	return nil
}

func (m *NewFactoryModel) next() tea.Cmd {
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
	case "enter", "ctrl+j":
		m.prompt += "\n"
	case "backspace":
		m.prompt = dropLast(m.prompt)
	case "ctrl+u":
		m.prompt = ""
	default:
		m.prompt += DecodeKeys(msg.Text)
	}
	return nil
}

func (m *NewFactoryModel) stagesKey(msg tea.KeyPressMsg) tea.Cmd {
	m.problem = ""
	stage := state.StageKeys[m.row]
	switch msg.String() {
	case "esc":
		m.step = 1
	case "ctrl+c":
		return tea.Quit
	case "enter":
		if problem := m.selectedRepo().PipelineProblem; problem != "" {
			m.problem = problem
			return nil
		}
		m.step = 3
	case "up":
		m.row = max(m.row-1, 0)
	case "down":
		m.row = min(m.row+1, len(state.StageKeys)-1)
	case "tab":
		m.nextPipeline()
	case "space":
		m.toggle(stage)
	case "m":
		m.cycleModel(state.RoleOf(stage))
	case "e":
		m.cycleEffort(state.RoleOf(stage))
	}
	return nil
}

func (m *NewFactoryModel) start() tea.Cmd {
	if strings.TrimSpace(m.prompt) == "" {
		m.problem = "The prompt is empty. Write at least a sentence about what this factory should do; it won't start without one."
		return nil
	}
	if problem := m.selectedRepo().PipelineProblem; problem != "" {
		m.problem = problem
		return nil
	}
	name, stages := m.chosenPipeline()
	m.result = &NewResult{Repo: m.selectedRepo().Path, Slug: m.slug(), Prompt: m.prompt, Pipeline: name, Stages: stages}
	if len(m.overrides) > 0 {
		m.result.Agents = m.overrides
	}
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
	y++
	c.Put(1, y, "Factory id", theme.Bright.B())
	y++
	drawInput(c, 1, y, w, "", []string{m.name}, true)
	y += 3
	for _, l := range m.notice(w) {
		c.Spans(1, y, l)
		y++
	}
	if m.problem != "" {
		for _, l := range theme.Wrap("✗ "+m.problem, w) {
			c.Put(1, y, l, theme.FgInk(theme.Red))
			y++
		}
	}
	if _, matched := m.existing(); !matched && m.id() != "" && !factory.IsReservedID(m.name) {
		y++
		m.drawPreview(c, 1, y, w)
	}
	hints := "⏎ next: pipeline   ↑↓ repo   esc cancel"
	if e, matched := m.existing(); matched && e.Archived {
		hints = "esc cancel"
	} else if matched {
		hints = "⏎ select it   ctrl+w select and wake it   esc cancel"
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
		add(fmt.Sprintf("%s already has a factory: %s in %s. No second factory is created.", e.Label, e.Status, e.Stage), theme.FgInk(theme.Yellow))
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
		add(fmt.Sprintf("%s is a new factory. You'll write its prompt next.", m.id()), theme.Muted)
	}
	return spans
}

func (m *NewFactoryModel) drawPreview(c *theme.Canvas, x, y, w int) {
	repo := m.selectedRepo()
	c.Put(x, y, "Will run, from "+repo.Display, theme.Quiet)
	y++
	for _, l := range theme.Wrap(strings.SplitN(m.launch().CreateCommand(), "; cd ", 2)[0], w-4) {
		c.Put(x+2, y, l, theme.Text)
		y++
	}
	reason := "no --chrome: the " + repo.Name + " profile uses terminal QA."
	if repo.Browser {
		reason = "--chrome because the " + repo.Name + " profile uses browser QA."
	}
	c.Put(x, y, reason, theme.Muted)
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
	var lines []string
	for i, raw := range strings.Split(m.prompt, "\n") {
		for j, l := range theme.Wrap(raw, w-8) {
			number := "   "
			if j == 0 {
				number = fmt.Sprintf("%2d ", i+1)
			}
			lines = append(lines, number+l)
		}
	}
	room := c.H - y - 5
	if len(lines) > room && room > 0 {
		lines = lines[len(lines)-room:]
	}
	drawInput(c, 1, y, w, "prompt", lines, true)
	y += len(lines) + 2
	if m.problem != "" {
		for _, l := range theme.Wrap("✗ "+m.problem, w) {
			y++
			c.Put(1, y, l, theme.FgInk(theme.Red))
		}
	}
	count := fmt.Sprintf("%d lines · %d characters", strings.Count(m.prompt, "\n")+1, uniseg.GraphemeClusterCount(m.prompt))
	c.Put(1, c.H-1, count, theme.Muted)
	c.Right(c.W-2, c.H-1, "ctrl+s start factory   ctrl+j or ⏎ new line   esc back", theme.Text)
}

func (m *NewFactoryModel) drawStages(c *theme.Canvas) {
	w := c.W - 2
	c.Put(1, 0, "Stages & agents · "+m.slug(), theme.Bright.B())
	c.Right(c.W-2, 0, "2 of 3", theme.Muted)
	hints := "⏎ next: prompt   space stage   m model   e effort   tab preset   esc back"
	if problem := m.selectedRepo().PipelineProblem; problem != "" {
		for i, l := range theme.Wrap("✗ "+problem, w) {
			c.Put(1, 2+i, l, theme.FgInk(theme.Red))
		}
		c.Put(1, c.H-1, "esc back", theme.Text)
		return
	}
	y := m.drawPresets(c, 2)
	y++
	running := m.runStages()
	for i, stage := range state.StageKeys {
		m.drawStageRow(c, y, i, stage, slices.Contains(running, stage))
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
	case role == "":
		c.Put(19, y, "—", theme.Quiet)
	default:
		setting, override := m.agent(role), m.overrides[role]
		c.Put(19, y, role, theme.Text)
		c.Put(31, y, setting.Model, overrideInk(override.Model))
		c.Put(39, y, setting.Effort, overrideInk(override.Effort))
	}
}

func overrideInk(value string) theme.Ink {
	if value != "" {
		return theme.Bright.B()
	}
	return theme.Muted
}

func drawInput(c *theme.Canvas, x, y, w int, title string, lines []string, focused bool) {
	border := theme.Line
	if focused {
		border = theme.Focused
	}
	head := ""
	if title != "" {
		head = "─ " + title + " "
	}
	c.Put(x, y, "╭"+head+strings.Repeat("─", max(w-2-theme.Width(head), 0))+"╮", border)
	for i, l := range lines {
		if i == len(lines)-1 && focused {
			l += "█"
		}
		c.Put(x, y+1+i, "│ ", border)
		c.PutMax(x+2, y+1+i, l, theme.Bright, w-4)
		c.Put(x+w-1, y+1+i, "│", border)
	}
	c.Put(x, y+1+len(lines), "╰"+strings.Repeat("─", max(w-2, 0))+"╯", border)
}
