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

func TestRepoSettingsSaveKeepsUnownedConfigAndComments(t *testing.T) {
	for _, tc := range []struct {
		name, input string
		mode, build string
		refuse      bool
		contains    []string
	}{
		{name: "block comments and order", input: "# header\ncommands:\n  # build head\n  build: old # inline\n  migrate: keep\nqa:\n  mode: browser\n  start: original # start\n  health: original-health # health\n  notes: keep notes\nports: [42, 43]\n# footer\n", mode: "terminal", contains: []string{"# header", "# build head", "build: new # inline", "migrate: keep", "start: original # start", "health: original-health # health", "notes: keep notes", "ports: [42, 43]", "# footer"}},
		{name: "flow mapping", input: "commands: {test: old, migrate: keep}\nqa: {notes: keep}\ncoderabbit: true # rabbit\n", mode: "browser", contains: []string{"test: \"\"", "migrate: keep", "notes: keep", "# rabbit"}},
		{name: "flow punctuation in a value", input: "commands: {}\nqa: {mode: terminal}\n", build: "go build -tags=a,b ./... {x} [y]"},
		{name: "flow punctuation under a missing parent", input: "design: {source: terminal}\n", build: "go test -run 'A,B' ./..."},
		{name: "missing parents", input: "design: {source: terminal}\n", contains: []string{"design: {source: terminal}"}},
		{name: "null parents", input: "commands: null # commands\nqa: # qa\n", contains: []string{"# commands", "# qa"}},
		{name: "comment only", input: "# header\n# footer\n", contains: []string{"# header\n# footer"}},
		{name: "empty"},
		{name: "unowned anchors", input: "defaults: &defaults {a: b}\nother: *defaults\n", contains: []string{"&defaults", "*defaults"}},
		{name: "malformed", input: "commands: [\n", refuse: true},
		{name: "duplicate", input: "commands: {build: a, build: b}\n", refuse: true},
		{name: "non mapping", input: "commands: nope\n", refuse: true},
		{name: "shared section", input: "commands: &shared {build: a}\nother: *shared\n", refuse: true},
		{name: "shared scalar", input: "commands: {build: &shared a}\nother: *shared\n", refuse: true},
		{name: "merge", input: "base: &base {build: a}\ncommands: {<<: *base}\n", refuse: true},
		{name: "external change", input: "commands: {build: old}\n"},
		{name: "failed write", input: "commands: {build: old}\n"},
		{name: "symlink and permissions", input: "commands: {build: old}\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := t.TempDir()
			if err := os.MkdirAll(RepoDir(repo), 0o755); err != nil {
				t.Fatal(err)
			}
			path := RepoConfigPath(repo)
			if tc.name == "symlink and permissions" {
				path = filepath.Join(repo, "target.yml")
				if err := os.Symlink(path, RepoConfigPath(repo)); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(path, []byte(tc.input), 0o640); err != nil {
				t.Fatal(err)
			}
			settings, err := ReadRepoSettings(repo)
			if tc.refuse {
				if err == nil {
					t.Fatal("unsafe input accepted")
				}
				data, _ := os.ReadFile(path)
				if string(data) != tc.input {
					t.Fatal("refused input changed")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			values := map[string]string{"commands.build": "new", "commands.lint": "", "commands.test": "", "qa.mode": tc.mode, "qa.start": "new-start", "qa.health": "new-health", "coderabbit": "false"}
			if tc.build != "" {
				values["commands.build"] = tc.build
			}
			if values["qa.mode"] == "" {
				values["qa.mode"] = "terminal"
			}
			original := tc.input
			if tc.name == "external change" {
				original += "external: edit\n"
				os.WriteFile(path, []byte(original), 0o640)
			}
			if tc.name == "failed write" {
				if err := os.Chmod(RepoDir(repo), 0o500); err != nil {
					t.Fatal(err)
				}
				defer os.Chmod(RepoDir(repo), 0o755)
			}
			err = settings.Save(values)
			data, _ := os.ReadFile(path)
			if tc.name == "external change" || tc.name == "failed write" {
				if err == nil {
					t.Fatal("save should refuse")
				}
				if string(data) != original {
					t.Fatal("failed save changed original bytes")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range tc.contains {
				if !strings.Contains(string(data), want) {
					t.Errorf("lost %q in:\n%s", want, data)
				}
			}
			again, err := ReadRepoSettings(repo)
			if err != nil {
				t.Fatal(err)
			}
			for _, key := range settingsKeys(values["qa.mode"]) {
				if !again.Present[key] || again.Values[key] != values[key] {
					t.Errorf("%s = %q, present %v", key, again.Values[key], again.Present[key])
				}
			}
			if tc.name == "block comments and order" {
				if !(strings.Index(string(data), "build:") < strings.Index(string(data), "migrate:") && strings.Index(string(data), "commands:") < strings.Index(string(data), "qa:") && strings.Index(string(data), "qa:") < strings.Index(string(data), "ports:")) {
					t.Fatal("existing keys reordered")
				}
			}
			if tc.name == "symlink and permissions" {
				info, _ := os.Lstat(RepoConfigPath(repo))
				target, _ := os.Stat(path)
				if info.Mode()&os.ModeSymlink == 0 || target.Mode().Perm() != 0o640 {
					t.Fatal("link or permissions changed")
				}
			}
		})
	}
}
