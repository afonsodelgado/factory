package host

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"github.com/google/uuid"

	"github.com/afonsodelgado/factory/internal/factory"
	"github.com/afonsodelgado/factory/internal/state"
)

type ReleaseRequest struct {
	Repo      string `json:"repo"`
	Slug      string `json:"slug"`
	To        string `json:"to"`
	Operation string `json:"operation"`
}

type AdoptRequest struct {
	Repo        string `json:"repo"`
	Slug        string `json:"slug"`
	Incarnation string `json:"incarnation"`
}

type Holding struct {
	Generation int64  `json:"generation,omitempty"`
	Unverified string `json:"unverified,omitempty"`
}

type HoldingRequest struct {
	Key string `json:"key"`
	Holding
}

func (m *Runtime) Release(r ReleaseRequest) error {
	f, ok := m.lookup(r.Repo, r.Slug)
	if !ok {
		return fmt.Errorf("no factory %s in %s", r.Slug, r.Repo)
	}
	if err := m.stopProcesses(f); err != nil {
		return err
	}
	m.dropQueued(f)
	err := m.Loader.UpdateRegistry(func(reg *state.Registry) {
		e := reg.Find(r.Repo, r.Slug)
		if e == nil {
			e = reg.Upsert(state.Entry{Repo: r.Repo, Slug: r.Slug, CreatedAt: m.Now(), Incarnation: Incarnation(f)})
		}
		e.MovedTo = r.To
	})
	if err != nil {
		return err
	}
	delete(m.holdings, f.Key())
	m.Loader.Note(f, "moved to "+r.To+" by operation "+r.Operation+"; its processes here are stopped")
	m.snap = m.Loader.Load()
	return nil
}

func (m *Runtime) lookup(repo, slug string) (factory.Factory, bool) {
	if m.Loader == nil {
		return factory.Factory{}, false
	}
	return m.Loader.Lookup(repo, slug)
}

func (m *Runtime) stopProcesses(f factory.Factory) error {
	m.stopBrowser(f.Key())
	m.stopStack(f.Key())
	if m.Engine == nil {
		return nil
	}
	strays := FactoryProcesses(m.currentPanes(), f.Repo, f.Slug)
	err := m.Engine.Pause(f.Repo, f.Slug)
	Terminate(strays)
	return err
}

func (m *Runtime) dropQueued(f factory.Factory) {
	kept := slices.DeleteFunc(slices.Clone(m.typeQueue), func(q QueuedType) bool {
		pane, found := byID(m.snap.Panes, q.Pane)
		return found && OfFactory(pane, f.Repo, f.Slug)
	})
	if len(kept) != len(m.typeQueue) && m.saveQueue(kept) {
		m.typeQueue = kept
	}
}

func (m *Runtime) CanAdopt(r AdoptRequest) error {
	if m.Loader == nil {
		return errors.New("the node is not attached to tmux")
	}
	l := m.Loader
	l.mu.Lock()
	defer l.mu.Unlock()
	worktree := filepath.Join(factory.WorktreesDir(r.Repo), r.Slug)
	incoming, err := os.Stat(worktree)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	known := map[string]string{}
	for _, f := range factory.Discover(l.Config.Repos, l.Registry) {
		known[f.Key()] = f.Worktree
	}
	for _, e := range l.Registry.Factories {
		if e.Incarnation != "" && e.Incarnation == r.Incarnation {
			continue
		}
		if e.Repo == r.Repo && e.Slug == r.Slug {
			return fmt.Errorf("another factory named %s already lives in %s on this node", e.Slug, e.Repo)
		}
		if incoming == nil {
			continue
		}
		existingPath := known[e.Key()]
		if existingPath == "" {
			existingPath = filepath.Join(factory.WorktreesDir(e.Repo), e.Slug)
		}
		existing, err := os.Stat(existingPath)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		if os.SameFile(incoming, existing) {
			return fmt.Errorf("the folder %s is already used by factory %s in %s on this node", worktree, e.Slug, e.Repo)
		}
	}
	return nil
}

func (m *Runtime) Adopt(r AdoptRequest) (string, error) {
	if err := m.CanAdopt(r); err != nil {
		return "", err
	}
	err := m.Loader.UpdateRegistry(func(reg *state.Registry) {
		e := reg.Find(r.Repo, r.Slug)
		if e == nil {
			e = reg.Upsert(state.Entry{Repo: r.Repo, Slug: r.Slug, CreatedAt: m.Now()})
		}
		e.Incarnation, e.MovedTo, e.Paused = r.Incarnation, "", false
	})
	return state.Key(r.Repo, r.Slug), err
}

func (m *Runtime) LaunchMoved(key string) (string, error) {
	m.snap = m.Loader.Load()
	it, ok := m.snap.Find(key)
	switch {
	case !ok || it.Factory.State == nil:
		return "", fmt.Errorf("no state.json for %s in its worktree yet", key)
	case it.Pane != nil && !it.Pane.Dead:
		return it.Pane.ID, nil
	case m.Engine == nil:
		return "", errors.New("the node is not attached to tmux")
	}
	f := it.Factory
	supervisor := f.Supervisor()
	launch := factory.Launch{Repo: f.Repo, Slug: f.Slug, Supervisor: supervisor}
	if supervisor.OnClaude() {
		launch.SessionID = uuid.NewString()
	} else {
		launch.AddDirs = m.WritableDirs(f.Repo, f.Worktree, supervisor)
	}
	err := m.Loader.UpdateRegistry(func(reg *state.Registry) {
		if e := reg.Find(f.Repo, f.Slug); e != nil {
			e.SessionID = launch.SessionID
		}
	})
	if err != nil {
		return "", err
	}
	pane, err := m.Engine.NewFactoryPane(NewPane{Repo: f.Repo, Slug: f.Slug, Dir: f.Worktree, Env: supervisorEnv(supervisor)})
	if err != nil {
		return "", err
	}
	if err := m.typeSupervisor(pane, f, launch.FreshResumeCommand()); err != nil {
		return "", err
	}
	m.Loader.Note(f, "moved here; started a fresh "+supervisor.ToolName()+" supervisor with "+launch.ResumeMessage())
	return pane, nil
}

func (m *Runtime) SetHolding(r HoldingRequest) {
	if r.Holding == (Holding{}) {
		delete(m.holdings, r.Key)
		return
	}
	m.holdings[r.Key] = r.Holding
}

func (m *Runtime) unverified(key string) error {
	if reason := m.holdings[key].Unverified; reason != "" {
		return errors.New(reason)
	}
	return nil
}

func (m *Runtime) movedAway(key string) error {
	if m.Loader == nil {
		return nil
	}
	e, ok := m.Loader.Entry(key)
	if !ok || e.MovedTo == "" {
		return nil
	}
	return fmt.Errorf("%s moved to %s; open it there. factory delete %s removes what is left of it here", e.Slug, e.MovedTo, e.Slug)
}
