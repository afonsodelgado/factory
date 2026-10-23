package state

import (
	"fmt"
	"slices"
	"strings"
)

type AgentSetting struct {
	Model  string `yaml:"model" json:"model,omitempty"`
	Effort string `yaml:"effort" json:"effort,omitempty"`
}

var AgentRoles = []string{"analyst", "designer", "architect", "engineer", "reviewer", "shepherd", "qa", "fixer"}

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

func (s AgentSetting) over(base AgentSetting) AgentSetting {
	if s.Model != "" {
		base.Model = s.Model
	}
	if s.Effort != "" {
		base.Effort = s.Effort
	}
	return base
}

func (s AgentSetting) Flag(role string) string {
	return role + "=" + s.Model + ":" + s.Effort
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
		setting := agents[role]
		if !slices.Contains(AgentRoles, role) {
			return fmt.Errorf("%s: agents.%s is not an agent; agents are %s", source, role, strings.Join(AgentRoles, ", "))
		}
		if setting.Model != "" && !slices.Contains(AgentModels, setting.Model) {
			return fmt.Errorf("%s: agents.%s.model %q is not a model; models are %s", source, role, setting.Model, strings.Join(AgentModels, ", "))
		}
		if setting.Effort != "" && !slices.Contains(AgentEfforts, setting.Effort) {
			return fmt.Errorf("%s: agents.%s.effort %q is not an effort; efforts are %s", source, role, setting.Effort, strings.Join(AgentEfforts, ", "))
		}
	}
	return nil
}

func ParseAgentFlag(flag string) (string, AgentSetting, error) {
	role, value, hasValue := strings.Cut(flag, "=")
	model, effort, _ := strings.Cut(value, ":")
	setting := AgentSetting{Model: model, Effort: effort}
	if !hasValue || setting == (AgentSetting{}) {
		return "", setting, fmt.Errorf("--agent %q: write it as role=model:effort, such as engineer=sonnet:medium or reviewer=:low", flag)
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
