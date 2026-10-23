package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestDeleteRefusesUnpushedWorkWithoutATerminal(t *testing.T) {
	for k, v := range map[string]string{"GIT_AUTHOR_NAME": "t", "GIT_AUTHOR_EMAIL": "t@example.com", "GIT_COMMITTER_NAME": "t", "GIT_COMMITTER_EMAIL": "t@example.com", "GIT_CONFIG_GLOBAL": "/dev/null", "GIT_CONFIG_SYSTEM": "/dev/null"} {
		t.Setenv(k, v)
	}
	repo := filepath.Join(t.TempDir(), "repo")
	worktree := filepath.Join(repo, ".claude", "worktrees", "eng-7-x")
	for _, args := range [][]string{{"init", "-q", "-b", "main", repo}, {"-C", repo, "commit", "-q", "--allow-empty", "-m", "init"}, {"-C", repo, "worktree", "add", "-q", "-b", "worktree-eng-7-x", worktree}} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(worktree, "notes.md"), []byte("draft"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(worktree, ".claude", "factory", "eng-7-x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(worktree, ".claude", "factory", "eng-7-x", "state.json"), []byte(`{"id":"eng-7-x"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	config := t.TempDir()
	if err := os.WriteFile(filepath.Join(config, "config.yml"), []byte("repos: ["+repo+"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FACTORY_CONFIG_DIR", config)
	t.Setenv("FACTORY_STATE_DIR", t.TempDir())
	t.Setenv("FACTORY_TMUX_SOCKET", "factory-delete-test-unused")
	t.Chdir(t.TempDir())
	stdin, feed, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	feed.WriteString("y\n")
	feed.Close()

	var out bytes.Buffer
	code := runDelete([]string{"eng-7-x"}, stdin, &out)
	asked := bytes.Contains(out.Bytes(), []byte("Deletes eng-7-x"))
	if _, err := os.Stat(filepath.Join(worktree, "notes.md")); code != 1 || !asked || err != nil {
		t.Errorf("exit %d, notes.md: %v, output:\n%s\nwant the first question, then exit 1 with nothing deleted: a script piping y must not delete uncommitted work", code, err, out.String())
	}
}
