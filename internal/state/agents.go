package state

import (
	"fmt"
	"slices"
	"strings"
)

type AgentSetting struct {
	Tool   string `yaml:"tool" json:"tool,omitempty"`
	Model  string `yaml:"model" json:"model,omitempty"`
	Effort string `yaml:"effort" json:"effort,omitempty"`
}

var AgentRoles = []string{"analyst", "designer", "architect", "engineer", "reviewer", "shepherd", "qa", "fixer"}

const (
	ToolClaude   = "claude"
	ToolCodex    = "codex"
	ToolOpenCode = "opencode"
)

var Tools = []string{ToolClaude, ToolCodex, ToolOpenCode}

var RoleTools = Tools

var SupervisorTools = []string{ToolClaude}

var AgentModels = []string{"haiku", "sonnet", "opus", "fable"}

var AgentEfforts = []string{"low", "medium", "high", "xhigh", "max"}

var DefaultAgents = map[string]AgentSetting{
	"analyst":   {Model: "opus", Effort: "high"},
	"designer":  {Model: "opus", Effort: "xhigh"},
	"architect": {Model: "fable", Effort: "high"},
	"engineer":  {Model: "opus", Effort: "xhigh"},
	"reviewer":  {Model: "opus", Effort: "high"},
	"shepherd":  {Model: "opus", Effort: "high"},
	"qa":        {Model: "opus", Effort: "high"},
	"fixer":     {Model: "opus", Effort: "xhigh"},
}

var stageRoles = map[string]string{
	"requirements": "analyst",
	"design":       "designer",
	"plan":         "architect",
	"implement":    "engineer",
	"review":       "reviewer",
	"coderabbit":   "shepherd",
	"qa":           "qa",
	"qa_fixes":     "fixer",
	"test_gaps":    "fixer",
}

func RoleOf(stage string) string { return stageRoles[stage] }

func (s AgentSetting) ToolName() string {
	if s.Tool == "" {
		return ToolClaude
	}
	return s.Tool
}

func (s AgentSetting) OnClaude() bool { return s.ToolName() == ToolClaude }

func (s AgentSetting) over(base AgentSetting) AgentSetting {
	if s.Tool != "" && s.ToolName() != base.ToolName() {
		base = AgentSetting{Tool: s.Tool}
	}
	if s.Model != "" {
		base.Model = s.Model
	}
	if s.Effort != "" {
		base.Effort = s.Effort
	}
	return base
}

func (s AgentSetting) Value() string {
	value := s.Model + ":" + s.Effort
	if s.OnClaude() {
		return value
	}
	return s.Tool + "/" + value
}

func (s AgentSetting) Flag(role string) string {
	return role + "=" + s.Value()
}

func ParseSetting(value string) AgentSetting {
	tool, rest, hasTool := strings.Cut(value, "/")
	if !hasTool {
		tool, rest = "", value
	}
	model, effort, _ := strings.Cut(rest, ":")
	return AgentSetting{Tool: tool, Model: model, Effort: effort}
}

func ResolveAgents(cfg *Config, profile Profile, overrides map[string]AgentSetting) map[string]AgentSetting {
	resolved := map[string]AgentSetting{}
	for _, role := range AgentRoles {
		setting := DefaultAgents[role]
		for _, layer := range []map[string]AgentSetting{cfg.Agents, profile.Agents, overrides} {
			setting = layer[role].over(setting)
		}
		resolved[role] = setting
	}
	return resolved
}

func ValidateAgents(source string, agents map[string]AgentSetting) error {
	for _, role := range sortedKeys(agents) {
		if !slices.Contains(AgentRoles, role) {
			return fmt.Errorf("%s: agents.%s is not an agent; agents are %s", source, role, strings.Join(AgentRoles, ", "))
		}
		if role == "qa" && !agents[role].OnClaude() {
			return fmt.Errorf("%s: agents.qa.tool %q is refused; QA drives Chrome and runs only on claude", source, agents[role].Tool)
		}
		if err := validateSetting(source, "agents."+role, agents[role], RoleTools); err != nil {
			return err
		}
	}
	return nil
}

func ValidateSupervisor(source string, setting AgentSetting) error {
	return validateSetting(source, "supervisor", setting, SupervisorTools)
}

func validateSetting(source, key string, setting AgentSetting, available []string) error {
	if !slices.Contains(Tools, setting.ToolName()) {
		return fmt.Errorf("%s: %s.tool %q is not a tool; tools are %s", source, key, setting.Tool, strings.Join(Tools, ", "))
	}
	if !slices.Contains(available, setting.ToolName()) {
		return fmt.Errorf("%s: %s.tool %q is not available yet; use %s", source, key, setting.Tool, strings.Join(available, ", "))
	}
	if !setting.OnClaude() {
		return nil
	}
	if setting.Model != "" && !slices.Contains(AgentModels, setting.Model) {
		return fmt.Errorf("%s: %s.model %q is not a model; models are %s", source, key, setting.Model, strings.Join(AgentModels, ", "))
	}
	if setting.Effort != "" && !slices.Contains(AgentEfforts, setting.Effort) {
		return fmt.Errorf("%s: %s.effort %q is not an effort; efforts are %s", source, key, setting.Effort, strings.Join(AgentEfforts, ", "))
	}
	return nil
}

func ParseAgentFlag(flag string) (string, AgentSetting, error) {
	role, value, hasValue := strings.Cut(flag, "=")
	setting := ParseSetting(value)
	if !hasValue || setting == (AgentSetting{}) {
		return "", setting, fmt.Errorf("--agent %q: write it as role=model:effort or role=tool/model:effort, such as engineer=sonnet:medium, reviewer=:low or engineer=codex/gpt-5.6-sol:high", flag)
	}
	return role, setting, ValidateAgents("--agent", map[string]AgentSetting{role: setting})
}

func sortedKeys(agents map[string]AgentSetting) []string {
	var keys []string
	for key := range agents {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}

func (c *Config) AgentsFor(repo string) (map[string]AgentSetting, error) {
	profile, err := ReadProfile(repo)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", ProfilePath(repo), err)
	}
	if err := ValidateAgents(ProfilePath(repo), profile.Agents); err != nil {
		return nil, err
	}
	return ResolveAgents(c, profile, nil), nil
}
