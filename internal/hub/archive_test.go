package hub

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func run(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v: %v\n%s", args, err, out)
	}
}

func TestWorktreeRemovalRefusesUnsafeWork(t *testing.T) {
	root := t.TempDir()
	origin := filepath.Join(root, "origin.git")
	repo := filepath.Join(root, "repo")
	worktree := filepath.Join(repo, ".claude", "worktrees", "eng-1-x")
	run(t, root, "git", "init", "-q", "--bare", "-b", "main", origin)
	run(t, root, "git", "init", "-q", "-b", "main", repo)
	if err := os.WriteFile(filepath.Join(repo, ".gitignore"), []byte(".claude/factory/\n.claude/worktrees/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, repo, "git", "add", ".gitignore")
	run(t, repo, "git", "commit", "-q", "-m", "init")
	run(t, repo, "git", "remote", "add", "origin", origin)
	run(t, repo, "git", "push", "-q", "-u", "origin", "main")
	run(t, repo, "git", "worktree", "add", "-q", "-b", "worktree-eng-1-x", worktree)
	run(t, worktree, "git", "push", "-q", "-u", "origin", "worktree-eng-1-x")
	factoryDir := filepath.Join(worktree, ".claude", "factory", "ENG-1")
	if err := os.MkdirAll(factoryDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(factoryDir, "state.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(worktree, "notes.md"), []byte("draft"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := RemoveWorktree(repo, worktree, factoryDir); err == nil {
		t.Fatal("removed a worktree with uncommitted changes")
	}

	run(t, worktree, "git", "add", "notes.md")
	run(t, worktree, "git", "commit", "-q", "-m", "notes")
	if err := RemoveWorktree(repo, worktree, factoryDir); err == nil {
		t.Fatal("removed a worktree with a commit not pushed to its upstream")
	}

	run(t, worktree, "git", "push", "-q")
	if err := RemoveWorktree(repo, worktree, factoryDir); err != nil {
		t.Fatalf("a clean, pushed worktree was not removed: %v", err)
	}
	if _, err := os.Stat(worktree); !os.IsNotExist(err) {
		t.Error("the worktree directory is still there")
	}
	if _, err := os.Stat(filepath.Join(repo, ".claude", "factory", "ENG-1", "state.json")); err != nil {
		t.Errorf("the factory directory inside the worktree was not kept: %v", err)
	}
	run(t, repo, "git", "rev-parse", "--verify", "worktree-eng-1-x")
}
