package gitclone

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"slices"
	"strings"
)

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
		return "", fmt.Errorf("%s has no git remote, so it stays on this machine. Push it somewhere other nodes can read, add that with git remote add origin <url>, and try again", repo)
	default:
		return "", fmt.Errorf("%s has the git remotes %s and none is origin; rename the one other nodes clone from to origin with git remote rename", repo, strings.Join(remotes, ", "))
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
