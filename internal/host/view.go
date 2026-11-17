package host

import (
	"cmp"
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/afonsodelgado/factory/internal/factory"
	"github.com/afonsodelgado/factory/internal/state"
	"github.com/afonsodelgado/factory/internal/tmux"
)

const (
	viewPrefix = "view-"
	staleView  = time.Minute
)

func Incarnation(f factory.Factory) string {
	if f.Entry != nil && f.Entry.Incarnation != "" {
		return f.Entry.Incarnation
	}
	return f.Key()
}

func repoName(repo string) string {
	url, err := Git(repo, "config", "--get", "remote.origin.url")
	if err != nil || url == "" {
		return filepath.Base(repo)
	}
	return strings.TrimSuffix(filepath.Base(url), ".git")
}

func stillOpen(st *state.State, id string, since time.Time) bool {
	if st == nil {
		return false
	}
	return slices.ContainsFunc(st.OpenAttention(), func(a state.Attention) bool { return a.ID == id && a.CreatedAt.Time.Equal(since) })
}

func ViewSession(id string) string { return viewPrefix + id }

func PrepareView(tm tmux.Client, f factory.Factory, role, token string) (string, error) {
	sweepViews(tm)
	panes, err := tm.Panes()
	if err != nil {
		return "", err
	}
	target, ok := attachable(panes, f, role, token)
	if !ok {
		return "", fmt.Errorf("%s has no %s pane with that identity on this server any more; wait for the list to refresh and try again", f.Label(), cmp.Or(role, "session"))
	}
	id := uuid.NewString()
	name := ViewSession(id)
	placeholder, err := tm.Run("new-session", "-d", "-s", name, "-P", "-F", "#{window_id}", "exec sleep 60")
	if err != nil {
		return "", err
	}
	steps := [][]string{
		{"link-window", "-s", target.WindowID, "-t", name + ":"},
		{"kill-window", "-t", placeholder},
		{"select-window", "-t", target.WindowID},
		{"select-pane", "-t", target.ID},
		{"set-option", "-t", name, "status", "off"},
		{"set-hook", "-t", name, "client-attached", "set-option -t " + name + " destroy-unattached on"},
	}
	for _, step := range steps {
		if _, err := tm.Run(step...); err != nil {
			tm.Run("kill-session", "-t", "="+name)
			return "", err
		}
	}
	return id, nil
}

func attachable(panes []tmux.Pane, f factory.Factory, role, token string) (tmux.Pane, bool) {
	if role == "" {
		p, ok := FactoryPane(panes, f.Repo, f.Slug)
		return p, ok && !p.Dead && p.Token == token
	}
	return stagePaneOf(panes, f, role, token)
}

func sweepViews(tm tmux.Client) {
	out, err := tm.Run("list-sessions", "-F", "#{session_name} #{session_attached} #{session_created}")
	if err != nil {
		return
	}
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 3 || !strings.HasPrefix(fields[0], viewPrefix) || fields[1] != "0" {
			continue
		}
		if created, err := strconv.ParseInt(fields[2], 10, 64); err == nil && time.Since(time.Unix(created, 0)) > staleView {
			tm.Run("kill-session", "-t", "="+fields[0])
		}
	}
}
