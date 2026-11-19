package gitclone

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
)

var ErrNoNetworkRemote = errors.New("no network git remote")

type localOnly string

func (e localOnly) Error() string        { return string(e) }
func (e localOnly) Is(target error) bool { return target == ErrNoNetworkRemote }

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
		return "", localOnly(filepath.Base(repo) + " has no network git remote, so no other node can have it and its factories can't move. Push it to a remote, clone it on another node and run factory init there")
	default:
		return "", fmt.Errorf("%s has the git remotes %s and none is origin; rename the one other nodes clone from to origin with git remote rename", repo, strings.Join(remotes, ", "))
	}
	raw, err := Git(context.Background(), repo, "config", "--get", "remote."+name+".url")
	if err != nil {
		return "", err
	}
	if err := CheckURL(raw); err != nil {
		return "", err
	}
	return raw, nil
}

func CheckURL(raw string) error {
	if strings.HasPrefix(raw, "-") {
		return fmt.Errorf("%q is not a git URL", raw)
	}
	if u, err := url.Parse(raw); err == nil && u.Scheme != "" {
		switch {
		case u.Scheme == "file":
			return localOnly(raw + " is a path on this machine, which the server can't read; use a network git remote")
		case u.User != nil && (u.Scheme == "http" || u.Scheme == "https"):
			return fmt.Errorf("the git remote URL carries credentials, which would be copied to the server; remove them from the URL and give the server its own git credentials")
		}
		return nil
	}
	host, _, scpLike := strings.Cut(raw, ":")
	if !scpLike || strings.ContainsAny(host, "/\\") {
		return localOnly(raw + " is a path on this machine, which the server can't read; use a network git remote")
	}
	return nil
}

func SameRemote(a, b string) bool {
	hostA, pathA, okA := remoteParts(a)
	hostB, pathB, okB := remoteParts(b)
	return a == b || (okA && okB && hostA == hostB && pathA == pathB)
}

func Find(repos map[string]string, gitURL string) (string, bool) {
	if repo, exact := repos[gitURL]; exact {
		return repo, true
	}
	for _, origin := range slices.Sorted(maps.Keys(repos)) {
		if repo := repos[origin]; SameRemote(origin, gitURL) {
			return repo, true
		}
	}
	return "", false
}

func remoteParts(raw string) (host, path string, ok bool) {
	if u, err := url.Parse(raw); err == nil && u.Scheme != "" && u.Host != "" {
		return strings.ToLower(u.Hostname()), repoPath(u.Path), true
	}
	login, rest, scpLike := strings.Cut(raw, ":")
	if !scpLike || strings.ContainsAny(login, "/\\") {
		return "", "", false
	}
	host = login
	if at := strings.LastIndex(login, "@"); at >= 0 {
		host = login[at+1:]
	}
	return strings.ToLower(host), repoPath(rest), true
}

func repoPath(path string) string {
	return strings.TrimSuffix(strings.Trim(path, "/"), ".git")
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
