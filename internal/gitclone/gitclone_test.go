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

func TestSameRemoteMatchesOneRepoOverSSHAndHTTPS(t *testing.T) {
	cases := []struct {
		a, b string
		same bool
	}{
		{"git@github.com:acme/app.git", "https://github.com/acme/app", true},
		{"ssh://git@GitHub.com:22/acme/app.git", "https://github.com/acme/app.git/", true},
		{"https://github.com/acme/app.git", "github.com:acme/app", true},
		{"git@github.com:acme/app.git", "git@github.com:acme/other.git", false},
		{"git@github.com:acme/app.git", "git@gitlab.com:acme/app.git", false},
		{"/srv/app", "git@github.com:srv/app.git", false},
	}
	for _, c := range cases {
		if got := SameRemote(c.a, c.b); got != c.same {
			t.Errorf("SameRemote(%q, %q) = %v; want %v, so an https clone on one node and an ssh clone on another count as one repo", c.a, c.b, got, c.same)
		}
	}
}

func TestRemoteURLNeverHandsOutARejectedURLAndNamesALocalOnlyRepo(t *testing.T) {
	repo := t.TempDir()
	if _, err := Git(context.Background(), repo, "init", "-q"); err != nil {
		t.Fatal(err)
	}
	if _, err := RemoteURL(repo); !errors.Is(err, ErrNoNetworkRemote) {
		t.Errorf("a repo without remotes answered %v; want ErrNoNetworkRemote", err)
	}
	Git(context.Background(), repo, "remote", "add", "origin", "https://me:token@github.com/acme/app.git")
	if url, err := RemoteURL(repo); url != "" || err == nil || errors.Is(err, ErrNoNetworkRemote) {
		t.Errorf("a credential URL gave %q, %v; want no URL, so the token never reaches the phone or another node, and a problem other than a missing remote", url, err)
	}
}

func TestFindPrefersTheExactURLThenTheSameRepoInAStableOrder(t *testing.T) {
	repos := map[string]string{"https://github.com/acme/app.git": "/https", "git@github.com:acme/app.git": "/ssh", "ssh://git@github.com/acme/app": "/other"}
	if repo, _ := Find(repos, "https://github.com/acme/app.git"); repo != "/https" {
		t.Errorf("Find picked %s for the exact URL; want /https", repo)
	}
	for range 20 {
		if repo, _ := Find(repos, "https://GitHub.com/acme/app"); repo != "/ssh" {
			t.Fatalf("Find picked %s; want the first sorted match every time, so preflight and takeover use one checkout", repo)
		}
	}
}
