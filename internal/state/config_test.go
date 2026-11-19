package state

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/goccy/go-yaml"
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
		{name: "built-in quick includes independent review and publication", pick: "quick", want: []string{"requirements", "plan", "implement", "review", "pr", "handoff"}},
		{name: "global config overrides a built-in", config: "pipelines:\n  quick: [plan, implement, review, qa, pr]\n", pick: "quick", want: []string{"requirements", "plan", "implement", "review", "qa", "pr", "handoff"}},
		{name: "profile wins over the global config", config: "pipelines:\n  tiny: [plan, implement, review, qa, pr]\ndefault_pipeline: quick\n", profile: "pipelines:\n  tiny: [plan, implement, review, pr]\ndefault_pipeline: tiny\n", want: []string{"requirements", "plan", "implement", "review", "pr", "handoff"}},
		{name: "stale vendor setting is ignored", profile: "coderabbit: false\n", pick: "full", want: StageKeys},
		{name: "browser and terminal use the same stages", profile: "qa:\n  mode: browser\n", pick: "full", want: StageKeys},
		{name: "requirements and handoff are always kept", config: "pipelines:\n  bare: [plan, implement, review, pr]\n", pick: "bare", want: []string{"requirements", "plan", "implement", "review", "pr", "handoff"}},
		{name: "unknown stage in profile is refused", profile: "pipelines:\n  quick: [qa_fixes]\n", refused: `.factory/config.yml: pipelines.quick has the unknown stage "qa_fixes"`},
		{name: "unknown stage in config is refused", config: "pipelines:\n  quick: [coderabbit]\n", refused: `config.yml: pipelines.quick has the unknown stage "coderabbit"`},
		{name: "publication cannot be omitted", config: "pipelines:\n  incomplete: [plan, implement, review]\n", refused: "must include pr"},
		{name: "custom is reserved", profile: "pipelines:\n  custom: [plan]\n", refused: ".factory/config.yml: pipelines.custom is reserved"},
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
	if merged.Stalled().String() != "2h0m0s" || !slices.Equal(merged.Codex.WritableDirs, []string{"/global", "/repo"}) {
		t.Errorf("stalled %v, writable dirs %v; want the repo's 2h and both dirs", merged.Stalled(), merged.Codex.WritableDirs)
	}
	os.MkdirAll(RepoDir(repo), 0o755)
	os.WriteFile(RepoConfigPath(repo), []byte("stalled_after: 45m\n"), 0o644)
	if profile, _ := ReadProfile(repo); profile.StalledAfter != "45m" {
		t.Errorf("with both files present the repo config reads stalled_after %q; want .factory/config.yml's 45m, or edits there are ignored", profile.StalledAfter)
	}
}

func TestMachineSettingsKeepTheRestOfTheConfig(t *testing.T) {
	phone := Phone{Enabled: true, Port: 7419, Hostname: "devbox.tail1234.ts.net", VAPIDPublicKey: "BPublic"}
	cases := []struct{ name, original, comment string }{
		{"a config with settings", "# my settings\nrepos:\n  - ~/dev/a # main repo\n", "# main repo"},
		{"a config that holds only comments", "# just a comment\n", "# just a comment"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			os.WriteFile(filepath.Join(dir, "config.yml"), []byte(c.original), 0o644)
			err := UpdateMachine(dir, func(m *Machine) error {
				m.Phone = phone
				m.Node.KeepAwake = sleeps
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			data, _ := os.ReadFile(filepath.Join(dir, "config.yml"))
			t.Setenv("FACTORY_CONFIG_DIR", dir)
			t.Setenv("FACTORY_STATE_DIR", t.TempDir())
			cfg, err := LoadConfig()
			if err != nil || !strings.Contains(string(data), c.comment) || cfg.Phone != phone || cfg.KeepsAwake() {
				t.Fatalf("config after phone and keep-awake settings:\n%s\nerr %v; want the user's comments kept and both settings read back", data, err)
			}
			if info, _ := os.Stat(filepath.Join(dir, "config.yml")); info.Mode().Perm() != 0o600 {
				t.Fatalf("config.yml mode %v; want 0600 because it now names the machine's tailnet", info.Mode().Perm())
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

func TestOldRemotesAreListedOnceForNodeEnrollmentAndTheirKeysDropped(t *testing.T) {
	dir := t.TempDir()
	original := "# my settings\nrepos:\n  - ~/dev/a # main repo\nremotes:\n  homelab:\n    host: afonso@homelab.lan\n  build:\n    host: 10.0.0.7\n    port: 2222\ndefault_remote: homelab\npush:\n  topic: factory-q7rk2m9x\nnode:\n  port: 7419\n"
	os.WriteFile(filepath.Join(dir, "config.yml"), []byte(original), 0o644)
	listed, err := DropLegacyRemotes(dir)
	if err != nil || !slices.Equal(listed, []string{"build (10.0.0.7)", "homelab (afonso@homelab.lan)"}) {
		t.Fatalf("listed %q, %v; want each old remote with its ssh host as written, never a guessed tailnet name", listed, err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "config.yml"))
	for _, gone := range []string{"remotes", "default_remote", "push", "factory-q7rk2m9x"} {
		if strings.Contains(string(data), gone) {
			t.Fatalf("config after the migration:\n%s\nstill holds %s", data, gone)
		}
	}
	if !strings.Contains(string(data), "# main repo") || !strings.Contains(string(data), "port: 7419") {
		t.Fatalf("config after the migration:\n%s\nwant the user's comments and node settings kept", data)
	}
	if again, err := DropLegacyRemotes(dir); err != nil || len(again) > 0 {
		t.Fatalf("a second run listed %q, %v; want the remotes listed only once", again, err)
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
		{name: "editing a block scalar keeps newlines", input: "commands:\n  build: |\n    make deps\n    make all\n", build: "make deps\nmake all\n -j4"},
		{name: "repeated CRLF saves preserve block scalars and comments", input: "# config\r\ncommands:\r\n  build: old # build\r\ndesign:\r\n  brief: |\r\n    line one\r\n    line two\r\n"},
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
			for _, key := range SettingsKeys(values["qa.mode"]) {
				if !again.Present[key] || again.Values[key] != values[key] {
					t.Errorf("%s = %q, present %v", key, again.Values[key], again.Present[key])
				}
			}
			if tc.name == "repeated CRLF saves preserve block scalars and comments" {
				for save := range 2 {
					text := string(data)
					if strings.Contains(text, "\r\r") || strings.Contains(text, "\r\n\r\n") || strings.Contains(strings.ReplaceAll(text, "\r\n", ""), "\n") {
						t.Errorf("save %d changed line endings or added blank lines: %q", save+1, text)
					}
					file, err := parseRepoSettings(data)
					if err != nil {
						t.Fatal(err)
					}
					var brief string
					if err := yaml.NodeToValue(settingsNode(file, "design.brief"), &brief); err != nil || brief != "line one\nline two\n" {
						t.Errorf("save %d changed brief: %q, %v", save+1, brief, err)
					}
					if save == 0 {
						values["commands.build"] += " again"
						if err := again.Save(values); err != nil {
							t.Fatal(err)
						}
						data, err = os.ReadFile(path)
						if err != nil {
							t.Fatal(err)
						}
					}
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

func TestRepoSettingsIgnoresAStaleVendorKey(t *testing.T) {
	repo := t.TempDir()
	if err := os.MkdirAll(RepoDir(repo), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(RepoConfigPath(repo), []byte("coderabbit:\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	settings, err := ReadRepoSettings(repo)
	if err != nil {
		t.Fatal(err)
	}
	if settings.Values["coderabbit"] != "" {
		t.Errorf("coderabbit = %q; the retired setting must not appear in the form", settings.Values["coderabbit"])
	}
}

func TestExpandHomeCleansTrailingSlashForRepoMatching(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if got, want := ExpandHome("~/dev/app/"), filepath.Join(home, "dev", "app"); got != want {
		t.Fatalf("expanded repo = %q, want clean path %q", got, want)
	}
}

func TestSharedSettingsMoveBetweenConfigsWithoutTouchingMachineKeysOrComments(t *testing.T) {
	from, to := t.TempDir(), t.TempDir()
	os.WriteFile(filepath.Join(from, "config.yml"), []byte("# laptop\nicons: unicode\nagents:\n  reviewer: {tool: codex, model: gpt-5.5, effort: high}\nstalled_after: 45m\nnode:\n  port: 7418\n"), 0o644)
	os.WriteFile(filepath.Join(to, "config.yml"), []byte("# homebox, keep me\nreview:\n  browsers: 1\nnode:\n  owner: alice@example.com\nstalled_after: 20m\n"), 0o644)

	shared, err := ReadShared(from)
	if err != nil || !strings.Contains(shared, "reviewer") || strings.Contains(shared, "icons") || strings.Contains(shared, "port") {
		t.Fatalf("shared settings of the laptop = %q, %v; want agents and stalled_after only", shared, err)
	}
	written, err := WriteShared(to, shared)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(to, "config.yml"))
	for _, kept := range []string{"# homebox, keep me", "browsers: 1", "owner: alice@example.com", "45m", "gpt-5.5"} {
		if !strings.Contains(string(data), kept) {
			t.Errorf("after syncing, homebox's config.yml lacks %q:\n%s", kept, data)
		}
	}
	if written != shared {
		t.Errorf("homebox reads back %q after writing %q, so the next sync tick would see a local edit and send it back", written, shared)
	}
}

func TestDiscoveryRecordsOnlyBlankKeysWithItsNameAndNeverChangesYours(t *testing.T) {
	repo := t.TempDir()
	os.MkdirAll(RepoDir(repo), 0o755)
	os.WriteFile(RepoConfigPath(repo), []byte("# header\ncommands:\n  build: make # yours\n  lint: \"\" # golangci once it is installed\nqa:\n  mode: \"\"\npipelines: {quick: [plan]}\n"), 0o644)
	day := time.Date(2026, 11, 19, 0, 0, 0, 0, time.UTC)
	found := Found{Factory: "csv", Day: day}

	var verified map[string]string
	verify := func(effective map[string]string, _ []string) error { verified = effective; return nil }
	got, err := RecordDiscovered(repo, "csv", day, map[string]string{"commands.build": "go build ./...", "commands.lint": "go vet ./...", "qa.mode": "terminal"}, []string{"commands.build"}, verify)
	if err != nil || !slices.Equal(got.Recorded, []string{"commands.lint", "qa.mode"}) || !slices.Equal(got.Kept, []string{"commands.build"}) {
		t.Fatalf("recording gave %+v, %v; want only the blank keys written and the typed build kept", got, err)
	}
	if verified["commands.build"] != "make" || verified["commands.lint"] != "go vet ./..." {
		t.Errorf("verification saw %v; want the kept build and the new lint together, as they will be saved", verified)
	}
	data, _ := os.ReadFile(RepoConfigPath(repo))
	for _, want := range []string{"# header", "build: make # yours", "lint: go vet ./... # golangci once it is installed · found by csv, 2026-11-19", "mode: terminal # found by csv, 2026-11-19", "pipelines: {quick: [plan]}"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("the config lost %q:\n%s", want, data)
		}
	}
	if s, _ := ReadRepoSettings(repo); s.Provenance["commands.lint"] != found || s.Discovered("commands.build") {
		t.Errorf("provenance reads %+v; want lint found by csv and build the developer's", s.Provenance)
	}

	if again, _ := RecordDiscovered(repo, "pdf", day, map[string]string{"commands.lint": "golangci-lint run"}, nil, nil); len(again.Recorded) > 0 {
		t.Errorf("a second discovery without --replace wrote %v over csv's value", again.Recorded)
	}
	if replaced, _ := RecordDiscovered(repo, "pdf", day, map[string]string{"commands.lint": "golangci-lint run"}, []string{"commands.lint"}, nil); !slices.Equal(replaced.Recorded, []string{"commands.lint"}) {
		t.Errorf("replacing discovery's own value recorded %v", replaced.Recorded)
	}

	s, _ := ReadRepoSettings(repo)
	values := maps.Clone(s.Values)
	values["commands.lint"] = "make lint"
	if err := s.Save(values); err != nil {
		t.Fatal(err)
	}
	if mine, _ := ReadRepoSettings(repo); mine.Discovered("commands.lint") || !mine.Discovered("qa.mode") {
		t.Errorf("after an edit provenance reads %+v; want lint the developer's and qa.mode still discovered", mine.Provenance)
	}
	if _, err := RecordYours(repo, map[string]string{"commands.build": "make all"}); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(RepoConfigPath(repo)); !strings.Contains(string(data), "lint: make lint # golangci once it is installed\n") || !strings.Contains(string(data), "build: make all # yours\n") {
		t.Errorf("edits and typed commands left:\n%s\nwant the developer's own comments kept and only the found-by note gone", data)
	}
	if after, _ := RecordDiscovered(repo, "pdf", day, map[string]string{"commands.lint": "go vet ./..."}, []string{"commands.lint"}, nil); len(after.Recorded) > 0 {
		t.Errorf("discovery replaced a value the developer edited: %+v", after)
	}

	var wg sync.WaitGroup
	for _, key := range []string{"commands.test", "qa.start"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			RecordDiscovered(repo, "csv", day, map[string]string{key: key + " found"}, nil, nil)
		}()
	}
	wg.Wait()
	if s, _ := ReadRepoSettings(repo); s.Values["commands.test"] != "commands.test found" || s.Values["qa.start"] != "qa.start found" || s.Values["commands.lint"] != "make lint" {
		t.Errorf("two factories recording at once left %v; want both blank keys written and nothing else changed", s.Values)
	}
}

func TestStackVerificationRunsWithoutTheConfigLockAndRepeatsWhenTheConfigChanged(t *testing.T) {
	repo := t.TempDir()
	os.MkdirAll(RepoDir(repo), 0o755)
	os.WriteFile(RepoConfigPath(repo), []byte("commands:\n  build: \"\"\nqa:\n  mode: browser\n"), 0o644)
	day := time.Date(2026, 11, 19, 0, 0, 0, 0, time.UTC)
	verifications := 0
	verify := func(effective map[string]string, _ []string) error {
		verifications++
		if verifications > 1 {
			return nil
		}
		saved := make(chan error, 1)
		go func() {
			_, err := RecordYours(repo, map[string]string{"commands.build": "make"})
			saved <- err
		}()
		select {
		case err := <-saved:
			return err
		case <-time.After(5 * time.Second):
			t.Fatal("saving the config waited on discovery's stack check; the lock must not be held while the stack starts")
		}
		return nil
	}
	got, err := RecordDiscovered(repo, "csv", day, map[string]string{"qa.start": "pnpm dev"}, nil, verify)
	if err != nil || verifications != 2 || !slices.Equal(got.Recorded, []string{"qa.start"}) {
		t.Fatalf("recording during a concurrent edit gave %+v, %v after %d verifications; want it verified again with the new config, then saved", got, err, verifications)
	}
	if s, _ := ReadRepoSettings(repo); s.Values["commands.build"] != "make" || s.Values["qa.start"] != "pnpm dev" {
		t.Errorf("the config holds %v; want both the developer's edit and the verified start", s.Values)
	}
}

func TestRecordGivesUpWithoutAVerificationItWouldThrowAway(t *testing.T) {
	repo := t.TempDir()
	os.MkdirAll(RepoDir(repo), 0o755)
	os.WriteFile(RepoConfigPath(repo), []byte("commands:\n  build: \"\"\nqa:\n  mode: browser\n"), 0o644)
	verifications := 0
	verify := func(map[string]string, []string) error {
		verifications++
		_, err := RecordYours(repo, map[string]string{"commands.build": fmt.Sprint("make ", verifications)})
		return err
	}
	_, err := RecordDiscovered(repo, "csv", time.Now(), map[string]string{"qa.start": "pnpm dev"}, nil, verify)
	if err == nil || verifications != verifyAttempts-1 {
		t.Fatalf("a config that changed during every verification gave %v after %d verifications; want a refusal after %d, never a stack check whose result is thrown away", err, verifications, verifyAttempts-1)
	}
}
