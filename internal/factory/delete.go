package factory

import (
	"encoding/json"
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
	Delivered string
	Sessions  []string
	Launches  []string
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
		Delivered: filepath.Join(cfg.StateDir, "delivered", state.FileStem(f.Repo, f.Slug)+".json"),
		Sessions:  sessionFiles(cfg, f),
		Launches:  existing(state.LaunchScriptPaths(cfg.StateDir, f.Repo, f.Slug)),
	}
	branch := worktreeBranch(f)
	others := otherFactoriesBranches(cfg, f)
	r.Branches = slices.DeleteFunc(localBranches(f.Repo, withStacks(f.Repo, knownBranches(f, branch))), func(b string) bool {
		return slices.Contains(others, b)
	})
	r.StateDirs = stateDirs(f, branch)
	return r
}

func otherFactoriesBranches(cfg *state.Config, f Factory) []string {
	registry, err := state.LoadRegistry(cfg.RegistryPath())
	if err != nil {
		return nil
	}
	var branches []string
	for _, e := range registry.Factories {
		if e.Repo == f.Repo && e.Slug != f.Slug {
			branches = append(branches, "worktree-"+e.Slug)
			branches = append(branches, e.Branches...)
		}
	}
	return branches
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

func knownBranches(f Factory, branch string) []string {
	names := []string{branch, "worktree-" + f.Slug}
	if listed, ok, _ := findWorktree(f.Repo, f.Worktree); ok {
		names = append(names, listed.branch)
	}
	if f.State != nil {
		for _, pr := range f.State.PRs {
			names = append(names, pr.Branch)
		}
	}
	if f.Entry != nil {
		names = append(names, f.Entry.Branches...)
	}
	return names
}

func withStacks(repo string, names []string) []string {
	for _, stack := range ghStacks(repo) {
		if slices.ContainsFunc(stack, func(b string) bool { return slices.Contains(names, b) }) {
			names = append(names, stack...)
		}
	}
	return names
}

func ghStacks(repo string) [][]string {
	common, err := git(repo, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return nil
	}
	data, err := os.ReadFile(filepath.Join(common, "gh-stack"))
	if err != nil {
		return nil
	}
	var file struct {
		Stacks []struct {
			Branches []struct {
				Branch string `json:"branch"`
			} `json:"branches"`
		} `json:"stacks"`
	}
	if json.Unmarshal(data, &file) != nil {
		return nil
	}
	var stacks [][]string
	for _, s := range file.Stacks {
		var names []string
		for _, b := range s.Branches {
			names = append(names, b.Branch)
		}
		stacks = append(stacks, names)
	}
	return stacks
}

func localBranches(repo string, names []string) []string {
	protected := trunkNames(repo)
	var branches []string
	for _, name := range names {
		if name == "" || slices.Contains(protected, name) || slices.Contains(branches, name) {
			continue
		}
		branches = append(branches, name)
	}
	return branches
}

func trunkNames(repo string) []string {
	names := []string{"main", "master"}
	if remoteHead, err := git(repo, "rev-parse", "--abbrev-ref", "origin/HEAD"); err == nil {
		names = append(names, strings.TrimPrefix(remoteHead, "origin/"))
	}
	return names
}

func remoteTrunk(repo string) string {
	for _, ref := range []string{"origin/HEAD", "origin/main", "origin/master"} {
		if _, err := git(repo, "rev-parse", "--verify", "--quiet", "refs/remotes/"+ref); err == nil {
			return ref
		}
	}
	return ""
}

func sessionFiles(cfg *state.Config, f Factory) []string {
	hooks, _ := state.ReadHookFiles(cfg.StateDir)
	worktree := ""
	if f.Worktree != "" && realPath(f.Worktree) != realPath(f.Repo) {
		worktree = realPath(f.Worktree)
	}
	var paths []string
	for _, h := range hooks {
		ownSession := h.SessionID == f.SessionID()
		inWorktree := worktree != "" && within(realPath(h.CWD), worktree)
		if h.SessionID == "" || (!ownSession && !inWorktree) {
			continue
		}
		for _, ext := range []string{".json", ".lock"} {
			paths = append(paths, filepath.Join(cfg.SessionsDir(), h.SessionID+ext))
		}
	}
	return existing(paths)
}

func existing(paths []string) []string {
	return slices.DeleteFunc(paths, func(path string) bool {
		_, err := os.Lstat(path)
		return err != nil
	})
}

func within(path, dir string) bool {
	return path == dir || strings.HasPrefix(path, dir+string(filepath.Separator))
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
	clean := filepath.Clean(path)
	return filepath.Join(realPath(filepath.Dir(clean)), filepath.Base(clean))
}

func realPath(path string) string {
	clean := filepath.Clean(path)
	if real, err := filepath.EvalSymlinks(clean); err == nil {
		return real
	}
	parent := filepath.Dir(clean)
	if parent == clean {
		return clean
	}
	return filepath.Join(realPath(parent), filepath.Base(clean))
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
	trunk := remoteTrunk(r.Repo)
	for _, branch := range r.Branches {
		if !branchExists(r.Repo, branch) {
			continue
		}
		commits, err := git(r.Repo, "log", "--oneline", "refs/heads/"+branch, "--not", "--remotes")
		if err != nil {
			commits = "cannot compare with the remotes: " + commits
		}
		if found := lines(commits); len(found) > 0 && !landedIn(r.Repo, trunk, branch) {
			w.Unpushed[branch] = found
		}
	}
	return w
}

func landedIn(repo, trunk, branch string) bool {
	if trunk == "" {
		return false
	}
	merged, err := git(repo, "merge-tree", "--write-tree", trunk, "refs/heads/"+branch)
	if err != nil {
		return false
	}
	tree, err := git(repo, "rev-parse", trunk+"^{tree}")
	mergedTree, _, _ := strings.Cut(merged, "\n")
	return err == nil && mergedTree == tree
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

func PanesClosed(label string, err error) RemovalStep {
	step := RemovalStep{Name: "close panes", Detail: label, State: Ran}
	if err != nil {
		step.State, step.Err = Failed, err
	}
	return step
}

func Run(closed RemovalStep, r Removal, registry func(func(*state.Registry)) error) []RemovalStep {
	steps := []RemovalStep{closed, removeWorktree(r)}
	worktrees, _ := listWorktrees(r.Repo)
	for _, branch := range r.Branches {
		steps = append(steps, deleteBranch(r.Repo, branch, worktrees))
	}
	for _, dir := range r.StateDirs {
		steps = append(steps, removePath("remove state", dir))
	}
	steps = append(steps,
		removePath("remove marker", r.Marker),
		removePath("remove activity log", r.Activity),
		removePath("remove drafts", r.Drafts),
		removePath("remove stage runs", r.StageRuns),
		removePath("remove delivered messages", r.Delivered),
	)
	for _, path := range r.Sessions {
		steps = append(steps, removePath("remove session record", path))
	}
	for _, path := range r.Launches {
		steps = append(steps, removePath("remove launch script", path))
	}
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
		for _, branch := range r.Branches {
			if !slices.Contains(entry.Branches, branch) {
				entry.Branches = append(entry.Branches, branch)
			}
		}
	})
	if err != nil {
		step.State, step.Err = Failed, err
	}
	return step
}

func removeWorktree(r Removal) RemovalStep {
	defer git(r.Repo, "worktree", "prune")
	step := RemovalStep{Name: "remove worktree", Detail: r.Worktree, State: Skipped}
	if r.Worktree == "" {
		return step
	}
	listed, tracked, err := findWorktree(r.Repo, r.Worktree)
	ownFolder := realPath(r.Worktree) == realPath(filepath.Join(WorktreesDir(r.Repo), r.Slug))
	untracked := err == nil && !tracked
	switch {
	case realPath(r.Worktree) == realPath(r.Repo):
		step.Detail += ", kept: it is the main checkout"
		return step
	case !tracked && !dirExists(r.Worktree):
		return step
	case ownFolder && untracked:
		return removePath(step.Name, r.Worktree)
	}
	target := r.Worktree
	if tracked {
		target = listed.path
	}
	step.State = Ran
	if out, err := git(r.Repo, "worktree", "remove", "--force", "--force", target); err != nil {
		step.State, step.Err = Failed, fmt.Errorf("git worktree remove: %s", out)
	}
	return step
}

type gitWorktree struct {
	path   string
	branch string
}

func listWorktrees(repo string) ([]gitWorktree, error) {
	out, err := git(repo, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	var list []gitWorktree
	for _, line := range lines(out) {
		if path, ok := strings.CutPrefix(line, "worktree "); ok {
			list = append(list, gitWorktree{path: path})
		} else if branch, ok := strings.CutPrefix(line, "branch refs/heads/"); ok && len(list) > 0 {
			list[len(list)-1].branch = branch
		}
	}
	return list, nil
}

func findWorktree(repo, dir string) (gitWorktree, bool, error) {
	if dir == "" {
		return gitWorktree{}, false, nil
	}
	list, err := listWorktrees(repo)
	target := realPath(dir)
	for _, w := range list {
		if realPath(w.path) == target {
			return w, true, nil
		}
	}
	return gitWorktree{}, false, err
}

func deleteBranch(repo, branch string, worktrees []gitWorktree) RemovalStep {
	step := RemovalStep{Name: "delete branch", Detail: branch, State: Skipped}
	if !branchExists(repo, branch) {
		return step
	}
	if i := slices.IndexFunc(worktrees, func(w gitWorktree) bool { return w.branch == branch }); i >= 0 {
		step.Detail += ", kept: checked out at " + worktrees[i].path
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
