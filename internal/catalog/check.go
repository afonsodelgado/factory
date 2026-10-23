package catalog

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/afonsodelgado/factory/internal/state"
)

var ErrUnavailable = errors.New("can't check")

func PickEffort(model Model, keep string) string {
	if keep != "" && slices.Contains(model.Efforts, keep) {
		return keep
	}
	picked := ""
	for _, effort := range model.Efforts {
		if rank(effort) <= rank("high") && (picked == "" || rank(effort) > rank(picked)) {
			picked = effort
		}
	}
	return picked
}

func Find(models []Model, id string) (Model, bool) {
	for _, m := range models {
		if m.ID == id {
			return m, true
		}
	}
	return Model{}, false
}

func ToolDefault(models []Model, configured state.AgentSetting) state.AgentSetting {
	if model, ok := Find(models, configured.Model); ok {
		return state.AgentSetting{Model: model.ID, Effort: PickEffort(model, configured.Effort)}
	}
	if len(models) == 0 {
		return state.AgentSetting{}
	}
	return state.AgentSetting{Model: models[0].ID, Effort: PickEffort(models[0], "")}
}

var codexDefaultLine = regexp.MustCompile(`^\s*(model|model_reasoning_effort)\s*=\s*"([^"]*)"`)

func CodexDefault(home string) state.AgentSetting {
	data, err := os.ReadFile(filepath.Join(home, "config.toml"))
	if err != nil {
		return state.AgentSetting{}
	}
	var setting state.AgentSetting
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "[") {
			break
		}
		match := codexDefaultLine.FindStringSubmatch(line)
		switch {
		case match == nil:
		case match[1] == "model":
			setting.Model = match[2]
		default:
			setting.Effort = match[2]
		}
	}
	return setting
}

type Catalogs struct {
	CodexHome string
	StateDir  string
}

func Local(stateDir string) Catalogs {
	return Catalogs{CodexHome: state.CodexHome(), StateDir: stateDir}
}

func (c Catalogs) models(tool string) ([]Model, state.AgentSetting, error) {
	switch tool {
	case state.ToolCodex:
		models, err := Codex(c.CodexHome)
		return models, CodexDefault(c.CodexHome), err
	case state.ToolOpenCode:
		models, err := OpenCode(c.StateDir)
		return models, state.AgentSetting{}, err
	}
	return Claude(), state.AgentSetting{}, nil
}

func (c Catalogs) WithDefault(setting state.AgentSetting) state.AgentSetting {
	if setting.OnClaude() || setting.Model != "" {
		return setting
	}
	models, configured, err := c.models(setting.ToolName())
	if err != nil {
		return setting
	}
	fallback := ToolDefault(models, configured)
	setting.Model = fallback.Model
	if model, ok := Find(models, fallback.Model); ok {
		setting.Effort = PickEffort(model, setting.Effort)
	}
	return setting
}

func (c Catalogs) Validate(source, key string, setting state.AgentSetting) error {
	nothingToCheck := setting.Model == "" && setting.Effort == ""
	if setting.OnClaude() || nothingToCheck {
		return nil
	}
	tool := setting.ToolName()
	models, configured, err := c.models(tool)
	if err != nil {
		return fmt.Errorf("%w the %s model of %s in %s: %v", ErrUnavailable, tool, key, source, err)
	}
	return Check(source, key, setting, models, configured)
}

func Check(source, key string, setting state.AgentSetting, models []Model, configured state.AgentSetting) error {
	if setting.OnClaude() || (setting.Model == "" && setting.Effort == "") {
		return nil
	}
	tool := setting.ToolName()
	id := setting.Model
	if id == "" {
		id = ToolDefault(models, configured).Model
	}
	model, listed := Find(models, id)
	if !listed {
		return fmt.Errorf("%s: %s.model %q is not %s; models are %s", source, key, id, modelKind(tool), strings.Join(ids(models), ", "))
	}
	if setting.Effort != "" && !slices.Contains(model.Efforts, setting.Effort) {
		return fmt.Errorf("%s: %s.effort %q is not an effort of %s %s; it has %s", source, key, setting.Effort, tool, model.ID, effortList(model))
	}
	return nil
}

func (c Catalogs) ValidateAgents(source string, agents map[string]state.AgentSetting) error {
	var unchecked error
	for _, role := range state.AgentRoles {
		setting, ok := agents[role]
		if !ok {
			continue
		}
		err := c.Validate(source, "agents."+role, setting)
		switch {
		case errors.Is(err, ErrUnavailable):
			unchecked = err
		case err != nil:
			return err
		}
	}
	return unchecked
}

func Refusal(err error) error {
	if errors.Is(err, ErrUnavailable) {
		return nil
	}
	return err
}

func modelKind(tool string) string {
	if tool == state.ToolOpenCode {
		return "a free OpenCode model"
	}
	return "a Codex model your ChatGPT login lists"
}

func effortList(model Model) string {
	if len(model.Efforts) == 0 {
		return "none"
	}
	return strings.Join(model.Efforts, ", ")
}

func ids(models []Model) []string {
	var list []string
	for _, m := range models {
		list = append(list, m.ID)
	}
	return list
}
