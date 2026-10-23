package factory

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

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

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

func TestDeleteRemovesEverythingLocalAndCanRunAgain(t *testing.T) {
	for k, v := range map[string]string{"GIT_AUTHOR_NAME": "t", "GIT_AUTHOR_EMAIL": "t@example.com", "GIT_COMMITTER_NAME": "t", "GIT_COMMITTER_EMAIL": "t@example.com", "GIT_CONFIG_GLOBAL": "/dev/null", "GIT_CONFIG_SYSTEM": "/dev/null"} {
		t.Setenv(k, v)
	}
	root := t.TempDir()
	origin, repo := filepath.Join(root, "origin.git"), filepath.Join(root, "repo")
	worktree := filepath.Join(WorktreesDir(repo), "eng-5-x")
	gitRun(t, root, "init", "-q", "--bare", "-b", "main", origin)
	gitRun(t, root, "init", "-q", "-b", "main", repo)
	writeFile(t, filepath.Join(repo, ".gitignore"), ".claude/\n")
	gitRun(t, repo, "add", ".gitignore")
	gitRun(t, repo, "commit", "-q", "-m", "init")
	gitRun(t, repo, "remote", "add", "origin", origin)
	gitRun(t, repo, "push", "-q", "-u", "origin", "main")
	gitRun(t, repo, "worktree", "add", "-q", "-b", "worktree-eng-5-x", worktree)
	gitRun(t, worktree, "push", "-q", "-u", "origin", "worktree-eng-5-x")
	gitRun(t, worktree, "switch", "-q", "-c", "stack-1")
	gitRun(t, worktree, "push", "-q", "-u", "origin", "stack-1")
	gitRun(t, worktree, "commit", "-q", "--allow-empty", "-m", "unpushed layer")
	gitRun(t, worktree, "switch", "-q", "-c", "stack-2")
	gitRun(t, worktree, "commit", "-q", "--allow-empty", "-m", "never pushed")
	gitRun(t, worktree, "switch", "-q", "worktree-eng-5-x")
	writeFile(t, filepath.Join(worktree, "notes.md"), "draft")

	if err := os.MkdirAll(filepath.Join(worktree, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(repo, ".claude", "factory"), filepath.Join(worktree, ".claude", "factory")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(repo, ".claude", "factory", "ENG-5", "state.json"), `{"id":"ENG-5","worktree":"`+worktree+`","branch":"worktree-eng-5-x","prs":[{"number":1,"branch":"stack-1"},{"number":2,"branch":"stack-2"}]}`)
	other := filepath.Join(repo, ".claude", "factory", "ENG-6", "state.json")
	writeFile(t, other, `{"id":"ENG-6"}`)
	writeFile(t, filepath.Join(repo, ".claude", "qa", "eng-5-x", "report.md"), "qa")
	writeFile(t, filepath.Join(repo, ".claude", "qa", "ENG-5", "brief.md"), "qa")
	marker := filepath.Join(WorktreesDir(repo), ".eng-5-x.ready")
	writeFile(t, marker, "")

	cfg := &state.Config{StateDir: t.TempDir()}
	activity := state.ActivityPath(cfg.ActivityDir(), repo, "eng-5-x")
	drafts := filepath.Join(cfg.DraftsDir(), state.FileStem(repo, "eng-5-x"))
	writeFile(t, activity, "{}\n")
	writeFile(t, filepath.Join(drafts, "q-5-1.json"), "{}")
	registry := &state.Registry{}
	registry.Upsert(state.Entry{Repo: repo, Slug: "eng-5-x", Created: true})
	update := func(fn func(*state.Registry)) error {
		fn(registry)
		return nil
	}

	var f Factory
	for _, found := range Discover([]string{repo}, registry) {
		if found.Slug == "eng-5-x" {
			f = found
		}
	}
	r := PlanRemoval(f, cfg)

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

	gitRun(t, repo, "worktree", "lock", worktree)
	if _, stopped := FirstFailure(Run(r, update)); !stopped || registry.Find(repo, "eng-5-x") == nil {
		t.Fatal("a delete that failed on a locked worktree left no registry entry, so nothing can find the factory to finish it")
	}
	gitRun(t, repo, "worktree", "unlock", worktree)

	steps := Run(r, update)
	if failed, ok := FirstFailure(steps); ok {
		t.Fatalf("delete failed at %s %s: %v\n%s", failed.Name, failed.Detail, failed.Err, Report(steps))
	}
	for _, gone := range []string{worktree, filepath.Join(repo, ".claude", "factory", "ENG-5"), filepath.Join(repo, ".claude", "qa", "eng-5-x"), filepath.Join(repo, ".claude", "qa", "ENG-5"), marker, activity, drafts} {
		if exists(gone) {
			t.Errorf("%s is still there after delete", gone)
		}
	}
	for _, branch := range []string{"worktree-eng-5-x", "stack-1", "stack-2"} {
		if branchExists(repo, branch) {
			t.Errorf("branch %s is still there after delete", branch)
		}
	}
	if !exists(other) {
		t.Error("another factory's state is gone: delete removed the symlinked .claude/factory parent instead of its own leaf")
	}
	if !branchExists(origin, "stack-1") {
		t.Error("the pushed stack-1 is gone from the remote: delete must never touch remote branches")
	}

	again := Run(r, update)
	for _, s := range again {
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
}
