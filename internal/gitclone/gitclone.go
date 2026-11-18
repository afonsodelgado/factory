package gitclone

import (
	"cmp"
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
	"time"
)

const Timeout = 10 * time.Minute

func Origin(repo string) string {
	url, err := Git(context.Background(), repo, "config", "--get", "remote.origin.url")
	if err != nil {
		return ""
	}
	return url
}

func RemoteURL(repo string) (string, error) {
	names, err := Git(context.Background(), repo, "remote")
	if err != nil {
		return "", err
	}
	remotes := strings.Fields(names)
	name := "origin"
	switch {
	case slices.Contains(remotes, "origin"):
	case len(remotes) == 1:
		name = remotes[0]
	case len(remotes) == 0:
		return "", fmt.Errorf("%s has no git remote, and another node clones from one. Push it somewhere that node can read, add that with git remote add origin <url>, and try again", repo)
	default:
		return "", fmt.Errorf("%s has the git remotes %s and none is origin; rename the one other nodes should clone to origin with git remote rename", repo, strings.Join(remotes, ", "))
	}
	raw, err := Git(context.Background(), repo, "config", "--get", "remote."+name+".url")
	if err != nil {
		return "", err
	}
	return raw, CheckURL(raw)
}

func CheckURL(raw string) error {
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

func Dir(stateDir, gitURL string) string {
	sum := sha256.Sum256([]byte(gitURL))
	return filepath.Join(resolved(stateDir), "repos", hex.EncodeToString(sum[:8]))
}

func resolved(path string) string {
	if real, err := filepath.EvalSymlinks(path); err == nil {
		return real
	}
	return filepath.Clean(path)
}

func Ensure(ctx context.Context, stateDir, gitURL string) (string, error) {
	if err := CheckURL(gitURL); err != nil {
		return "", err
	}
	dir := Dir(stateDir, gitURL)
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
		if _, err := Git(ctx, dir, "fetch", "--prune", "origin"); err != nil {
			return "", err
		}
		_, err := Git(ctx, dir, "merge", "--ff-only", "@{upstream}")
		return dir, err
	}
	partial := dir + ".partial"
	os.RemoveAll(partial)
	if _, err := Git(ctx, filepath.Dir(dir), "clone", "--", gitURL, partial); err != nil {
		if ctx.Err() != nil {
			return "", err
		}
		answer, _ := strings.CutPrefix(err.Error(), "git clone: ")
		return "", fmt.Errorf("could not clone %s. git answered: %s", gitURL, answer)
	}
	return dir, os.Rename(partial, dir)
}

func Git(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.Output()
	text := strings.TrimSpace(string(out))
	if ctxErr := ctx.Err(); err != nil && ctxErr != nil {
		return text, fmt.Errorf("git %s: %w", args[0], ctxErr)
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return text, fmt.Errorf("git %s: %s", args[0], cmp.Or(strings.TrimSpace(string(exit.Stderr)), text))
	}
	return text, err
}
