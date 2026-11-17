package factory

import (
	"os"
	"path/filepath"
	"slices"
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

func TestDiscoverOpensAPortableArtifactInItsOwnFactoryFolder(t *testing.T) {
	repo := t.TempDir()
	dir := filepath.Join(WorktreesDir(repo), "csv", ".claude", "factory", "csv")
	writeState(t, dir, `{"id":"csv","stages":{"design":{"artifacts":[{"url":"design/variant-a.html"},{"url":"../../../../etc/passwd"},{"url":"https://example.com/page"}]}}}`)
	found := Discover([]string{repo}, &state.Registry{})
	if len(found) != 1 {
		t.Fatalf("found %d factories", len(found))
	}
	var urls []string
	for _, a := range found[0].State.Artifacts() {
		urls = append(urls, a.URL)
	}
	want := []string{"file://" + filepath.Join(dir, "design", "variant-a.html"), "../../../../etc/passwd", "https://example.com/page"}
	if !slices.Equal(urls, want) {
		t.Errorf("artifacts read as %v; want a checkpointed relative page under its factory folder and nothing else rewritten", urls)
	}
}
