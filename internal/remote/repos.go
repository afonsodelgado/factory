package remote

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
)

func GitURL(repo, choice string) (string, error) {
	names, err := git(context.Background(), repo, "remote")
	if err != nil {
		return "", err
	}
	remotes := strings.Fields(names)
	name := choice
	switch {
	case choice != "" && !slices.Contains(remotes, choice):
		return "", fmt.Errorf("%s has no git remote named %s; git remote -v lists them", repo, choice)
	case choice != "":
	case slices.Contains(remotes, "origin"):
		name = "origin"
	case len(remotes) == 1:
		name = remotes[0]
	case len(remotes) == 0:
		return "", fmt.Errorf("%s has no git remote, and a remote factory clones from one. Push it somewhere the server can read, add that with git remote add origin <url>, and try again", repo)
	default:
		return "", fmt.Errorf("%s has the git remotes %s and none is origin; pick one with --git-remote", repo, strings.Join(remotes, ", "))
	}
	raw, err := git(context.Background(), repo, "config", "--get", "remote."+name+".url")
	if err != nil {
		return "", err
	}
	return raw, CheckCloneURL(raw)
}

func CheckCloneURL(raw string) error {
	if strings.HasPrefix(raw, "-") {
		return fmt.Errorf("%q is not a git URL", raw)
	}
	if u, err := url.Parse(raw); err == nil && u.Scheme != "" {
		switch {
		case u.Scheme == "file":
			return fmt.Errorf("%s is a path on this machine, which the server can't read; use a network git remote", raw)
		case u.User != nil && (u.Scheme == "http" || u.Scheme == "https"):
			return fmt.Errorf("the git remote URL carries credentials, which would be copied to the server; remove them from the URL and give the server its own git credentials")
		}
		return nil
	}
	host, _, scpLike := strings.Cut(raw, ":")
	if !scpLike || strings.ContainsAny(host, "/\\") {
		return fmt.Errorf("%s is a path on this machine, which the server can't read; use a network git remote", raw)
	}
	return nil
}

func CloneDir(stateDir, gitURL string) string {
	sum := sha256.Sum256([]byte(gitURL))
	return filepath.Join(stateDir, "repos", hex.EncodeToString(sum[:8]))
}

func EnsureClone(ctx context.Context, stateDir, gitURL string) (string, error) {
	if err := CheckCloneURL(gitURL); err != nil {
		return "", err
	}
	dir := CloneDir(stateDir, gitURL)
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return "", err
	}
	lock, err := os.OpenFile(dir+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return "", err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return "", err
	}
	if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
		if _, err := git(ctx, dir, "fetch", "--prune", "origin"); err != nil {
			return "", err
		}
		_, err := git(ctx, dir, "merge", "--ff-only", "@{upstream}")
		return dir, err
	}
	partial := dir + ".partial"
	os.RemoveAll(partial)
	if _, err := git(ctx, filepath.Dir(dir), "clone", "--", gitURL, partial); err != nil {
		return "", err
	}
	return dir, os.Rename(partial, dir)
}

func git(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	text := strings.TrimSpace(string(out))
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return text, fmt.Errorf("git %s: %s", args[0], text)
	}
	return text, err
}
