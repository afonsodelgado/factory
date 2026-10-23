package factory

import (
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/afonsodelgado/factory/internal/state"
)

func writeState(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "state.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDiscoverDeduplicatesSymlinkedStateAndMatchesTicketsExactly(t *testing.T) {
	repo := t.TempDir()
	worktrees := WorktreesDir(repo)
	writeState(t, filepath.Join(repo, ".claude", "factory", "ENG-740"), `{"ticket":"ENG-740","stage":"plan"}`)
	if err := os.MkdirAll(filepath.Join(worktrees, "eng-740-cors", ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(repo, ".claude", "factory"), filepath.Join(worktrees, "eng-740-cors", ".claude", "factory")); err != nil {
		t.Fatal(err)
	}
	writeState(t, filepath.Join(worktrees, "csv-export", ".claude", "factory", "csv-export"), `{"ticket":null,"stage":"design"}`)

	found := Discover([]string{repo}, &state.Registry{})

	var keys []string
	for _, f := range found {
		keys = append(keys, f.Key())
	}
	sort.Strings(keys)
	want := []string{state.Key(repo, "csv-export"), state.Key(repo, "eng-740-cors")}
	if len(keys) != 2 || keys[0] != want[0] || keys[1] != want[1] {
		t.Fatalf("factories = %v, want %v: the cowork symlink must not list a factory twice", keys, want)
	}

	if err := os.MkdirAll(filepath.Join(worktrees, "eng-7400-x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if wt, ok := FindWorktree(repo, "ENG-7400"); !ok || filepath.Base(wt) != "eng-7400-x" {
		t.Errorf("ENG-7400 found %q", wt)
	}
	if err := os.RemoveAll(filepath.Join(worktrees, "eng-740-cors")); err != nil {
		t.Fatal(err)
	}
	if wt, ok := FindWorktree(repo, "ENG-740"); ok {
		t.Errorf("ENG-740 matched %q", wt)
	}
}
