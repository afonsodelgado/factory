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
		{name: "a browser QA repo walks through the app before the handoff, a terminal one never does", profile: "qa:\n  mode: browser\n", pick: "full", want: []string{"requirements", "design", "plan", "implement", "review", "coderabbit", "qa", "qa_fixes", "test_gaps", "walkthrough", "handoff"}},
		{name: "requirements and handoff are always kept", config: "pipelines:\n  bare: [implement]\n", pick: "bare", want: []string{"requirements", "implement", "handoff"}},
		{name: "unknown stage in the profile is refused with the file and key", profile: "pipelines:\n  quick: [requirements, qa_fix]\n", refused: `.factory/config.yml: pipelines.quick has the unknown stage "qa_fix"`},
		{name: "unknown stage in the config is refused with the file and key", config: "pipelines:\n  quick: [tests]\n", refused: `config.yml: pipelines.quick has the unknown stage "tests"`},
		{name: "without qa there is nothing for qa_fixes and test_gaps to fix", config: "pipelines:\n  noqa: [implement, qa_fixes, test_gaps]\n", pick: "noqa", want: []string{"requirements", "implement", "handoff"}},
		{name: "custom is reserved for a factory's own list", profile: "pipelines:\n  custom: [plan]\n", refused: ".factory/config.yml: pipelines.custom is reserved"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir, repo := t.TempDir(), t.TempDir()
			if err := os.MkdirAll(RepoDir(repo), 0o755); err != nil {
				t.Fatal(err)
			}
			os.WriteFile(filepath.Join(dir, "config.yml"), []byte(c.config), 0o644)
			os.WriteFile(RepoConfigPath(repo), []byte(c.profile), 0o644)
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
		{name: "unknown model in the profile is refused with the file and key", profile: "agents:\n  engineer: {model: gpt}\n", refused: `.factory/config.yml: agents.engineer.model "gpt" is not a model`},
		{name: "unknown agent in the config is refused with the file and key", config: "agents:\n  coder: {model: opus}\n", refused: "config.yml: agents.coder is not an agent"},
		{name: "qa on another tool is refused with the file and key", profile: "agents:\n  qa: {tool: codex}\n", refused: `.factory/config.yml: agents.qa.tool "codex" is refused`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir, repo := t.TempDir(), t.TempDir()
			if err := os.MkdirAll(RepoDir(repo), 0o755); err != nil {
				t.Fatal(err)
			}
			os.WriteFile(filepath.Join(dir, "config.yml"), []byte(c.config), 0o644)
			os.WriteFile(RepoConfigPath(repo), []byte(c.profile), 0o644)
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

func TestSwitchingAnAgentsToolBackToClaudeTakesItsFrontmatter(t *testing.T) {
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

func TestARepoConfigOverridesTheMachineDefaultsAndAnOldOneStillLoads(t *testing.T) {
	dir, repo := t.TempDir(), t.TempDir()
	global := "supervisor: {tool: codex, model: gpt-5.6-sol}\nstalled_after: 20m\nclaude: {remote_control: true}\ncodex: {writable_dirs: [/global]}\n"
	os.WriteFile(filepath.Join(dir, "config.yml"), []byte(global), 0o644)
	t.Setenv("FACTORY_CONFIG_DIR", dir)
	t.Setenv("FACTORY_STATE_DIR", t.TempDir())
	os.MkdirAll(filepath.Dir(LegacyProfilePath(repo)), 0o755)
	os.WriteFile(LegacyProfilePath(repo), []byte("supervisor: {effort: low}\nstalled_after: 2h\nclaude: {remote_control: false}\ncodex: {writable_dirs: [/repo]}\n"), 0o644)
	cfg, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	profile, err := ReadProfile(repo)
	if err != nil {
		t.Fatal(err)
	}
	merged := cfg.WithProfile(profile)
	if want := (AgentSetting{Tool: ToolCodex, Model: "gpt-5.6-sol", Effort: "low"}); merged.Supervisor != want {
		t.Errorf("supervisor = %+v; want %+v, the repo's effort over the global tool and model", merged.Supervisor, want)
	}
	if merged.Stalled().String() != "2h0m0s" || merged.RemoteControl() || !slices.Equal(merged.Codex.WritableDirs, []string{"/global", "/repo"}) {
		t.Errorf("stalled %v, remote control %v, writable dirs %v; want the repo's 2h and false, and both dirs", merged.Stalled(), merged.RemoteControl(), merged.Codex.WritableDirs)
	}
	os.MkdirAll(RepoDir(repo), 0o755)
	os.WriteFile(RepoConfigPath(repo), []byte("stalled_after: 45m\n"), 0o644)
	if profile, _ := ReadProfile(repo); profile.StalledAfter != "45m" {
		t.Errorf("with both files present the repo config reads stalled_after %q; want .factory/config.yml's 45m, or edits there are ignored", profile.StalledAfter)
	}
}

func TestRemoteConfigRefusesWhatSSHWouldMisread(t *testing.T) {
	cases := map[string]string{
		"a default naming no remote":     "default_remote: homelab\n",
		"a host that ssh reads as flag":  "remotes:\n  homelab:\n    host: -oProxyCommand=sh\n",
		"a host with shell syntax":       "remotes:\n  homelab:\n    host: \"homelab;rm -rf ~\"\n",
		"a user with a space":            "remotes:\n  homelab:\n    host: homelab\n    user: \"a b\"\n",
		"a port out of range":            "remotes:\n  homelab:\n    host: homelab\n    port: 70000\n",
		"the reserved name local":        "remotes:\n  local:\n    host: homelab\n",
		"a push server that is not http": "push:\n  server: file:///etc\n",
	}
	for name, config := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			os.WriteFile(filepath.Join(dir, "config.yml"), []byte(config), 0o644)
			t.Setenv("FACTORY_CONFIG_DIR", dir)
			t.Setenv("FACTORY_STATE_DIR", t.TempDir())
			if _, err := LoadConfig(); err == nil {
				t.Error("loaded; want it refused before any ssh command is built from it")
			}
		})
	}
}

func TestRemoteSetupKeepsTheRestOfTheConfig(t *testing.T) {
	cases := []struct{ name, original, comment string }{
		{"a config with settings", "# my settings\nrepos:\n  - ~/dev/a # main repo\nremotes:\n  old:\n    host: old\n", "# main repo"},
		{"a config that holds only comments", "# just a comment\n", "# just a comment"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			os.WriteFile(filepath.Join(dir, "config.yml"), []byte(c.original), 0o644)
			err := UpdateMachine(dir, func(m *Machine) error {
				m.Remotes = map[string]Remote{}
				m.Remotes["homelab"] = Remote{Host: "homelab", Port: 2222}
				m.DefaultRemote = "homelab"
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			data, _ := os.ReadFile(filepath.Join(dir, "config.yml"))
			t.Setenv("FACTORY_CONFIG_DIR", dir)
			t.Setenv("FACTORY_STATE_DIR", t.TempDir())
			cfg, err := LoadConfig()
			if err != nil || !strings.Contains(string(data), c.comment) || len(cfg.Remotes) != 1 || cfg.Remotes["homelab"].Port != 2222 || cfg.DefaultRemote != "homelab" {
				t.Fatalf("config after adding a remote:\n%s\nerr %v; want the user's comments kept and only homelab listed", data, err)
			}
			for range 3 {
				if err := UpdateMachine(dir, func(m *Machine) error { return nil }); err != nil {
					t.Fatal(err)
				}
			}
			if again, _ := os.ReadFile(filepath.Join(dir, "config.yml")); string(again) != string(data) {
				t.Fatalf("config after three more writes:\n%s\nwant it unchanged, not indented further on every write:\n%s", again, data)
			}
		})
	}
}
