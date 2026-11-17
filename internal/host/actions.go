package host

import (
	"fmt"

	"github.com/afonsodelgado/factory/internal/factory"
	"github.com/afonsodelgado/factory/internal/state"
)

type ArchiveRequest struct {
	Key    string
	Remove bool
}

func (m *Runtime) Archive(r ArchiveRequest) error {
	it, ok := m.snap.Find(r.Key)
	if !ok || m.Loader == nil {
		return fmt.Errorf("no factory %s", r.Key)
	}
	movedTo := ""
	if r.Remove {
		var err error
		movedTo, err = RemoveWorktree(it.Factory.Repo, it.Factory.Worktree, it.Factory.Dir)
		if err != nil {
			return err
		}
	}
	err := m.Loader.UpdateRegistry(func(reg *state.Registry) {
		entry := reg.Find(it.Factory.Repo, it.Factory.Slug)
		if entry == nil {
			entry = reg.Upsert(state.Entry{Repo: it.Factory.Repo, Slug: it.Factory.Slug, CreatedAt: it.Factory.CreatedAt()})
		}
		entry.Archived = true
		if movedTo != "" {
			entry.Dir = movedTo
		}
	})
	if err == nil {
		m.Loader.Note(it.Factory, "archived")
	}
	return err
}

func (m *Runtime) Unarchive(it Item) error {
	if m.Loader == nil {
		return nil
	}
	err := m.Loader.UpdateRegistry(func(reg *state.Registry) {
		if entry := reg.Find(it.Factory.Repo, it.Factory.Slug); entry != nil {
			entry.Archived = false
		}
	})
	if err == nil {
		m.Loader.Note(it.Factory, "unarchived")
	}
	return err
}

func (m *Runtime) Delete(r DeleteRequest) ([]factory.RemovalStep, error) {
	f, err := m.deletable(r)
	if err != nil {
		return nil, err
	}
	if m.workChanged(f, r.Work) {
		return nil, fmt.Errorf("%s has uncommitted or unpushed work that was not confirmed, or it changed since the confirmation; nothing was deleted. Run the delete again to see it", f.Label())
	}
	return m.DeleteFactory(f)
}

func (m *Runtime) DeleteFactory(f factory.Factory) ([]factory.RemovalStep, error) {
	var closeErr error
	if m.Engine != nil {
		strays := FactoryProcesses(m.currentPanes(), f.Repo, f.Slug)
		closeErr = m.Engine.Pause(f.Repo, f.Slug)
		Terminate(strays)
	}
	steps := factory.Run(factory.PanesClosed(f.Label(), closeErr), factory.PlanRemoval(f, m.Config), m.Loader.UpdateRegistry)
	m.Loader.Forget(f.Key())
	if failed, stopped := factory.FirstFailure(steps); stopped {
		return steps, fmt.Errorf("deleting %s failed at %s %s: %v. Run d again to finish", f.Label(), failed.Name, failed.Detail, failed.Err)
	}
	return steps, nil
}
