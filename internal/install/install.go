package install

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/afonsodelgado/factory/bundle"
)

type FileState string

const (
	Same    FileState = "same"
	Changed FileState = "changed"
	Missing FileState = "missing"
)

type File struct {
	Source string
	Target string
	State  FileState
	Mode   fs.FileMode
}

type Plan struct {
	ClaudeDir string
	Command   string
	Files     []File
	Hooks     []string
	Stale     []string
}

func (p Plan) Pending() []File {
	var pending []File
	for _, f := range p.Files {
		if f.State != Same {
			pending = append(pending, f)
		}
	}
	return pending
}

func (p Plan) Changes() int {
	return len(p.Pending()) + len(p.Hooks) + len(p.Stale)
}

func ClaudeDir() string {
	if dir := os.Getenv("CLAUDE_CONFIG_DIR"); dir != "" {
		return dir
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude")
}

func HookCommand(binary string) string {
	return quoteIfNeeded(binary) + " hook"
}

func quoteIfNeeded(s string) string {
	if strings.ContainsAny(s, " \t'\"\\$`&;|<>()*?[]#~") {
		return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
	}
	return s
}

func Bundled() ([]File, error) {
	var files []File
	err := fs.WalkDir(bundle.Files, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || p == "hooks.json" {
			return err
		}
		mode := fs.FileMode(0o644)
		if path.Base(path.Dir(p)) == "scripts" {
			mode = 0o755
		}
		files = append(files, File{Source: p, Mode: mode})
		return nil
	})
	return files, err
}

func NewPlan(claudeDir, binary string) (Plan, error) {
	plan := Plan{ClaudeDir: claudeDir, Command: HookCommand(binary)}
	files, err := Bundled()
	if err != nil {
		return plan, err
	}
	for _, f := range files {
		f.Target = filepath.Join(claudeDir, filepath.FromSlash(f.Source))
		want, _ := fs.ReadFile(bundle.Files, f.Source)
		have, err := os.ReadFile(f.Target)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			f.State = Missing
		case err != nil:
			return plan, err
		case bytes.Equal(have, want):
			f.State = Same
		default:
			f.State = Changed
		}
		plan.Files = append(plan.Files, f)
	}
	settings, err := readSettings(filepath.Join(claudeDir, "settings.json"))
	if err != nil {
		return plan, err
	}
	plan.Hooks, plan.Stale, err = missingHooks(settings, plan.Command)
	return plan, err
}

type Result struct {
	Written []string
	Backups []string
}

func Apply(plan Plan, now time.Time) (Result, error) {
	var result Result
	stamp := now.Format("20060102-150405")
	for _, f := range plan.Pending() {
		data, err := fs.ReadFile(bundle.Files, f.Source)
		if err != nil {
			return result, err
		}
		if f.State == Changed {
			backup := backupPath(f.Target, stamp)
			if err := os.Rename(f.Target, backup); err != nil {
				return result, err
			}
			result.Backups = append(result.Backups, backup)
		}
		if err := os.MkdirAll(filepath.Dir(f.Target), 0o755); err != nil {
			return result, err
		}
		if err := os.WriteFile(f.Target, data, f.Mode); err != nil {
			return result, err
		}
		result.Written = append(result.Written, f.Target)
	}
	if len(plan.Hooks) == 0 && len(plan.Stale) == 0 {
		return result, nil
	}
	written, backup, err := MergeHooks(plan.ClaudeDir, plan.Command, stamp)
	if backup != "" {
		result.Backups = append(result.Backups, backup)
	}
	if written != "" {
		result.Written = append(result.Written, written)
	}
	return result, err
}

type group struct {
	Matcher string `json:"matcher,omitempty"`
	Hooks   []struct {
		Type    string `json:"type"`
		Command string `json:"command"`
	} `json:"hooks"`
}

func ours(command string) bool {
	fields := strings.Fields(strings.ReplaceAll(command, "'", ""))
	return len(fields) == 2 && filepath.Base(fields[0]) == "factory-tui" && fields[1] == "hook"
}

func bundledHooks(command string) (*object, error) {
	data, err := fs.ReadFile(bundle.Files, "hooks.json")
	if err != nil {
		return nil, err
	}
	encoded, _ := json.Marshal(command)
	data = bytes.ReplaceAll(data, []byte(`"factory-tui hook"`), encoded)
	return parseObject(data)
}

func eventGroups(hooks *object, event string) ([]json.RawMessage, error) {
	raw, ok := hooks.values[event]
	if !ok {
		return nil, nil
	}
	var groups []json.RawMessage
	err := json.Unmarshal(raw, &groups)
	return groups, err
}

func commandsIn(raw json.RawMessage) []string {
	var g group
	json.Unmarshal(raw, &g)
	var commands []string
	for _, h := range g.Hooks {
		commands = append(commands, h.Command)
	}
	return commands
}

func missingHooks(settings *object, command string) ([]string, []string, error) {
	want, err := bundledHooks(command)
	if err != nil {
		return nil, nil, err
	}
	installed, err := settings.child("hooks")
	if err != nil {
		return nil, nil, err
	}
	var missing, stale []string
	for _, event := range want.keys {
		groups, err := eventGroups(installed, event)
		if err != nil {
			return nil, nil, err
		}
		found := false
		for _, g := range groups {
			for _, c := range commandsIn(g) {
				switch {
				case c == command:
					found = true
				case ours(c):
					stale = append(stale, c)
				}
			}
		}
		if !found {
			missing = append(missing, event)
		}
	}
	return missing, unique(stale), nil
}

func unique(items []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, i := range items {
		if !seen[i] {
			seen[i] = true
			out = append(out, i)
		}
	}
	sort.Strings(out)
	return out
}

func MergeHooks(claudeDir, command, stamp string) (string, string, error) {
	return rewriteHooks(claudeDir, stamp, func(installed *object) error {
		want, err := bundledHooks(command)
		if err != nil {
			return err
		}
		for _, event := range want.keys {
			groups, err := eventGroups(installed, event)
			if err != nil {
				return err
			}
			groups = withoutOurs(groups)
			var add []json.RawMessage
			if err := json.Unmarshal(want.values[event], &add); err != nil {
				return err
			}
			installed.set(event, groups, add...)
		}
		return nil
	})
}

func RemoveHooks(claudeDir, stamp string) (string, string, error) {
	return rewriteHooks(claudeDir, stamp, func(installed *object) error {
		for _, event := range append([]string(nil), installed.keys...) {
			groups, err := eventGroups(installed, event)
			if err != nil {
				return err
			}
			kept := withoutOurs(groups)
			if len(kept) == len(groups) {
				continue
			}
			if len(kept) == 0 {
				installed.remove(event)
				continue
			}
			installed.set(event, kept)
		}
		return nil
	})
}

func withoutOurs(groups []json.RawMessage) []json.RawMessage {
	var kept []json.RawMessage
	for _, g := range groups {
		mine := false
		for _, c := range commandsIn(g) {
			if ours(c) {
				mine = true
			}
		}
		if !mine {
			kept = append(kept, g)
		}
	}
	return kept
}

func rewriteHooks(claudeDir, stamp string, change func(*object) error) (string, string, error) {
	path := filepath.Join(claudeDir, "settings.json")
	settings, err := readSettings(path)
	if err != nil {
		return "", "", err
	}
	hooks, err := settings.child("hooks")
	if err != nil {
		return "", "", err
	}
	if err := change(hooks); err != nil {
		return "", "", err
	}
	hooksJSON, err := hooks.marshal()
	if err != nil {
		return "", "", err
	}
	if !settings.has("hooks") {
		settings.keys = append(settings.keys, "hooks")
	}
	settings.values["hooks"] = hooksJSON
	data, err := settings.marshal()
	if err != nil {
		return "", "", err
	}
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, data, "", "  "); err != nil {
		return "", "", err
	}
	pretty.WriteByte('\n')
	backup := ""
	if old, err := os.ReadFile(path); err == nil {
		if bytes.Equal(old, pretty.Bytes()) {
			return "", "", nil
		}
		backup = backupPath(path, stamp)
		if err := os.WriteFile(backup, old, 0o644); err != nil {
			return "", "", err
		}
	}
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		return "", backup, err
	}
	return path, backup, os.WriteFile(path, pretty.Bytes(), 0o644)
}

func backupPath(path, stamp string) string {
	backup := path + "." + stamp + ".bak"
	for n := 2; exists(backup); n++ {
		backup = fmt.Sprintf("%s.%s-%d.bak", path, stamp, n)
	}
	return backup
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func readSettings(path string) (*object, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return newObject(), nil
	}
	if err != nil {
		return nil, err
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return newObject(), nil
	}
	settings, err := parseObject(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return settings, nil
}
