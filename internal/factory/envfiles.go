package factory

import (
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
		target := filepath.Join(worktree, rel)
		if _, err := os.Lstat(target); err == nil {
			continue
		}
		if err := copyFile(filepath.Join(repo, rel), target); err != nil {
			return copied, err
		}
		copied = append(copied, rel)
	}
	return copied, nil
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
