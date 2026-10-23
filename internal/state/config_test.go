package state

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestStalledAfterWithoutAUnitIsRefused(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.yml"), []byte("stalled_after: 30\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FACTORY_CONFIG_DIR", dir)
	t.Setenv("FACTORY_STATE_DIR", t.TempDir())
	if cfg, err := LoadConfig(); err == nil {
		t.Errorf("stalled_after: 30 loaded as %v; want it refused, since the user meant 30 of something and silently got the default", cfg.Stalled())
	}
}

func TestPipelineResolution(t *testing.T) {
	cases := []struct {
		name, config, profile, pick string
		want                        []string
		refused                     string
	}{
		{name: "built-in quick works with no config", pick: "quick", want: []string{"requirements", "plan", "implement", "review", "handoff"}},
		{name: "global config overrides a built-in", config: "pipelines:\n  quick: [requirements, implement, handoff]\n", pick: "quick", want: []string{"requirements", "implement", "handoff"}},
		{name: "profile wins over the global config, also for the default", config: "pipelines:\n  tiny: [plan]\ndefault_pipeline: quick\n", profile: "pipelines:\n  tiny: [implement]\ndefault_pipeline: tiny\n", want: []string{"requirements", "implement", "handoff"}},
		{name: "coderabbit false in the profile removes the stage", profile: "coderabbit: false\n", pick: "full", want: []string{"requirements", "design", "plan", "implement", "review", "qa", "qa_fixes", "test_gaps", "handoff"}},
		{name: "requirements and handoff are always kept", config: "pipelines:\n  bare: [implement]\n", pick: "bare", want: []string{"requirements", "implement", "handoff"}},
		{name: "unknown stage in the profile is refused with the file and key", profile: "pipelines:\n  quick: [requirements, qa_fix]\n", refused: `factory.yml: pipelines.quick has the unknown stage "qa_fix"`},
		{name: "unknown stage in the config is refused with the file and key", config: "pipelines:\n  quick: [tests]\n", refused: `config.yml: pipelines.quick has the unknown stage "tests"`},
		{name: "without qa there is nothing for qa_fixes and test_gaps to fix", config: "pipelines:\n  noqa: [implement, qa_fixes, test_gaps]\n", pick: "noqa", want: []string{"requirements", "implement", "handoff"}},
		{name: "custom is reserved for a factory's own list", profile: "pipelines:\n  custom: [plan]\n", refused: "factory.yml: pipelines.custom is reserved"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir, repo := t.TempDir(), t.TempDir()
			if err := os.MkdirAll(filepath.Join(repo, ".claude"), 0o755); err != nil {
				t.Fatal(err)
			}
			os.WriteFile(filepath.Join(dir, "config.yml"), []byte(c.config), 0o644)
			os.WriteFile(ProfilePath(repo), []byte(c.profile), 0o644)
			t.Setenv("FACTORY_CONFIG_DIR", dir)
			t.Setenv("FACTORY_STATE_DIR", t.TempDir())
			cfg, err := LoadConfig()
			var ps Pipelines
			if err == nil {
				ps, err = cfg.PipelinesFor(repo)
			}
			if c.refused != "" {
				if err == nil || !strings.Contains(err.Error(), c.refused) {
					t.Fatalf("got error %v; want one containing %q, so the user can find the bad key", err, c.refused)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			p, err := ps.Find(c.pick)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(p.Stages, c.want) {
				t.Errorf("pipeline %q runs %v; want %v", p.Name, p.Stages, c.want)
			}
		})
	}
}

func TestAgentResolution(t *testing.T) {
	cases := []struct {
		name, config, profile string
		want                  map[string]AgentSetting
		refused               string
	}{
		{name: "a profile effort keeps the config's model", config: "agents:\n  engineer: {model: sonnet, effort: medium}\n", profile: "agents:\n  engineer: {effort: low}\n", want: map[string]AgentSetting{"engineer": {Model: "sonnet", Effort: "low"}, "analyst": DefaultAgents["analyst"]}},
		{name: "unknown model in the profile is refused with the file and key", profile: "agents:\n  engineer: {model: gpt}\n", refused: `factory.yml: agents.engineer.model "gpt" is not a model`},
		{name: "unknown agent in the config is refused with the file and key", config: "agents:\n  coder: {model: opus}\n", refused: "config.yml: agents.coder is not an agent"},
		{name: "qa on another tool is refused with the file and key", profile: "agents:\n  qa: {tool: codex}\n", refused: `factory.yml: agents.qa.tool "codex" is refused`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir, repo := t.TempDir(), t.TempDir()
			if err := os.MkdirAll(filepath.Join(repo, ".claude"), 0o755); err != nil {
				t.Fatal(err)
			}
			os.WriteFile(filepath.Join(dir, "config.yml"), []byte(c.config), 0o644)
			os.WriteFile(ProfilePath(repo), []byte(c.profile), 0o644)
			t.Setenv("FACTORY_CONFIG_DIR", dir)
			t.Setenv("FACTORY_STATE_DIR", t.TempDir())
			cfg, err := LoadConfig()
			var agents map[string]AgentSetting
			if err == nil {
				agents, err = cfg.AgentsFor(repo)
			}
			if c.refused != "" {
				if err == nil || !strings.Contains(err.Error(), c.refused) {
					t.Fatalf("got error %v; want one containing %q, since the Agent tool would fail the spawn halfway through a run", err, c.refused)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			for role, want := range c.want {
				if agents[role] != want {
					t.Errorf("%s resolves to %+v; want %+v", role, agents[role], want)
				}
			}
		})
	}
}

func TestSwitchingAnAgentsToolDropsTheOtherToolsModel(t *testing.T) {
	cfg := &Config{Agents: map[string]AgentSetting{"engineer": {Tool: ToolCodex, Model: "gpt-5.6-sol", Effort: "ultra"}, "reviewer": {Model: "sonnet"}}}
	profile := Profile{Agents: map[string]AgentSetting{"reviewer": {Tool: ToolOpenCode}}}
	agents := ResolveAgents(cfg, profile, map[string]AgentSetting{"engineer": {Tool: ToolClaude}})
	if got, want := agents["engineer"], (AgentSetting{Tool: ToolClaude, Model: "opus", Effort: "xhigh"}); got != want {
		t.Errorf("engineer switched back to claude resolves to %+v; want %+v, its frontmatter defaults, not gpt-5.6-sol and not a bare claude", got, want)
	}
	if got := agents["reviewer"]; got != (AgentSetting{Tool: ToolOpenCode}) {
		t.Errorf("reviewer switched to opencode resolves to %+v; want no model or effort, so the tool's own default applies at launch", got)
	}
	if got := agents["engineer"].Flag("engineer"); got != "engineer=claude/opus:xhigh" {
		t.Errorf("the engineer reaches the supervisor as %q; want the claude tool named, or the supervisor keeps the codex layer's tool", got)
	}
}
