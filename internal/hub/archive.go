package hub

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type RemovalCheck struct {
	Worktree   string
	Branch     string
	Dirty      []string
	Unpushed   []string
	NoUpstream bool
	Problem    string
}

func (r RemovalCheck) Removable() bool {
	return r.Problem == "" && len(r.Dirty) == 0 && len(r.Unpushed) == 0 && !r.NoUpstream
}

func git(dir string, args ...string) (string, error) {
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	return strings.TrimRight(string(out), "\n"), err
}

func lines(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

func CheckRemoval(worktree string) RemovalCheck {
	check := RemovalCheck{Worktree: worktree}
	status, err := git(worktree, "status", "--porcelain")
	if err != nil {
		check.Problem = "not a git worktree: " + status
		return check
	}
	check.Dirty = lines(status)
	check.Branch, _ = git(worktree, "rev-parse", "--abbrev-ref", "HEAD")
	if _, err := git(worktree, "rev-parse", "--abbrev-ref", "@{upstream}"); err != nil {
		check.NoUpstream = true
		return check
	}
	ahead, err := git(worktree, "log", "--oneline", "@{upstream}..HEAD")
	if err != nil {
		check.Problem = "cannot compare with the upstream: " + ahead
		return check
	}
	check.Unpushed = lines(ahead)
	return check
}

func FactoryDirRescue(repo, worktree, dir string) (string, bool) {
	if dir == "" {
		return "", false
	}
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return "", false
	}
	realWorktree, err := filepath.EvalSymlinks(worktree)
	if err != nil || !strings.HasPrefix(realDir, realWorktree+string(filepath.Separator)) {
		return "", false
	}
	return filepath.Join(repo, ".claude", "factory", filepath.Base(realDir)), true
}

func RemoveWorktree(repo, worktree, factoryDir string) error {
	if check := CheckRemoval(worktree); !check.Removable() {
		return fmt.Errorf("refused: %s has work that is not safe to delete", worktree)
	}
	restore, err := rescueFactoryDir(repo, worktree, factoryDir)
	if err != nil {
		return err
	}
	out, err := git(repo, "worktree", "remove", worktree)
	if err != nil {
		restore()
		return fmt.Errorf("git worktree remove: %s", out)
	}
	return nil
}

func rescueFactoryDir(repo, worktree, dir string) (func(), error) {
	target, inside := FactoryDirRescue(repo, worktree, dir)
	if !inside {
		return func() {}, nil
	}
	if _, err := os.Lstat(target); err == nil {
		return nil, fmt.Errorf("refused: %s already exists, so the factory directory has nowhere to go", target)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return nil, err
	}
	realDir, _ := filepath.EvalSymlinks(dir)
	if err := os.Rename(realDir, target); err != nil {
		return nil, fmt.Errorf("refused: cannot move %s out of the worktree: %w", realDir, err)
	}
	return func() { os.Rename(target, realDir) }, nil
}
