package host

import (
	"cmp"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/afonsodelgado/factory/internal/factory"
	"github.com/afonsodelgado/factory/internal/remote"
	"github.com/afonsodelgado/factory/internal/state"
	"github.com/afonsodelgado/factory/internal/tmux"
)

const (
	viewPrefix   = "view-"
	activityKept = 200
	staleView    = time.Minute
)

func Incarnation(f factory.Factory) string {
	if f.Entry != nil && f.Entry.Incarnation != "" {
		return f.Entry.Incarnation
	}
	return f.Key()
}

func (m *Runtime) View() remote.View {
	home, _ := os.UserHomeDir()
	view := remote.View{Home: home, Now: m.Now(), Factories: []remote.Factory{}, Opens: m.Opens()}
	if m.Pages != nil {
		view.Review = remote.ReviewHome{Socket: m.Config.ReviewSocket(), Token: m.Pages.Token()}
	}
	names := map[string]string{}
	for _, it := range m.snap.Items {
		w := m.wire(it)
		if _, known := names[w.Repo]; !known {
			names[w.Repo] = repoName(w.Repo)
		}
		w.RepoName = names[w.Repo]
		view.Factories = append(view.Factories, w)
	}
	return view
}

func repoName(repo string) string {
	url, err := Git(repo, "config", "--get", "remote.origin.url")
	if err != nil || url == "" {
		return filepath.Base(repo)
	}
	return strings.TrimSuffix(filepath.Base(url), ".git")
}

func (m *Runtime) wire(it Item) remote.Factory {
	f := it.Factory
	w := remote.Factory{
		ID: f.Label(), Slug: f.Slug, Incarnation: Incarnation(f), Repo: f.Repo, Worktree: f.Worktree, ReviewKey: ReviewKey(f),
		State: f.State, StateMod: f.StateMod, Entry: f.Entry, Status: it.Status, Unblocked: it.Unblocked, Hook: it.Hook,
		Pane: wirePane(it.Pane),
	}
	if f.StateErr != nil {
		w.StateError = f.StateErr.Error()
	}
	if it.Stage != nil {
		w.Stage = &remote.Stage{Run: it.Stage.Run, Pane: wirePane(it.Stage.Pane)}
	}
	if m.Config.StateDir != "" {
		logged, _ := state.ReadActivity(state.ActivityPath(m.Config.ActivityDir(), f.Repo, f.Slug))
		w.Activity = logged[max(0, len(logged)-activityKept):]
	}
	return w
}

func wirePane(p *tmux.Pane) *remote.Pane {
	if p == nil || p.Dead {
		return nil
	}
	return &remote.Pane{ID: p.ID, Token: p.Token, Path: p.Path}
}

type AnswerRequest struct {
	Key        string
	ItemID     string
	ItemSince  time.Time
	Text       string
	LeavesOpen bool
}

type AnswerReply struct {
	At time.Time
}

func (m *Runtime) TypeAnswer(r AnswerRequest) (AnswerReply, error) {
	it, ok := m.snap.Find(r.Key)
	if !ok {
		return AnswerReply{}, fmt.Errorf("no factory %s", r.Key)
	}
	if err := m.unverified(r.Key); err != nil {
		return AnswerReply{}, err
	}
	if !stillOpen(it.Factory.State, r.ItemID, r.ItemSince) {
		return AnswerReply{}, fmt.Errorf("%s is no longer open in %s; nothing was sent", r.ItemID, it.Factory.Label())
	}
	at := m.Now()
	reason, err := Deliver(m.Typist, Target{Label: it.Factory.Label(), Pane: it.Pane, Hook: it.Hook}, r.Text)
	switch {
	case err != nil:
		return AnswerReply{}, err
	case reason != "":
		return AnswerReply{}, errors.New(reason)
	}
	m.typedAt[it.Pane.ID] = at
	if m.Loader != nil {
		m.Loader.Note(it.Factory, "answered "+r.ItemID+" from a laptop")
	}
	return AnswerReply{At: at}, nil
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
