package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/afonsodelgado/factory/internal/factory"
	"github.com/afonsodelgado/factory/internal/host"
	"github.com/afonsodelgado/factory/internal/node"
	"github.com/afonsodelgado/factory/internal/state"
	"github.com/google/uuid"
)

func shortStateDir(t *testing.T) string {
	t.Helper()
	stateDir, err := os.MkdirTemp("/tmp", "fdel-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(stateDir) })
	t.Setenv("FACTORY_STATE_DIR", stateDir)
	return stateDir
}

func scratchNode(t *testing.T, config string) {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		if os.Getenv("CI") == "" {
			t.Skip("tmux not installed")
		}
		t.Fatal(err)
	}
	socket := "fdel-" + uuid.NewString()
	if out, err := exec.Command("tmux", "-L", socket, "-f", "/dev/null", "new-session", "-d", "-s", "factory", "exec sleep 600").CombinedOutput(); err != nil {
		t.Fatalf("tmux: %v\n%s", err, out)
	}
	t.Cleanup(func() { exec.Command("tmux", "-L", socket, "kill-server").Run() })
	t.Setenv("FACTORY_TMUX_SOCKET", socket)
	writeFile(t, filepath.Join(config, "config.yml"), readOr(filepath.Join(config, "config.yml"))+fmt.Sprintf("node:\n  port: %d\n", freePort(t)))
	cfg, err := state.LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	n, err := startNode(ctx, cfg, "owner@example.com")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancel()
		n.close()
	})
}

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
	shortStateDir(t)
	scratchNode(t, config)
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

	confirmedClean := host.DeleteRequest{Repo: repo, Slug: "eng-7-x", Work: host.WorkDigest(factory.Work{})}
	answer, err := node.Unix(filepath.Join(os.Getenv("FACTORY_STATE_DIR"), "node.sock")).Call(context.Background(), host.Message{T: "delete", Op: uuid.NewString(), Data: mustJSON(confirmedClean)})
	if _, statErr := os.Stat(filepath.Join(worktree, "notes.md")); err != nil || answer.T == "done" || statErr != nil {
		t.Errorf("a delete confirmed before notes.md existed answered %+v, %v; want the node to refuse work that changed since the confirmation", answer, err)
	}
}

func TestDeleteOfAFactoryWhoseRepoIsGoneFinishesAndCanRunAgain(t *testing.T) {
	config := t.TempDir()
	gone := filepath.Join(t.TempDir(), "removed-repo")
	t.Setenv("FACTORY_CONFIG_DIR", config)
	stateDir := shortStateDir(t)
	t.Chdir(t.TempDir())
	registry, err := state.LoadRegistry(filepath.Join(stateDir, "registry.json"))
	if err != nil {
		t.Fatal(err)
	}
	registry.Upsert(state.Entry{Repo: gone, Slug: "x", Created: true})
	if err := registry.Save(); err != nil {
		t.Fatal(err)
	}
	scratchNode(t, config)

	for run := 1; run <= 2; run++ {
		var out bytes.Buffer
		if code := runDelete([]string{"x", "--yes"}, os.Stdin, &out); code != 0 {
			t.Fatalf("run %d: exit %d, output:\n%s\nwant 0: a factory whose repo folder was removed must still be deletable, and deleting it again is not an error", run, code, out.String())
		}
	}
	if registry, _ := state.LoadRegistry(filepath.Join(stateDir, "registry.json")); registry.Find(gone, "x") != nil {
		t.Error("the registry still lists the factory after delete")
	}
}
