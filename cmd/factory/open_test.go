package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/afonsodelgado/factory/internal/hub"
)

func TestOpenRepoResolvesToTheConfiguredPathOrIsRefused(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "factory")
	other := filepath.Join(root, "other")
	for _, dir := range []string{repo, other} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(repo)
	for _, given := range []string{repo + "/", ".", "../factory"} {
		if got, ok := configuredRepo([]string{repo}, given); !ok || got != repo {
			t.Errorf("--repo %q resolved to %q, %v; want the configured %q", given, got, ok, repo)
		}
	}
	if got, ok := configuredRepo([]string{repo}, other); ok {
		t.Errorf("--repo %q outside the configured repos was accepted as %q", other, got)
	}
}

func TestOpenAndTheHubRefuseWithoutANodeAndCreateNothing(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "repo")
	if out, err := exec.Command("git", "init", "-q", repo).CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("FACTORY_CONFIG_DIR", t.TempDir())
	t.Setenv("FACTORY_STATE_DIR", t.TempDir())
	t.Setenv("FACTORY_TMUX_SOCKET", "factory-open-test-unused")
	t.Chdir(repo)
	if err := openMain(hub.OpenRequest{Name: "csv", Prompt: "Export contacts."}); !errors.Is(err, errNoNode) {
		t.Fatalf("open without a node = %v; want the node guidance", err)
	}
	if err := hubMain(nil, false); !errors.Is(err, errNoNode) {
		t.Fatalf("the hub without a node = %v; want the node guidance, never a runtime of its own", err)
	}
	if _, err := os.Stat(filepath.Join(repo, ".factory")); err == nil {
		t.Error("open set up the repo before finding no node to run the factory")
	}
}
