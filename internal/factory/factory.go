package factory

import (
	"bytes"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/afonsodelgado/factory/internal/state"
)

const (
	StartTimeout = 5 * time.Minute
	LaunchGrace  = 30 * time.Second
)

type Factory struct {
	Repo      string
	Slug      string
	ID        string
	Dir       string
	Worktree  string
	State     *state.State
	StateErr  error
	StateMod  time.Time
	Heartbeat time.Time
	Entry     *state.Entry
}

func (f Factory) Key() string {
	return state.Key(f.Repo, f.Slug)
}

func (f Factory) RepoName() string {
	return filepath.Base(f.Repo)
}

func (f Factory) Label() string {
	switch {
	case f.State != nil && f.State.ID != "":
		return f.State.ID
	case f.ID != "":
		return f.ID
	}
	return f.Slug
}

func (f Factory) Title() string {
	if f.State != nil && f.State.Title != "" {
		return f.State.Title
	}
	if f.Entry != nil {
		return f.Entry.Title
	}
	return ""
}

func (f Factory) Done() bool {
	return f.State != nil && f.State.Done()
}

func (f Factory) Archived() bool {
	return f.Entry != nil && f.Entry.Archived
}

func (f Factory) CreatedAt() time.Time {
	if f.Entry != nil && !f.Entry.CreatedAt.IsZero() {
		return f.Entry.CreatedAt
	}
	if f.State != nil {
		for _, h := range f.State.History {
			if !h.At.IsZero() {
				return h.At.Time
			}
		}
	}
	return f.StateMod
}

func (f Factory) SessionID() string {
	if f.Entry != nil && f.Entry.SessionID != "" {
		return f.Entry.SessionID
	}
	if f.State != nil {
		return f.State.SessionID
	}
	return ""
}

func (f Factory) WaitingOn() string {
	if f.State == nil {
		return ""
	}
	return f.State.WaitingOn
}

func WorktreesDir(repo string) string {
	return filepath.Join(repo, ".claude", "worktrees")
}

func existingWorktree(repo, id string) (string, bool) {
	wt := filepath.Join(WorktreesDir(repo), strings.ToLower(id))
	if info, err := os.Stat(wt); id == "" || err != nil || !info.IsDir() {
		return "", false
	}
	return wt, true
}

func Discover(repos []string, reg *state.Registry) []Factory {
	var found []Factory
	keys := map[string]bool{}
	for _, repo := range repos {
		seen := map[string]bool{}
		for _, path := range statePaths(repo) {
			real, err := filepath.EvalSymlinks(path)
			if err != nil || seen[real] {
				continue
			}
			seen[real] = true
			f := load(repo, path, real)
			if pinned := reg.FindDir(repo, filepath.Dir(real)); pinned != nil {
				f.Slug = pinned.Slug
				if f.State == nil || f.State.Worktree == "" {
					f.Worktree = ""
				}
			}
			if keys[f.Key()] {
				continue
			}
			keys[f.Key()] = true
			f.Entry = reg.Find(f.Repo, f.Slug)
			found = append(found, f)
		}
		for i := range reg.Factories {
			e := &reg.Factories[i]
			if e.Repo != repo || !e.Created || keys[e.Key()] {
				continue
			}
			keys[e.Key()] = true
			found = append(found, Factory{Repo: repo, Slug: e.Slug, Worktree: filepath.Join(WorktreesDir(repo), e.Slug), Entry: e})
		}
	}
	sort.SliceStable(found, func(i, j int) bool {
		a, b := found[i].CreatedAt(), found[j].CreatedAt()
		if !a.Equal(b) {
			return a.Before(b)
		}
		return found[i].Key() < found[j].Key()
	})
	return found
}

func statePaths(repo string) []string {
	inWorktrees, _ := filepath.Glob(filepath.Join(WorktreesDir(repo), "*", ".claude", "factory", "*", "state.json"))
	legacy, _ := filepath.Glob(filepath.Join(repo, ".claude", "factory", "*", "state.json"))
	return append(inWorktrees, legacy...)
}

func load(repo, path, real string) Factory {
	dir := filepath.Dir(path)
	f := Factory{Repo: repo, ID: filepath.Base(dir), Dir: dir}
	if info, err := os.Stat(path); err == nil {
		f.StateMod = info.ModTime()
	}
	if info, err := os.Stat(filepath.Join(dir, ".heartbeat")); err == nil {
		f.Heartbeat = info.ModTime()
	}
	f.State, f.StateErr = readState(path)
	f.Worktree = worktreeOf(repo, path, real, f)
	f.Slug = strings.ToLower(f.ID)
	if f.Worktree != "" {
		f.Slug = filepath.Base(f.Worktree)
	}
	return f
}

func readState(path string) (*state.State, error) {
	data, err := os.ReadFile(path)
	if err != nil || len(bytes.TrimSpace(data)) == 0 {
		return nil, nil
	}
	return state.ParseState(data)
}

func worktreeOf(repo, path, real string, f Factory) string {
	if f.State != nil && f.State.Worktree != "" {
		return f.State.Worktree
	}
	if wt, ok := ownWorktree(repo, path, real); ok {
		return wt
	}
	if wt, ok := existingWorktree(repo, f.ID); ok {
		return wt
	}
	return ""
}

func ownWorktree(repo, path, real string) (string, bool) {
	rel, err := filepath.Rel(WorktreesDir(repo), path)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", false
	}
	wt := filepath.Join(WorktreesDir(repo), strings.Split(rel, string(filepath.Separator))[0])
	realWT, err := filepath.EvalSymlinks(wt)
	if err != nil || !strings.HasPrefix(real, realWT+string(filepath.Separator)) {
		return "", false
	}
	return wt, true
}
