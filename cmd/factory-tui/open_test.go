package main

import (
	"os"
	"path/filepath"
	"testing"
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
