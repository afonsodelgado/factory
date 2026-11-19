package dialog

import (
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/afonsodelgado/factory/internal/install"
	"github.com/afonsodelgado/factory/internal/recipe"
	"github.com/afonsodelgado/factory/internal/state"
	"github.com/afonsodelgado/factory/internal/ui/theme"
)

type Settings struct {
	Context       SettingsContext
	settings      *state.RepoSettings
	values        map[string]string
	field         int
	detected      map[string]state.Command
	problem       string
	result        *SettingsResult
	width, height int
}

func SettingsTitle() string {
	gear := "⚙︎"
	if theme.Width(gear) != 1 {
		gear = "≡"
	}
	return gear + " Repo settings"
}

func NewSettings(context SettingsContext) *Settings {
	m := &Settings{Context: context}
	var err error
	m.settings, err = state.ReadRepoSettings(context.Repo)
	if err != nil {
		m.problem = err.Error()
	} else {
		m.values = maps.Clone(m.settings.Values)
		if resolved, err := recipe.Resolve(context.Repo); err == nil {
			m.detected = map[string]state.Command{}
			for key, c := range resolved.Recipe.Commands {
				m.detected["commands."+key] = c
			}
			for key, c := range resolved.Recipe.QA {
				m.detected["qa."+key] = c
			}
		}
	}
	return m
}

func (m *Settings) Init() tea.Cmd { return nil }
func (m *Settings) Result() (any, bool) {
	if m.result == nil {
		return nil, false
	}
	return *m.result, true
}

func (m *Settings) fields() []string {
	return state.SettingsKeys(m.values["qa.mode"])
}

func settingChoice(key string) bool { return key == "qa.mode" }

func (m *Settings) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case tea.PasteMsg:
		if m.settings != nil {
			m.typeText(DecodeKeys(msg.Content))
		}
	case tea.KeyPressMsg:
		if msg.String() == "esc" || msg.String() == "ctrl+c" {
			m.result = &SettingsResult{Repo: m.Context.Repo}
			return m, tea.Quit
		}
		if m.settings == nil {
			return m, nil
		}
		fields := m.fields()
		key := fields[m.field]
		switch msg.String() {
		case "ctrl+s":
			if err := m.settings.Save(m.values); err != nil {
				m.problem = err.Error()
				return m, nil
			}
			m.result = &SettingsResult{Repo: m.Context.Repo, Saved: true}
			return m, tea.Quit
		case "up", "shift+tab":
			m.field = (m.field + len(fields) - 1) % len(fields)
		case "down", "tab":
			m.field = (m.field + 1) % len(fields)
		case "space", "left", "right":
			switch key {
			case "qa.mode":
				if m.values[key] == "browser" {
					m.values[key] = "terminal"
				} else {
					m.values[key] = "browser"
				}
			default:
				if msg.String() == "space" {
					m.typeText(" ")
				}
			}
		case "backspace":
			if !settingChoice(key) {
				m.values[key] = dropLast(m.values[key])
			}
		case "ctrl+u":
			if !settingChoice(key) {
				m.values[key] = ""
			}
		default:
			m.typeText(DecodeKeys(msg.Text))
		}
	}
	return m, nil
}

func (m *Settings) typeText(text string) {
	key := m.fields()[m.field]
	if !settingChoice(key) {
		m.values[key] += strings.ReplaceAll(text, "\n", " ")
	}
}

func (m *Settings) help() string {
	key := m.fields()[m.field]
	_, optional := optionalKeys[key]
	_, discovered := m.settings.Provenance[key]
	switch {
	case strings.Contains(m.values[key], "\n"):
		return "Multi-line value: ↵ marks a line break. Typing appends; ctrl+u clears the whole value."
	case discovered && m.values[key] != m.settings.Values[key]:
		return "Saving makes this value yours: the found-by note goes, and discovery never changes it again. Clear it to let the next factory discover " + settingTitle(key) + " here."
	case m.values[key] == "" && !optional:
		return "Blank: " + filepath.Base(m.Context.Repo) + "'s first factory on this machine discovers it, runs it here and saves it with a note of who found it. Anything you type wins; discovery never changes it."
	}
	if !m.settings.Present[key] {
		return "The file has no " + key + " yet. Saving adds it."
	}
	switch key {
	case "qa.mode":
		return "browser: QA drives Chrome against the local stack. terminal: QA runs the app in a scratch tmux server."
	case "qa.url":
		return "The app's local URL. ${FACTORY_WEB_PORT} in it lets every node pick a free port for each stack."
	case "qa.start":
		return "The command QA runs to start the local browser stack."
	case "qa.health":
		return "The URL QA checks to know the local stack is ready."
	case "qa.seed":
		return "Optional. A command that loads repeatable test data before QA. Blank or none: QA creates the data it needs through the app, on the local stack only."
	case "qa.sign_in":
		return "Optional. A command that signs in and prints the session cookies as JSON. Blank or none: QA signs up or logs in through the app, on the local stack only."
	default:
		return "build, lint and test are the focused checks every layer of a factory's stack runs. A cleared field is saved as an empty string and keeps its place."
	}
}

func (m *Settings) View() tea.View {
	c := theme.NewCanvas(m.width, m.height)
	w := max(c.W-2, 1)
	c.PutMax(1, 0, filepath.Base(m.Context.Repo)+"  "+install.HomeRelative(state.RepoConfigPath(m.Context.Repo)), theme.Bright, w)
	if m.settings == nil {
		c.Put(1, 2, "✗ Could not read repo settings. The file is unchanged.", theme.FgInk(theme.Red))
		lines := strings.Split(m.problem, "\n")
		lines = append(theme.Wrap(lines[0], w), lines[1:]...)
		for i, line := range lines {
			c.PutMax(1, 4+i, line, theme.FgInk(theme.Red), w)
		}
		c.Put(1, c.H-1, "esc close", theme.Text)
	} else {
		m.drawForm(c, w)
	}
	v := tea.NewView(c.Render())
	v.AltScreen = true
	return v
}

func (m *Settings) drawForm(c *theme.Canvas, w int) {
	changes := m.settings.Changes(m.values)
	if len(changes) > 0 {
		c.Right(c.W-2, 0, fmt.Sprintf("%d edited", len(changes)), theme.Bright.B())
	}
	c.PutMax(1, 1, "Saves only these keys. Comments, key order and every other key in the file stay.", theme.Muted, w)
	y := 2
	if m.Context.Banner != "" {
		for _, line := range theme.Wrap(m.Context.Banner, w) {
			c.Put(1, y, line, theme.FgInk(theme.Green))
			y++
		}
	}
	fields := m.fields()
	key := fields[m.field]
	action := "ctrl+u clear"
	if settingChoice(key) {
		action = "space switch"
	}
	hints := "ctrl+s save   ↑↓ field   " + action + "   esc cancel"
	help := theme.Wrap(m.help(), w)
	var problem []string
	if m.problem != "" {
		problem = theme.Wrap("✗ "+m.problem, w)
		problem = problem[:min(len(problem), max(c.H-y-4, 1))]
	}
	heights := make([]int, len(fields))
	total := 0
	for i, field := range fields {
		heights[i] = 3
		if settingChoice(field) {
			heights[i] = 1
		}
		total += heights[i]
	}
	blanks := 4
	if total+y+blanks+len(help)+len(problem)+1 > c.H {
		help = nil
	}
	if total+y+blanks+len(help)+len(problem)+1 > c.H {
		blanks = 0
	}
	bottom := c.H - 1 - len(help) - len(problem)
	if blanks > 0 {
		y++
	}
	start, focusEnd := 0, 0
	for i := 0; i <= m.field; i++ {
		focusEnd += heights[i]
	}
	for focusEnd > bottom-y && start < m.field {
		focusEnd -= heights[start]
		start++
	}
	c.Clip(y, bottom)
	for i := start; i < len(fields); i++ {
		field := fields[i]
		if blanks > 0 && field == "qa.mode" {
			y++
		}
		focused := i == m.field
		edited := slices.Contains(changes, field)
		m.drawField(c, y, w, field, focused, edited)
		y += heights[i]
	}
	if m.values["qa.mode"] == "terminal" && slices.ContainsFunc(browserOnlyKeys, func(key string) bool { return m.settings.Present[key] }) {
		c.PutMax(1, y+1, "start, health, seed and sign_in stay in the file; terminal QA does not use them.", theme.Muted, w)
	}
	c.Unclip()
	for i, line := range help {
		c.Put(1, bottom+i, line, theme.Muted)
	}
	for i, line := range problem {
		c.Put(1, bottom+len(help)+i, line, theme.FgInk(theme.Red))
	}
	c.Put(1, c.H-1, hints, theme.Text)
}

var browserOnlyKeys = []string{"qa.start", "qa.health", "qa.seed", "qa.sign_in", "qa.url"}

var optionalKeys = map[string]string{
	"qa.seed":    "not used · QA creates the data it needs through the app",
	"qa.sign_in": "not used · QA signs up or logs in through the app",
}

func settingTitle(key string) string {
	return strings.TrimPrefix(strings.TrimPrefix(key, "commands."), "qa.")
}

func (m *Settings) foundBy(key string) string {
	found, ok := m.settings.Provenance[key]
	if !ok {
		return ""
	}
	return "found by " + found.Factory + " · " + found.Day.Format("2 Jan")
}

func (m *Settings) drawField(c *theme.Canvas, y, w int, key string, focused, edited bool) {
	title := settingTitle(key)
	ink := theme.Quiet
	if focused {
		ink = theme.Focused
	}
	if settingChoice(key) {
		title, options, current := "QA mode", []string{"browser", "terminal"}, m.values[key]

		if focused {
			c.Put(1, y, "❯", theme.Focused.B())
		}
		c.Put(3, y, title, ink)
		x := 16
		for _, option := range options {
			mark, style := "○ ", theme.Text
			if option == current {
				mark, style = "● ", theme.Bright
			}
			x = c.Put(x, y, mark+option, style) + 3
		}
		switch {
		case edited:
			c.Put(x, y, "edited", theme.Bright.B())
		case current == "":
			c.Put(x, y, "not chosen yet · discovered too", theme.Muted)
		case m.foundBy(key) != "":
			c.Put(x, y, m.foundBy(key), theme.Muted)
		}
		return
	}
	value := strings.ReplaceAll(m.values[key], "\n", " ↵ ")
	unused, optional := optionalKeys[key]
	blank := value == ""
	if blank && !focused {
		if detected, ok := m.detected[key]; ok {
			value = detected.Command + " (detected: " + detected.Source + ")"
		}
	}
	if focused {
		value += "█"
	}
	drawInput(c, 1, y, w, title, []string{value}, focused, ink)
	switch {
	case blank && optional && focused:
		c.Put(6, y+1, "blank: not used", theme.Muted)
	case blank && optional:
		c.PutMax(3, y+1, unused, theme.Muted, w-4)
	case blank && !focused && m.detected[key].Command == "":
		c.PutMax(3, y+1, "blank · the first factory here discovers it", theme.Muted, w-4)
	}
	switch {
	case edited:
		c.Right(c.W-4, y, " edited ", theme.Bright.B())
	case optional:
		c.Right(c.W-4, y, " optional ", theme.Muted)
	case m.foundBy(key) != "":
		c.Right(c.W-4, y, " "+m.foundBy(key)+" ", theme.Muted)
	}
}
