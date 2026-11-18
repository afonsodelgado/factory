package trust

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/afonsodelgado/factory/internal/state"
)

func Managed(paths ...string) error {
	return errors.Join(Claude(ClaudeConfigDir(), paths...), Codex(state.CodexHome(), paths...))
}

func ClaudeConfigDir() string {
	if dir := os.Getenv("CLAUDE_CONFIG_DIR"); dir != "" {
		return dir
	}
	home, _ := os.UserHomeDir()
	return home
}

func Claude(configDir string, paths ...string) error {
	return update(filepath.Join(configDir, ".claude.json"), func(data []byte) ([]byte, error) {
		config := map[string]json.RawMessage{}
		if len(bytes.TrimSpace(data)) > 0 {
			if err := json.Unmarshal(data, &config); err != nil {
				return nil, err
			}
		}
		var projects map[string]map[string]json.RawMessage
		if raw, ok := config["projects"]; ok {
			if err := json.Unmarshal(raw, &projects); err != nil {
				return nil, err
			}
		}
		if projects == nil {
			projects = map[string]map[string]json.RawMessage{}
		}
		changed := false
		for _, path := range forms(paths) {
			if string(projects[path]["hasTrustDialogAccepted"]) == "true" {
				continue
			}
			if projects[path] == nil {
				projects[path] = map[string]json.RawMessage{}
			}
			projects[path]["hasTrustDialogAccepted"] = json.RawMessage("true")
			changed = true
		}
		if !changed {
			return data, nil
		}
		var err error
		if config["projects"], err = json.Marshal(projects); err != nil {
			return nil, err
		}
		return json.MarshalIndent(config, "", "  ")
	})
}

func Codex(home string, paths ...string) error {
	return update(filepath.Join(home, "config.toml"), func(data []byte) ([]byte, error) {
		text := string(data)
		for _, path := range forms(paths) {
			text = trustCodexProject(text, path)
		}
		return []byte(text), nil
	})
}

var (
	tableHeader = regexp.MustCompile(`(?m)^[ \t]*\[`)
	trustLevel  = regexp.MustCompile(`(?m)^[ \t]*trust_level[ \t]*=.*$`)
)

const trusted = `trust_level = "trusted"`

func CodexHeaders(path string) []string {
	headers := []string{"[projects." + basicString(path) + "]"}
	literalAllowed := !strings.ContainsFunc(path, func(r rune) bool { return r == '\'' || r < 0x20 || r == 0x7f })
	if literalAllowed {
		headers = append(headers, "[projects.'"+path+"']")
	}
	return headers
}

func basicString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '"' || r == '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case r < 0x20 || r == 0x7f:
			fmt.Fprintf(&b, `\u%04X`, r)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

func trustCodexProject(text, path string) string {
	header, start := "", -1
	for _, candidate := range CodexHeaders(path) {
		if i := strings.Index(text, candidate+"\n"); i >= 0 {
			header, start = candidate+"\n", i
			break
		}
	}
	if start < 0 {
		return separated(text) + CodexHeaders(path)[0] + "\n" + trusted + "\n"
	}
	bodyStart := start + len(header)
	rest := text[bodyStart:]
	end := len(rest)
	if next := tableHeader.FindStringIndex(rest); next != nil {
		end = next[0]
	}
	body := rest[:end]
	if trustLevel.MatchString(body) {
		body = trustLevel.ReplaceAllLiteralString(body, trusted)
	} else {
		body = trusted + "\n" + body
	}
	return text[:bodyStart] + body + rest[end:]
}

func separated(text string) string {
	if text == "" {
		return ""
	}
	return strings.TrimRight(text, "\n") + "\n\n"
}

func forms(paths []string) []string {
	var all []string
	for _, path := range paths {
		if !filepath.IsAbs(path) {
			continue
		}
		clean := filepath.Clean(path)
		for _, form := range []string{clean, physical(clean)} {
			if !slices.Contains(all, form) {
				all = append(all, form)
			}
		}
	}
	return all
}

func physical(path string) string {
	if real, err := filepath.EvalSymlinks(path); err == nil {
		return real
	}
	parent := filepath.Dir(path)
	if parent == path {
		return path
	}
	return filepath.Join(physical(parent), filepath.Base(path))
}

func update(path string, change func([]byte) ([]byte, error)) error {
	if real, err := filepath.EvalSymlinks(path); err == nil {
		path = real
	}
	for range 2 {
		before, err := os.ReadFile(path)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		after, err := change(before)
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		if bytes.Equal(after, before) {
			return nil
		}
		if current, _ := os.ReadFile(path); bytes.Equal(current, before) {
			return state.WriteFileAtomic(path, after)
		}
	}
	return fmt.Errorf("%s kept changing while factory recorded folder trust, so it was left as it is", path)
}
