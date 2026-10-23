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
	Want   []byte
}

type Plan struct {
	Tool        string
	Dir         string
	Command     string
	Files       []File
	HooksFile   string
	HooksSource string
	Hooks       []string
	Stale       []string
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

const openCodePlugin = "opencode/factory.js"

func Bundled(dirs ...string) ([]File, error) {
	var files []File
	for _, dir := range dirs {
		err := fs.WalkDir(bundle.Files, dir, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			mode := fs.FileMode(0o644)
			if path.Base(path.Dir(p)) == "scripts" {
				mode = 0o755
			}
			files = append(files, File{Source: p, Mode: mode})
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return files, nil
}

func NewPlan(claudeDir, binary string) (Plan, error) {
	plan := Plan{Tool: "claude", Dir: claudeDir, Command: HookCommand(binary), HooksFile: filepath.Join(claudeDir, "settings.json"), HooksSource: "hooks.json"}
	files, err := Bundled("skills", "agents")
	if err != nil {
		return plan, err
	}
	return plan.with(files, func(source string) string { return source })
}

func NewCodexPlan(codexDir, binary string) (Plan, error) {
	plan := Plan{Tool: "codex", Dir: codexDir, Command: HookCommand(binary), HooksFile: filepath.Join(codexDir, "hooks.json"), HooksSource: "codex/hooks.json"}
	files, err := Bundled("skills")
	if err != nil {
		return plan, err
	}
	return plan.with(files, func(source string) string { return source })
}

func NewOpenCodePlan(openCodeDir, binary string) (Plan, error) {
	plan := Plan{Tool: "opencode", Dir: openCodeDir, Command: HookCommand(binary)}
	files, err := Bundled(openCodePlugin)
	if err != nil {
		return plan, err
	}
	encoded, _ := json.Marshal(binary)
	for i := range files {
		files[i].Want = bytes.Replace(mustRead(files[i].Source), []byte(`const FACTORY = "factory"`), append([]byte("const FACTORY = "), encoded...), 1)
	}
	return plan.with(files, func(string) string { return "plugins/factory.js" })
}

func mustRead(source string) []byte {
	data, _ := fs.ReadFile(bundle.Files, source)
	return data
}

func (plan Plan) with(files []File, target func(source string) string) (Plan, error) {
	for _, f := range files {
		f.Target = filepath.Join(plan.Dir, filepath.FromSlash(target(f.Source)))
		if f.Want == nil {
			f.Want = mustRead(f.Source)
		}
		have, err := os.ReadFile(f.Target)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			f.State = Missing
		case err != nil:
			return plan, err
		case bytes.Equal(have, f.Want):
			f.State = Same
		default:
			f.State = Changed
		}
		plan.Files = append(plan.Files, f)
	}
	if plan.HooksFile == "" {
		return plan, nil
	}
	settings, err := readSettings(plan.HooksFile)
	if err != nil {
		return plan, err
	}
	plan.Hooks, plan.Stale, err = missingHooks(settings, plan.HooksSource, plan.Command)
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
		if err := os.WriteFile(f.Target, f.Want, f.Mode); err != nil {
			return result, err
		}
		result.Written = append(result.Written, f.Target)
	}
	if len(plan.Hooks) == 0 && len(plan.Stale) == 0 {
		return result, nil
	}
	written, backup, err := mergeHooks(plan.HooksFile, plan.HooksSource, plan.Command, stamp)
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
	binary, ok := hookBinary(command)
	return ok && filepath.Base(binary) == "factory"
}

func hookBinary(command string) (string, bool) {
	rest, ok := strings.CutSuffix(strings.TrimSpace(command), " hook")
	if !ok {
		return "", false
	}
	rest = strings.TrimSpace(rest)
	quoted := len(rest) >= 2 && rest[0] == '\'' && rest[len(rest)-1] == '\''
	if quoted {
		return strings.ReplaceAll(rest[1:len(rest)-1], `'\''`, "'"), true
	}
	if rest == "" || strings.ContainsAny(rest, " \t'\"") {
		return "", false
	}
	return rest, true
}

func bundledHooks(source, command string) (*object, error) {
	data, err := fs.ReadFile(bundle.Files, source)
	if err != nil {
		return nil, err
	}
	encoded, _ := json.Marshal(command)
	data = bytes.ReplaceAll(data, []byte(`"factory hook"`), encoded)
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

func missingHooks(settings *object, source, command string) ([]string, []string, error) {
	want, err := bundledHooks(source, command)
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
	return mergeHooks(filepath.Join(claudeDir, "settings.json"), "hooks.json", command, stamp)
}

func MergeCodexHooks(codexDir, command, stamp string) (string, string, error) {
	return mergeHooks(filepath.Join(codexDir, "hooks.json"), "codex/hooks.json", command, stamp)
}

func mergeHooks(file, source, command, stamp string) (string, string, error) {
	return rewriteHooks(file, stamp, func(installed *object) error {
		want, err := bundledHooks(source, command)
		if err != nil {
			return err
		}
		for _, event := range want.keys {
			groups, err := eventGroups(installed, event)
			if err != nil {
				return err
			}
			groups, _ = withoutOurs(groups)
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
	return removeHooks(filepath.Join(claudeDir, "settings.json"), stamp)
}

func RemoveCodexHooks(codexDir, stamp string) (string, string, error) {
	return removeHooks(filepath.Join(codexDir, "hooks.json"), stamp)
}

func removeHooks(file, stamp string) (string, string, error) {
	if !exists(file) {
		return "", "", nil
	}
	return rewriteHooks(file, stamp, func(installed *object) error {
		for _, event := range append([]string(nil), installed.keys...) {
			groups, err := eventGroups(installed, event)
			if err != nil {
				return err
			}
			kept, changed := withoutOurs(groups)
			if !changed {
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

func withoutOurs(groups []json.RawMessage) ([]json.RawMessage, bool) {
	var kept []json.RawMessage
	changed := false
	for _, g := range groups {
		trimmed, removed := withoutOurHooks(g)
		changed = changed || removed
		if trimmed != nil {
			kept = append(kept, trimmed)
		}
	}
	return kept, changed
}

func withoutOurHooks(raw json.RawMessage) (json.RawMessage, bool) {
	var fields map[string]json.RawMessage
	var hooks []json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || json.Unmarshal(fields["hooks"], &hooks) != nil {
		return raw, false
	}
	var kept []json.RawMessage
	for _, h := range hooks {
		var hook struct {
			Command string `json:"command"`
		}
		json.Unmarshal(h, &hook)
		if !ours(hook.Command) {
			kept = append(kept, h)
		}
	}
	switch {
	case len(kept) == len(hooks):
		return raw, false
	case len(kept) == 0:
		return nil, true
	}
	fields["hooks"], _ = json.Marshal(kept)
	trimmed, err := json.Marshal(fields)
	if err != nil {
		return raw, false
	}
	return trimmed, true
}

func rewriteHooks(path, stamp string, change func(*object) error) (string, string, error) {
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
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
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
