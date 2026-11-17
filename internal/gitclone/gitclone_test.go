package gitclone

import (
	"context"
	"errors"
	"testing"
)

func TestGitReturnsStdoutOnlyAndKeepsCancellation(t *testing.T) {
	repo := t.TempDir()
	if _, err := Git(context.Background(), repo, "init", "-q"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_TRACE", "1")
	out, err := Git(context.Background(), repo, "rev-parse", "--git-dir")
	if err != nil || out != ".git" {
		t.Fatalf("got %q, %v; want the bare stdout even when git writes to stderr", out, err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Git(cancelled, repo, "rev-parse", "--git-dir"); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v; want a cancelled context to surface as context.Canceled", err)
	}
}
