package hub

import (
	"fmt"
	"os/exec"
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

func RemoveWorktree(repo, worktree string) error {
	if check := CheckRemoval(worktree); !check.Removable() {
		return fmt.Errorf("refused: %s has work that is not safe to delete", worktree)
	}
	out, err := git(repo, "worktree", "remove", worktree)
	if err != nil {
		return fmt.Errorf("git worktree remove: %s", out)
	}
	return nil
}
