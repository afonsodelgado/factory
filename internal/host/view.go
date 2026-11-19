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
	hub, inHub := hubShowing(panes, target)
	if inHub && hub.Attached {
		return "", fmt.Errorf("%s is on screen in the hub on that machine; it shows here once that hub moves to another factory", f.Label())
	}
	id := uuid.NewString()
	name := ViewSession(id)
	placeholder, err := tm.Run("new-session", "-d", "-s", name, "-P", "-F", "#{window_id}", "exec sleep 60")
	if err != nil {
		return "", err
	}
	link := []string{"link-window", "-s", target.ID, "-t", "=" + name + ":"}
	bring := [][]string{link}
	if inHub {
		bring = [][]string{{"break-pane", "-d", "-s", target.ID, "-t", "=" + hub.Session + ":", "-n", parkingName(target)}}
		for _, split := range splitsBeside(panes, hub, target) {
			bring = append(bring, []string{"join-pane", "-d", "-h", "-s", split, "-t", target.ID})
		}
		bring = append(bring, link)
	}
	steps := [][]string{
		{"kill-window", "-t", placeholder},
		{"select-pane", "-t", target.ID},
		{"set-option", "-t", name, "status", "off"},
		{"set-hook", "-t", name, "client-attached", "set-option -t " + name + " destroy-unattached on"},
	}
	err = tm.Batch(bring...)
	for _, step := range steps {
		if err != nil {
			break
		}
		_, err = tm.Run(step...)
	}
	if err == nil && !viewHolds(tm, name, target.ID) {
		err = fmt.Errorf("%s moved while its view was prepared; try again", f.Label())
	}
	if err != nil {
		tm.Run("kill-session", "-t", "="+name)
		return "", err
	}
	return id, nil
}

func hubShowing(panes []tmux.Pane, p tmux.Pane) (tmux.Pane, bool) {
	for _, q := range panes {
		if q.UI == "selector" && q.WindowID == p.WindowID && !isView(q.Session) {
			return q, true
		}
	}
	return tmux.Pane{}, false
}

func splitsBeside(panes []tmux.Pane, hub, target tmux.Pane) []string {
	if target.Stage != "" {
		return nil
	}
	var splits []string
	for _, p := range panes {
		if p.Session == hub.Session && p.WindowID == hub.WindowID && p.ID != target.ID && p.UI == "" && p.Slug == "" {
			splits = append(splits, p.ID)
		}
	}
	return splits
}

func parkingName(p tmux.Pane) string {
	if p.Stage != "" {
		return StageParkName(p.StageOf, p.Stage)
	}
	return ParkName(p.Slug)
}

func viewHolds(tm tmux.Client, view, pane string) bool {
	out, err := tm.Run("list-panes", "-s", "-t", "="+view, "-F", "#{pane_id} #{@factory_ui}")
	if err != nil {
		return false
	}
	lines := strings.Split(out, "\n")
	held := slices.ContainsFunc(lines, func(l string) bool { return strings.HasPrefix(l, pane+" ") })
	hub := slices.ContainsFunc(lines, func(l string) bool { return strings.HasSuffix(l, " selector") })
	return held && !hub
}

func isView(session string) bool { return strings.HasPrefix(session, viewPrefix) }

func Viewed(panes []tmux.Pane, id string) bool {
	return id != "" && slices.ContainsFunc(panes, func(p tmux.Pane) bool { return p.ID == id && isView(p.Session) })
}

func EndViews(tm tmux.Client, panes []tmux.Pane, ids ...string) {
	for _, p := range panes {
		if isView(p.Session) && p.ID != "" && slices.Contains(ids, p.ID) {
			tm.Run("kill-session", "-t", "="+p.Session)
		}
	}
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
		if len(fields) != 3 || !isView(fields[0]) || fields[1] != "0" {
			continue
		}
		if created, err := strconv.ParseInt(fields[2], 10, 64); err == nil && time.Since(time.Unix(created, 0)) > staleView {
			tm.Run("kill-session", "-t", "="+fields[0])
		}
	}
}
