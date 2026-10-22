package install

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/afonsodelgado/factory/bundle"
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
	binary := "/opt/factory-tui/bin/factory-tui"
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
	for _, event := range []string{"SessionStart", "UserPromptSubmit", "PreToolUse", "PermissionRequest", "Notification", "Stop", "StopFailure", "SessionEnd"} {
		ours := 0
		for _, c := range commands[event] {
			if c == binary+" hook" {
				ours++
			}
		}
		if ours != 1 {
			t.Errorf("%s has %d factory-tui hooks, want 1", event, ours)
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
