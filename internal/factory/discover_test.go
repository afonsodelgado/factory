package factory

import (
	"os"
	"path/filepath"
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

func TestDiscoverDeduplicatesSymlinkedStateAndReadsOldTicketFiles(t *testing.T) {
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

	labels := map[string]string{}
	for _, f := range found {
		labels[f.Key()] = f.Label()
	}
	want := map[string]string{state.Key(repo, "csv-export"): "csv-export", state.Key(repo, "eng-740"): "ENG-740"}
	if len(labels) != len(want) {
		t.Fatalf("factories = %v, want %v: the cowork symlink must not list a factory twice", labels, want)
	}
	for key, label := range want {
		if labels[key] != label {
			t.Errorf("%s is labelled %q, want %q: a state.json written with ticket, or ticket null, must still name its factory", key, labels[key], label)
		}
	}
}
