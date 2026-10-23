package install

import (
	"encoding/json"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/afonsodelgado/factory/bundle"
	"github.com/afonsodelgado/factory/internal/state"
)

const vaultSettings = `{
  "model": "opus",
  "hooks": {
    "UserPromptSubmit": [{"hooks": [{"type": "command", "command": "bash ~/.claude/hooks/vault-winddown.sh"}]}],
    "SessionStart": [{"hooks": [{"type": "command", "command": "bash ~/.claude/hooks/vault-session-start.sh"}]}],
    "SessionEnd": [{"hooks": [{"type": "command", "command": "bash ~/.claude/hooks/vault-session-end.sh"}]}]
  }
}
`

func hookCommands(t *testing.T, dir string) map[string][]string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	var settings struct {
		Model string                       `json:"model"`
		Hooks map[string][]json.RawMessage `json:"hooks"`
	}
	if err := json.Unmarshal(data, &settings); err != nil {
		t.Fatal(err)
	}
	if settings.Model != "opus" {
		t.Errorf("an unrelated setting was lost: model = %q", settings.Model)
	}
	commands := map[string][]string{}
	for event, groups := range settings.Hooks {
		for _, g := range groups {
			commands[event] = append(commands[event], commandsIn(g)...)
		}
	}
	return commands
}

func TestBootstrapBacksUpKeepsExistingHooksAndIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	binary := "/opt/factory/bin/factory"
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(vaultSettings), 0o644); err != nil {
		t.Fatal(err)
	}
	skill := filepath.Join(dir, "skills", "factory", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(skill), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(skill, []byte("my local edits"), 0o644); err != nil {
		t.Fatal(err)
	}

	plan, err := NewPlan(dir, binary)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(plan, time.Date(2026, 10, 22, 18, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}

	backups, _ := filepath.Glob(skill + ".*.bak")
	if len(backups) != 1 {
		t.Fatalf("backups of the changed SKILL.md = %v, want one", backups)
	}
	if data, _ := os.ReadFile(backups[0]); string(data) != "my local edits" {
		t.Errorf("the backup holds %q, not the user's edits", data)
	}
	want, _ := fs.ReadFile(bundle.Files, "skills/factory/SKILL.md")
	if got, _ := os.ReadFile(skill); string(got) != string(want) {
		t.Error("SKILL.md is not the bundled one after bootstrap")
	}
	script, err := os.Stat(filepath.Join(dir, "skills", "factory", "scripts", "wait-for-review.sh"))
	if err != nil || script.Mode().Perm()&0o100 == 0 {
		t.Errorf("wait-for-review.sh is not executable after bootstrap: %v %v", script, err)
	}

	commands := hookCommands(t, dir)
	for _, vault := range []string{"UserPromptSubmit", "SessionStart", "SessionEnd"} {
		if !strings.Contains(strings.Join(commands[vault], " "), "vault-") {
			t.Errorf("the vault %s hook was removed: %v", vault, commands[vault])
		}
	}
	for _, event := range []string{"SessionStart", "UserPromptSubmit", "PreToolUse", "PostToolUse", "PostToolUseFailure", "PermissionRequest", "Notification", "Stop", "StopFailure", "SessionEnd"} {
		ours := 0
		for _, c := range commands[event] {
			if c == binary+" hook" {
				ours++
			}
		}
		if ours != 1 {
			t.Errorf("%s has %d factory hooks, want 1", event, ours)
		}
	}

	again, err := NewPlan(dir, binary)
	if err != nil {
		t.Fatal(err)
	}
	if n := again.Changes(); n != 0 {
		t.Errorf("a second bootstrap would make %d changes, want none: %v %v", n, again.Pending(), again.Hooks)
	}
	result, err := Apply(again, time.Date(2026, 10, 22, 19, 0, 0, 0, time.UTC))
	if err != nil || len(result.Written) != 0 || len(result.Backups) != 0 {
		t.Errorf("second bootstrap wrote %v and backed up %v", result.Written, result.Backups)
	}
}

func TestHooksFromASpacedBinaryPathAreReplacedAndRemovedWithoutTouchingSharedGroups(t *testing.T) {
	dir := t.TempDir()
	settings := `{"model": "opus", "hooks": {"SessionStart": [{"hooks": [
		{"type": "command", "command": "bash ~/.claude/hooks/vault-session-start.sh"},
		{"type": "command", "command": "'/Old Apps/factory' hook"}
	]}]}}`
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(settings), 0o644); err != nil {
		t.Fatal(err)
	}
	command := HookCommand("/My Apps/factory")
	for range 2 {
		if _, _, err := MergeHooks(dir, command, "20261022T180000"); err != nil {
			t.Fatal(err)
		}
	}
	for event, commands := range hookCommands(t, dir) {
		ours := 0
		for _, c := range commands {
			if strings.Contains(c, "factory") {
				ours++
			}
		}
		if ours != 1 {
			t.Errorf("%s has %d factory hooks after two installs, want 1: %v", event, ours, commands)
		}
	}

	if _, _, err := RemoveHooks(dir, "20261022T190000"); err != nil {
		t.Fatal(err)
	}
	commands := hookCommands(t, dir)
	if strings.Contains(strings.Join(commands["SessionStart"], " "), "factory") {
		t.Errorf("install-hooks --remove left a factory hook: %v", commands["SessionStart"])
	}
	if len(commands) != 1 || len(commands["SessionStart"]) != 1 {
		t.Errorf("removal took the vault hook that shared a group with ours: %v", commands)
	}
}

func TestCodexAndOpenCodeInstallsKeepTheUsersHooksAndCallThisBinary(t *testing.T) {
	codex, opencode := t.TempDir(), t.TempDir()
	binary := "/My Apps/factory"
	mine := `{"description": "mine", "hooks": {"Stop": [{"hooks": [{"type": "command", "command": "notify.sh"}]}]}}`
	if err := os.WriteFile(filepath.Join(codex, "hooks.json"), []byte(mine), 0o644); err != nil {
		t.Fatal(err)
	}
	codexPlan, err := NewCodexPlan(codex, binary)
	if err != nil {
		t.Fatal(err)
	}
	openCodePlan, err := NewOpenCodePlan(opencode, binary)
	if err != nil {
		t.Fatal(err)
	}
	for _, plan := range []Plan{codexPlan, openCodePlan} {
		if _, err := Apply(plan, time.Now()); err != nil {
			t.Fatal(err)
		}
	}

	stop := codexHookCommands(t, codex)["Stop"]
	if !slices.Contains(stop, "notify.sh") || !slices.Contains(stop, HookCommand(binary)) {
		t.Errorf("Codex Stop hooks are %v; want the user's notify.sh kept beside %q", stop, HookCommand(binary))
	}
	if _, err := os.Stat(filepath.Join(codex, "skills", "factory", "SKILL.md")); err != nil {
		t.Errorf("Codex cannot load the factory skill: %v", err)
	}
	plugin, err := os.ReadFile(filepath.Join(opencode, "plugins", "factory.js"))
	if err != nil || !strings.Contains(string(plugin), `const FACTORY = "/My Apps/factory"`) {
		t.Errorf("the OpenCode plugin does not call this binary, so OpenCode sessions report no status: %v", err)
	}

	if _, _, err := RemoveCodexHooks(codex, "20261023T190000"); err != nil {
		t.Fatal(err)
	}
	if stop := codexHookCommands(t, codex)["Stop"]; !slices.Equal(stop, []string{"notify.sh"}) {
		t.Errorf("after removal Codex Stop hooks are %v; want only the user's notify.sh", stop)
	}
}

func codexHookCommands(t *testing.T, dir string) map[string][]string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "hooks.json"))
	if err != nil {
		t.Fatal(err)
	}
	var file struct {
		Description string                       `json:"description"`
		Hooks       map[string][]json.RawMessage `json:"hooks"`
	}
	if err := json.Unmarshal(data, &file); err != nil {
		t.Fatal(err)
	}
	if file.Description != "mine" {
		t.Errorf("the user's description was lost: %q", file.Description)
	}
	commands := map[string][]string{}
	for event, groups := range file.Hooks {
		for _, g := range groups {
			commands[event] = append(commands[event], commandsIn(g)...)
		}
	}
	return commands
}

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.com"}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func TestInitWritesOnceFromAnywhereInTheRepoAndKeepsTheConfigsComments(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repo := filepath.Join(home, "dev", "svc")
	subdir := filepath.Join(repo, "internal", "api")
	if err := os.MkdirAll(subdir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "go.mod"), []byte("module svc\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "init", "-q")
	git(t, repo, "add", "go.mod")
	git(t, repo, "commit", "-q", "-m", "init")
	configFile := filepath.Join(t.TempDir(), "config.yml")
	if err := os.WriteFile(configFile, []byte("# my repos\nrepos: [~/dev/other] # keep me\nicons: unicode\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	first, err := Init(subdir, configFile)
	if err != nil {
		t.Fatal(err)
	}
	if !first.ProfileWritten || len(first.Excluded) != len(IgnoredPaths) || !first.ConfigAdded {
		t.Fatalf("first init from a subdirectory = %+v; want the profile, the exclude lines and the config entry written for the repo", first)
	}
	if _, err := os.Stat(state.ProfilePath(repo)); err != nil {
		t.Fatalf("init from a subdirectory wrote the profile in the wrong place: %v", err)
	}
	profile, err := state.ReadProfile(repo)
	if err != nil || profile.Commands.Build != "go build ./..." || profile.QA.Mode != "terminal" || profile.CodeRabbit == nil || *profile.CodeRabbit {
		t.Errorf("profile for a go.mod repo reads as %+v, %v; want go commands, terminal QA and CodeRabbit off", profile, err)
	}
	if err := os.WriteFile(state.RepoConfigPath(repo), []byte("edited\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	worktree := filepath.Join(repo, ".claude", "worktrees", "x")
	git(t, repo, "worktree", "add", "-q", worktree)
	second, err := Init(worktree, configFile)
	if err != nil {
		t.Fatal(err)
	}
	if second.Changed() {
		t.Errorf("second init from a worktree changed %+v; want nothing changed", second)
	}
	if profile, _ := os.ReadFile(state.RepoConfigPath(repo)); string(profile) != "edited\n" {
		t.Errorf("second init overwrote the edited profile with:\n%s", profile)
	}
	exclude, _ := os.ReadFile(filepath.Join(repo, ".git", "info", "exclude"))
	if n := strings.Count(string(exclude), ".claude/worktrees/"); n != 1 {
		t.Errorf("exclude lists .claude/worktrees/ %d times:\n%s", n, exclude)
	}
	config, _ := os.ReadFile(configFile)
	if want := "# my repos\nrepos: [~/dev/other, ~/dev/svc] # keep me\nicons: unicode\n"; string(config) != want {
		t.Errorf("config =\n%s\nwant\n%s", config, want)
	}
	if out, _ := exec.Command("git", "-C", repo, "status", "--porcelain").Output(); len(out) != 0 {
		t.Errorf("init left the repo dirty, so the factory's clean-tree preflight fails:\n%s", out)
	}
}

func TestInitCarriesARepoConfigFromBeforeTheFactoryDirectory(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := t.TempDir()
	git(t, repo, "init", "-q")
	os.MkdirAll(filepath.Dir(state.LegacyProfilePath(repo)), 0o755)
	os.WriteFile(state.LegacyProfilePath(repo), []byte("commands: {build: make build}\nqa: {mode: browser}\n"), 0o644)

	r, err := Init(repo, filepath.Join(t.TempDir(), "config.yml"))
	if err != nil {
		t.Fatal(err)
	}
	profile, _ := state.ReadProfile(repo)
	if r.CopiedFrom == "" || profile.Commands.Build != "make build" || !profile.BrowserQA() {
		t.Errorf("init over .claude/factory.yml gave %+v and reads %+v; want it copied, or a generic config replaces the repo's commands and QA mode", r, profile)
	}
	if _, err := os.Stat(state.LegacyProfilePath(repo)); err != nil {
		t.Errorf("init removed .claude/factory.yml (%v); factories already running there still read it", err)
	}
}
