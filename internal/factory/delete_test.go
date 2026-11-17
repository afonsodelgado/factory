package factory

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/afonsodelgado/factory/internal/state"
)

func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func commitFile(t *testing.T, dir, name, message string) {
	t.Helper()
	writeFile(t, filepath.Join(dir, name), message)
	gitRun(t, dir, "add", name)
	gitRun(t, dir, "commit", "-q", "-m", message)
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

func newRepo(t *testing.T) (root, origin, repo string) {
	t.Helper()
	for k, v := range map[string]string{"GIT_AUTHOR_NAME": "t", "GIT_AUTHOR_EMAIL": "t@example.com", "GIT_COMMITTER_NAME": "t", "GIT_COMMITTER_EMAIL": "t@example.com", "GIT_CONFIG_GLOBAL": "/dev/null", "GIT_CONFIG_SYSTEM": "/dev/null"} {
		t.Setenv(k, v)
	}
	root = t.TempDir()
	origin, repo = filepath.Join(root, "origin.git"), filepath.Join(root, "repo")
	gitRun(t, root, "init", "-q", "--bare", "-b", "main", origin)
	gitRun(t, root, "init", "-q", "-b", "main", repo)
	writeFile(t, filepath.Join(repo, ".gitignore"), ".claude/\n")
	gitRun(t, repo, "add", ".gitignore")
	gitRun(t, repo, "commit", "-q", "-m", "init")
	gitRun(t, repo, "remote", "add", "origin", origin)
	gitRun(t, repo, "push", "-q", "-u", "origin", "main")
	return root, origin, repo
}

func TestDeleteRemovesEverythingLocalAndCanRunAgain(t *testing.T) {
	root, origin, repo := newRepo(t)
	worktree := filepath.Join(WorktreesDir(repo), "eng-5-x")
	gitRun(t, repo, "worktree", "add", "-q", "-b", "worktree-eng-5-x", worktree)
	gitRun(t, worktree, "push", "-q", "-u", "origin", "worktree-eng-5-x")
	gitRun(t, worktree, "switch", "-q", "-c", "stack-1")
	gitRun(t, worktree, "push", "-q", "-u", "origin", "stack-1")
	commitFile(t, worktree, "layer-1.go", "unpushed layer")
	gitRun(t, worktree, "switch", "-q", "-c", "stack-2")
	commitFile(t, worktree, "layer-2.go", "never pushed")
	gitRun(t, worktree, "switch", "-q", "worktree-eng-5-x")
	writeFile(t, filepath.Join(worktree, "notes.md"), "draft")

	if err := os.MkdirAll(filepath.Join(worktree, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(repo, ".claude", "factory"), filepath.Join(worktree, ".claude", "factory")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(repo, ".claude", "factory", "ENG-5", "state.json"), `{"schema_version":2,"id":"ENG-5","worktree":"`+worktree+`","branch":"worktree-eng-5-x","prs":[{"number":1,"branch":"stack-1"},{"number":2,"branch":"stack-2"}]}`)
	other := filepath.Join(repo, ".claude", "factory", "ENG-6", "state.json")
	writeFile(t, other, `{"schema_version":2,"id":"ENG-6"}`)
	writeFile(t, filepath.Join(repo, ".claude", "qa", "eng-5-x", "report.md"), "qa")
	writeFile(t, filepath.Join(repo, ".claude", "qa", "ENG-5", "brief.md"), "qa")
	marker := filepath.Join(WorktreesDir(repo), ".eng-5-x.ready")
	writeFile(t, marker, "")

	cfg := &state.Config{StateDir: t.TempDir()}
	activity := state.ActivityPath(cfg.ActivityDir(), repo, "eng-5-x")
	drafts := filepath.Join(cfg.DraftsDir(), state.FileStem(repo, "eng-5-x"))
	writeFile(t, activity, "{}\n")
	writeFile(t, filepath.Join(drafts, "q-5-1.json"), "{}")
	launch := state.LaunchScriptPath(cfg.StateDir, repo, "eng-5-x", "engineer")
	writeFile(t, launch, "codex")
	for session, cwd := range map[string]string{"eng-5-session": worktree, "neighbour-session": worktree + "-2"} {
		if err := os.MkdirAll(cwd, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := state.RecordHook(cfg.StateDir, []byte(`{"session_id":"`+session+`","cwd":"`+cwd+`","hook_event_name":"Stop"}`), "", time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	sessionRecord := filepath.Join(cfg.SessionsDir(), "eng-5-session.json")
	registry := &state.Registry{}
	registry.Upsert(state.Entry{Repo: repo, Slug: "eng-5-x", Created: true})
	update := func(fn func(*state.Registry)) error {
		fn(registry)
		return nil
	}

	discover := func() Factory {
		for _, found := range Discover([]string{repo}, registry) {
			if found.Slug == "eng-5-x" {
				return found
			}
		}
		t.Fatal("delete can no longer find eng-5-x")
		return Factory{}
	}
	r := PlanRemoval(discover(), cfg)

	work := CheckWork(r)
	unpushed := slices.Sorted(func(yield func(string) bool) {
		for b := range work.Unpushed {
			if !yield(b) {
				return
			}
		}
	})
	if !slices.ContainsFunc(work.Dirty, func(l string) bool { return strings.Contains(l, "notes.md") }) || !slices.Equal(unpushed, []string{"stack-1", "stack-2"}) {
		t.Errorf("work = %+v; want notes.md uncommitted and stack-1 and stack-2 unpushed, or delete skips the second confirmation and loses them", work)
	}

	gitRun(t, repo, "worktree", "lock", "--reason", "claude session eng-5-x (pid 1)", worktree)
	refLock := filepath.Join(repo, ".git", "refs", "heads", "stack-2.lock")
	writeFile(t, refLock, "")
	if _, stopped := FirstFailure(Run(PanesClosed("eng-5-x", nil), r, update)); !stopped || registry.Find(repo, "eng-5-x") == nil {
		t.Fatal("a delete that failed on a locked branch left no registry entry, so nothing can find the factory to finish it")
	}
	if err := os.Remove(refLock); err != nil {
		t.Fatal(err)
	}

	r = PlanRemoval(discover(), cfg)
	steps := Run(PanesClosed("eng-5-x", nil), r, update)
	if failed, ok := FirstFailure(steps); ok {
		t.Fatalf("delete failed at %s %s: %v\n%s", failed.Name, failed.Detail, failed.Err, Report(steps))
	}
	for _, gone := range []string{worktree, filepath.Join(repo, ".claude", "factory", "ENG-5"), filepath.Join(repo, ".claude", "qa", "eng-5-x"), filepath.Join(repo, ".claude", "qa", "ENG-5"), marker, activity, drafts, launch, sessionRecord} {
		if exists(gone) {
			t.Errorf("%s is still there after delete", gone)
		}
	}
	for _, branch := range []string{"worktree-eng-5-x", "stack-1", "stack-2"} {
		if branchExists(repo, branch) {
			t.Errorf("branch %s is still there after a delete that ran again: the failed run removed state.json and the retry forgot the stack", branch)
		}
	}
	if !exists(filepath.Join(cfg.SessionsDir(), "neighbour-session.json")) {
		t.Error("delete removed the session record of a factory in a neighbouring worktree")
	}
	if !exists(other) {
		t.Error("another factory's state is gone: delete removed the symlinked .claude/factory parent instead of its own leaf")
	}
	if !branchExists(origin, "stack-1") {
		t.Error("the pushed stack-1 is gone from the remote: delete must never touch remote branches")
	}

	again := Run(PanesClosed("eng-5-x", nil), r, update)
	for _, s := range again[1:] {
		if s.State != Skipped {
			t.Errorf("second delete: %s %s %s; want every step skipped, so a delete that stopped half way can be finished", s.State, s.Name, s.Detail)
		}
	}
	taken := func(slug string) bool {
		return registry.Find(repo, slug) != nil || exists(filepath.Join(WorktreesDir(repo), slug))
	}
	if slug := FreshSlug("eng-5-x", taken); slug != "eng-5-x" {
		t.Errorf("a new factory after delete gets %s; want the same id back", slug)
	}

	writeFile(t, filepath.Join(worktree, "node_modules", "left.js"), "")
	registry.Upsert(state.Entry{Repo: repo, Slug: "eng-5-x", Created: true})
	stranger := filepath.Join(root, "not-a-factory")
	writeFile(t, filepath.Join(stranger, "keep.md"), "")
	orphaned := Run(PanesClosed("eng-5-x", nil), PlanRemoval(discover(), cfg), update)
	if failed, ok := FirstFailure(orphaned); ok || exists(worktree) || registry.Find(repo, "eng-5-x") != nil {
		t.Fatalf("delete of a worktree folder git no longer tracks: failed %+v, folder there %v; it fails forever and the factory can never be deleted\n%s", failed, exists(worktree), Report(orphaned))
	}
	Run(PanesClosed("eng-5-x", nil), Removal{Repo: repo, Slug: "eng-5-x", Worktree: stranger}, update)
	if !exists(filepath.Join(stranger, "keep.md")) {
		t.Error("delete removed an untracked folder outside the factory's own worktrees slot")
	}
}

func TestDeleteFinishesAFactoryWhoseLockedWorktreeFolderIsGone(t *testing.T) {
	_, _, repo := newRepo(t)
	worktree := filepath.Join(WorktreesDir(repo), "x")
	gitRun(t, repo, "branch", "y-layer-1")
	gitRun(t, repo, "worktree", "add", "-q", "-b", "worktree-x", worktree)
	for _, layer := range []string{"x-layer-1", "x-layer-2"} {
		gitRun(t, worktree, "switch", "-q", "-c", layer)
		commitFile(t, worktree, layer+".go", layer)
	}
	gitRun(t, worktree, "switch", "-q", "worktree-x")
	writeFile(t, filepath.Join(repo, ".git", "gh-stack"), `{"stacks":[{"trunk":{"branch":"main"},"branches":[{"branch":"y-layer-1"},{"branch":"x-layer-1"},{"branch":"x-layer-2"}]}]}`)
	gitRun(t, worktree, "switch", "-q", "x-layer-2")
	gitRun(t, repo, "worktree", "lock", "--reason", "claude session x (pid 1)", worktree)
	if err := os.RemoveAll(worktree); err != nil {
		t.Fatal(err)
	}
	cfg := &state.Config{StateDir: t.TempDir()}
	registry, err := state.LoadRegistry(cfg.RegistryPath())
	if err != nil {
		t.Fatal(err)
	}
	registry.Upsert(state.Entry{Repo: repo, Slug: "x", Created: true})
	registry.Upsert(state.Entry{Repo: repo, Slug: "y", Branches: []string{"y-layer-1"}})
	if err := registry.Save(); err != nil {
		t.Fatal(err)
	}
	update := func(fn func(*state.Registry)) error {
		fn(registry)
		return registry.Save()
	}

	f := Discover([]string{repo}, registry)[0]
	steps := Run(PanesClosed("x", nil), PlanRemoval(f, cfg), update)
	if failed, ok := FirstFailure(steps); ok {
		t.Fatalf("delete failed at %s %s: %v; a locked worktree removed by hand keeps its branch checked out, so delete fails on every retry\n%s", failed.Name, failed.Detail, failed.Err, Report(steps))
	}
	for _, branch := range []string{"worktree-x", "x-layer-1", "x-layer-2"} {
		if branchExists(repo, branch) {
			t.Errorf("branch %s is still there: delete must clear git's entry for a locked worktree removed by hand, and find the stack branches only gh-stack tracks", branch)
		}
	}
	if !branchExists(repo, "main") {
		t.Error("delete removed the trunk")
	}
	if !branchExists(repo, "y-layer-1") {
		t.Error("delete removed y-layer-1, which another factory owns, because x's stack is built on it")
	}
}

func TestCheckWorkTreatsASquashMergedStackAsSafe(t *testing.T) {
	root, origin, repo := newRepo(t)
	worktree := filepath.Join(WorktreesDir(repo), "d")
	gitRun(t, repo, "worktree", "add", "-q", "-b", "worktree-d", worktree)
	gitRun(t, worktree, "switch", "-q", "-c", "stack-1")
	commitFile(t, worktree, "feature.go", "feature")
	gitRun(t, worktree, "push", "-q", "-u", "origin", "stack-1")
	merger := filepath.Join(root, "merger")
	gitRun(t, root, "clone", "-q", origin, merger)
	gitRun(t, merger, "merge", "-q", "--squash", "origin/stack-1")
	gitRun(t, merger, "commit", "-q", "-m", "feature (#1)")
	commitFile(t, merger, "later.go", "later work on main")
	gitRun(t, merger, "push", "-q", "origin", "main", ":stack-1")
	gitRun(t, repo, "fetch", "-q", "--prune")

	f := Factory{Repo: repo, Slug: "d", Worktree: worktree, State: &state.State{SchemaVersion: 2, Branch: "worktree-d", PRs: []state.PR{{Number: 1, Branch: "stack-1"}}}}
	if work := CheckWork(PlanRemoval(f, &state.Config{StateDir: t.TempDir()})); !work.Empty() {
		t.Errorf("work = %+v; a squash-merged stack whose remote branch is gone is reported as work that exists nowhere else, so deleting a done factory asks twice and refuses without a terminal", work)
	}
}
