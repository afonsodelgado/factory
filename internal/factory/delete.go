package factory

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/afonsodelgado/factory/internal/state"
)

type Removal struct {
	Repo      string
	Slug      string
	Worktree  string
	Branches  []string
	StateDirs []string
	Marker    string
	Activity  string
	Drafts    string
	StageRuns string
}

func PlanRemoval(f Factory, cfg *state.Config) Removal {
	r := Removal{
		Repo:      f.Repo,
		Slug:      f.Slug,
		Worktree:  f.Worktree,
		Marker:    filepath.Join(WorktreesDir(f.Repo), "."+f.Slug+".ready"),
		Activity:  state.ActivityPath(cfg.ActivityDir(), f.Repo, f.Slug),
		Drafts:    filepath.Join(cfg.DraftsDir(), state.FileStem(f.Repo, f.Slug)),
		StageRuns: state.StageRunsPath(cfg.StateDir, f.Repo, f.Slug),
	}
	branch := worktreeBranch(f)
	r.Branches = append(r.Branches, branch)
	if f.State != nil {
		for _, pr := range f.State.PRs {
			r.Branches = append(r.Branches, pr.Branch)
		}
	}
	r.Branches = localBranches(f.Repo, r.Branches)
	r.StateDirs = stateDirs(f, branch)
	return r
}

func worktreeBranch(f Factory) string {
	if f.State != nil && f.State.Branch != "" {
		return f.State.Branch
	}
	if _, err := os.Lstat(filepath.Join(f.Worktree, ".git")); f.Worktree != "" && err == nil {
		if branch, err := git(f.Worktree, "rev-parse", "--abbrev-ref", "HEAD"); err == nil && branch != "HEAD" {
			return branch
		}
	}
	return "worktree-" + f.Slug
}

func localBranches(repo string, names []string) []string {
	trunk := ""
	if remoteHead, err := git(repo, "rev-parse", "--abbrev-ref", "origin/HEAD"); err == nil {
		trunk = strings.TrimPrefix(remoteHead, "origin/")
	}
	var branches []string
	for _, name := range names {
		if name == "" || name == trunk || slices.Contains(branches, name) {
			continue
		}
		branches = append(branches, name)
	}
	return branches
}

func stateDirs(f Factory, branch string) []string {
	names := []string{f.Slug}
	if label := f.Label(); label != f.Slug && isPlainName(label) {
		names = append(names, label)
	}
	var candidates []string
	if f.Dir != "" {
		candidates = append(candidates, f.Dir)
	}
	for _, name := range names {
		if f.Worktree != "" {
			candidates = append(candidates, filepath.Join(f.Worktree, ".claude", "factory", name), filepath.Join(f.Worktree, ".claude", "qa", name))
		}
		candidates = append(candidates, filepath.Join(f.Repo, ".claude", "factory", name), filepath.Join(f.Repo, ".claude", "qa", name), filepath.Join(ArchiveDir(f.Repo), name))
	}
	if f.Worktree != "" {
		candidates = append(candidates, filepath.Join(f.Worktree, ".claude", "qa", branch+".md"))
	}
	var dirs []string
	for _, c := range candidates {
		if leaf := throughLinkedParent(c); !slices.Contains(dirs, leaf) {
			dirs = append(dirs, leaf)
		}
	}
	return dirs
}

func isPlainName(name string) bool {
	return name != "" && name != "." && name != ".." && filepath.Base(name) == name
}

func throughLinkedParent(path string) string {
	parent, err := filepath.EvalSymlinks(filepath.Dir(path))
	if err != nil {
		return filepath.Clean(path)
	}
	return filepath.Join(parent, filepath.Base(path))
}

type Work struct {
	Dirty    []string
	Unpushed map[string][]string
}

func (w Work) Empty() bool {
	return len(w.Dirty) == 0 && len(w.Unpushed) == 0
}

func CheckWork(r Removal) Work {
	w := Work{Unpushed: map[string][]string{}}
	if dirExists(r.Worktree) {
		status, err := git(r.Worktree, "status", "--porcelain")
		if err != nil {
			status = "cannot read git status: " + status
		}
		w.Dirty = lines(status)
	}
	for _, branch := range r.Branches {
		if !branchExists(r.Repo, branch) {
			continue
		}
		commits, err := git(r.Repo, "log", "--oneline", "refs/heads/"+branch, "--not", "--remotes")
		if err != nil {
			commits = "cannot compare with the remotes: " + commits
		}
		if found := lines(commits); len(found) > 0 {
			w.Unpushed[branch] = found
		}
	}
	return w
}

type Outcome int

const (
	Ran Outcome = iota
	Skipped
	Failed
)

var outcomeWords = map[Outcome]string{Ran: "ran", Skipped: "skipped", Failed: "failed"}

func (o Outcome) String() string {
	return outcomeWords[o]
}

type RemovalStep struct {
	Name   string
	Detail string
	State  Outcome
	Err    error
}

func Run(r Removal, registry func(func(*state.Registry)) error) []RemovalStep {
	steps := []RemovalStep{removeWorktree(r)}
	for _, branch := range r.Branches {
		steps = append(steps, deleteBranch(r.Repo, branch))
	}
	for _, dir := range r.StateDirs {
		steps = append(steps, removePath("remove state", dir))
	}
	steps = append(steps,
		removePath("remove marker", r.Marker),
		removePath("remove activity log", r.Activity),
		removePath("remove drafts", r.Drafts),
		removePath("remove stage runs", r.StageRuns),
	)
	if _, stopped := FirstFailure(steps); stopped {
		return append(steps, keepEntry(r, registry))
	}
	return append(steps, removeEntry(r, registry))
}

func keepEntry(r Removal, registry func(func(*state.Registry)) error) RemovalStep {
	step := RemovalStep{Name: "keep registry entry", Detail: r.Slug + ", so that delete can run again", State: Ran}
	err := registry(func(reg *state.Registry) {
		entry := reg.Find(r.Repo, r.Slug)
		if entry == nil {
			entry = reg.Upsert(state.Entry{Repo: r.Repo, Slug: r.Slug})
		}
		entry.Created = true
	})
	if err != nil {
		step.State, step.Err = Failed, err
	}
	return step
}

func removeWorktree(r Removal) RemovalStep {
	step := RemovalStep{Name: "remove worktree", Detail: r.Worktree, State: Skipped}
	if dirExists(r.Worktree) {
		step.State = Ran
		if out, err := git(r.Repo, "worktree", "remove", "--force", r.Worktree); err != nil {
			step.State, step.Err = Failed, fmt.Errorf("git worktree remove: %s", out)
		}
	}
	git(r.Repo, "worktree", "prune")
	return step
}

func deleteBranch(repo, branch string) RemovalStep {
	step := RemovalStep{Name: "delete branch", Detail: branch, State: Skipped}
	if !branchExists(repo, branch) {
		return step
	}
	step.State = Ran
	if out, err := git(repo, "branch", "-D", branch); err != nil {
		step.State, step.Err = Failed, fmt.Errorf("git branch -D: %s", out)
	}
	return step
}

func removePath(name, path string) RemovalStep {
	step := RemovalStep{Name: name, Detail: path, State: Skipped}
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return step
	}
	step.State = Ran
	switch {
	case err != nil:
		step.State, step.Err = Failed, err
	case info.Mode()&fs.ModeSymlink != 0:
		err = os.Remove(path)
	default:
		err = os.RemoveAll(path)
	}
	if err != nil {
		step.State, step.Err = Failed, err
	}
	return step
}

func removeEntry(r Removal, registry func(func(*state.Registry)) error) RemovalStep {
	step := RemovalStep{Name: "remove registry entry", Detail: r.Slug, State: Skipped}
	removed := 0
	err := registry(func(reg *state.Registry) {
		removed = reg.Remove(func(e state.Entry) bool {
			ownEntry := e.Repo == r.Repo && e.Slug == r.Slug
			pinnedToRemovedDir := e.Repo == r.Repo && e.Dir != "" && slices.Contains(r.StateDirs, e.Dir)
			return ownEntry || pinnedToRemovedDir
		})
	})
	switch {
	case err != nil:
		step.State, step.Err = Failed, err
	case removed > 0:
		step.State = Ran
	}
	return step
}

func Report(steps []RemovalStep) string {
	var b strings.Builder
	for _, s := range steps {
		fmt.Fprintf(&b, "%-8s %s %s", s.State, s.Name, s.Detail)
		if s.Err != nil {
			fmt.Fprintf(&b, ": %v", s.Err)
		}
		b.WriteString("\n")
	}
	return b.String()
}

func FirstFailure(steps []RemovalStep) (RemovalStep, bool) {
	for _, s := range steps {
		if s.State == Failed {
			return s, true
		}
	}
	return RemovalStep{}, false
}

func git(dir string, args ...string) (string, error) {
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	return strings.TrimRight(string(out), "\n"), err
}

func branchExists(repo, branch string) bool {
	_, err := git(repo, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch)
	return err == nil
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return path != "" && err == nil && info.IsDir()
}

func lines(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return strings.Split(s, "\n")
}
