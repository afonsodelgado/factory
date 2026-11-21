package factory

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func CopyEnvFiles(repo, worktree string) ([]string, error) {
	if worktree == "" || worktree == repo {
		return nil, nil
	}
	out, err := exec.Command("git", "-C", repo, "ls-files", "-z", "--others", "--ignored", "--exclude-standard", "--directory").Output()
	if err != nil {
		return nil, err
	}
	var copied []string
	for _, rel := range strings.Split(string(out), "\x00") {
		if !isEnvFile(rel) {
			continue
		}
		source, target := filepath.Join(repo, rel), filepath.Join(worktree, rel)
		if info, err := os.Lstat(target); err == nil {
			merged, err := appendMissingKeys(source, target, info)
			if err != nil {
				return copied, err
			}
			if merged {
				copied = append(copied, rel)
			}
			continue
		}
		if err := copyFile(source, target); err != nil {
			return copied, err
		}
		copied = append(copied, rel)
	}
	return copied, nil
}

func appendMissingKeys(from, to string, target os.FileInfo) (bool, error) {
	info, err := os.Stat(from)
	if err != nil || !info.Mode().IsRegular() || !target.Mode().IsRegular() {
		return false, err
	}
	source, err := os.ReadFile(from)
	if err != nil {
		return false, err
	}
	existing, err := os.ReadFile(to)
	if err != nil {
		return false, err
	}
	defined := map[string]bool{}
	for _, entry := range envEntries(string(existing)) {
		defined[entry.key] = true
	}
	latest := map[string]string{}
	var order []string
	for _, entry := range envEntries(string(source)) {
		if _, seen := latest[entry.key]; !seen && !defined[entry.key] {
			order = append(order, entry.key)
		}
		latest[entry.key] = entry.text
	}
	var missing []string
	for _, key := range order {
		missing = append(missing, latest[key])
	}
	if len(missing) == 0 {
		return false, nil
	}
	addition := strings.Join(missing, "\n") + "\n"
	if len(existing) > 0 && !strings.HasSuffix(string(existing), "\n") {
		addition = "\n" + addition
	}
	file, err := os.OpenFile(to, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		return false, err
	}
	_, err = file.WriteString(addition)
	return true, errors.Join(err, file.Close())
}

type envEntry struct{ key, text string }

func envEntries(content string) []envEntry {
	var entries []envEntry
	lines := strings.Split(content, "\n")
	for i := 0; i < len(lines); i++ {
		key, value, ok := envAssignment(lines[i])
		if !ok {
			continue
		}
		block := []string{lines[i]}
		if quote, open := openQuote(value); open {
			for i+1 < len(lines) {
				i++
				block = append(block, lines[i])
				if strings.Contains(lines[i], quote) {
					break
				}
			}
		}
		entries = append(entries, envEntry{key: key, text: strings.Join(block, "\n")})
	}
	return entries
}

func envAssignment(line string) (string, string, bool) {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "#") {
		return "", "", false
	}
	key, value, found := strings.Cut(strings.TrimPrefix(line, "export "), "=")
	key = strings.TrimSpace(key)
	return key, strings.TrimSpace(value), found && key != ""
}

func openQuote(value string) (string, bool) {
	if value == "" || (value[0] != '"' && value[0] != '\'') {
		return "", false
	}
	quote := value[:1]
	return quote, !strings.Contains(value[1:], quote)
}

func isEnvFile(rel string) bool {
	name := filepath.Base(rel)
	insideFactoryDirs := strings.HasPrefix(rel, ".claude/")
	return rel != "" && !strings.HasSuffix(rel, "/") && !insideFactoryDirs && (name == ".env" || strings.HasPrefix(name, ".env."))
}

func copyFile(from, to string) error {
	info, err := os.Stat(from)
	if err != nil || !info.Mode().IsRegular() {
		return err
	}
	data, err := os.ReadFile(from)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
		return err
	}
	return os.WriteFile(to, data, info.Mode().Perm())
}
