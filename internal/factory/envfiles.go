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
	for _, line := range strings.Split(string(existing), "\n") {
		if key, ok := envKey(line); ok {
			defined[key] = true
		}
	}
	var missing []string
	for _, line := range strings.Split(string(source), "\n") {
		if key, ok := envKey(line); ok && !defined[key] {
			defined[key] = true
			missing = append(missing, line)
		}
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

func envKey(line string) (string, bool) {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "#") {
		return "", false
	}
	key, _, found := strings.Cut(strings.TrimPrefix(line, "export "), "=")
	key = strings.TrimSpace(key)
	return key, found && key != ""
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
