package hub

import (
	"os"
	"path/filepath"

	"github.com/afonsodelgado/factory/internal/factory"
	"github.com/afonsodelgado/factory/internal/host"
	"github.com/afonsodelgado/factory/internal/tmux"
)

func (e *Engine) Tag(pane, repo, slug string) error {
	for name, value := range map[string]string{"@factory_repo": repo, "@factory_slug": slug} {
		if err := e.TM.SetPaneOption(pane, name, value); err != nil {
			return err
		}
	}
	return nil
}

func (e *Engine) Select(l *Layout, incoming string) error {
	if incoming == l.Slot {
		return nil
	}
	panes, err := e.TM.Panes()
	if err != nil {
		return err
	}
	in, ok := byID(panes, incoming)
	if !ok {
		return nil
	}
	out, _ := byID(panes, l.Slot)
	if err := e.exchange(*l, in); err != nil {
		return err
	}
	if err := e.TM.RenameWindow(in.WindowID, parkingName(out)); err != nil {
		return err
	}
	l.Slot = incoming
	return nil
}

func (e *Engine) SlotViewer() (string, bool) {
	panes, err := e.TM.Panes()
	if err != nil {
		return "", false
	}
	for _, p := range panes {
		if e.Owns(p) && p.UI == "slot" {
			return p.ID, true
		}
	}
	return "", false
}

func parkingName(p tmux.Pane) string {
	if p.Slug == "" {
		return SlotParking
	}
	return host.ParkName(p.Slug)
}

func (e *Engine) Adopt(l Layout, repos []string) ([]string, error) {
	sessions, err := e.TM.Sessions()
	if err != nil {
		return nil, err
	}
	worktrees := worktreeRepos(repos)
	panes, err := e.TM.Panes()
	if err != nil {
		return nil, err
	}
	var adopted []string
	for _, session := range sessions {
		candidates, legacy := worktrees[session]
		if !legacy || session == e.Session {
			continue
		}
		pane, ok := firstPane(panes, session)
		if !ok {
			continue
		}
		repo, known := repoOfPane(candidates, pane)
		if !known {
			continue
		}
		if _, tagged := host.FactoryPane(panes, repo, session); tagged {
			continue
		}
		if err := e.Tag(pane.ID, repo, session); err != nil {
			return adopted, err
		}
		if err := e.TM.Break(pane.ID, e.Session, host.ParkName(session)); err != nil {
			return adopted, err
		}
		adopted = append(adopted, session)
	}
	if len(adopted) > 0 {
		return adopted, e.FitParking(l)
	}
	return adopted, nil
}

func worktreeRepos(repos []string) map[string][]string {
	byName := map[string][]string{}
	for _, repo := range repos {
		entries, err := os.ReadDir(factory.WorktreesDir(repo))
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() {
				byName[entry.Name()] = append(byName[entry.Name()], repo)
			}
		}
	}
	return byName
}

func repoOfPane(candidates []string, p tmux.Pane) (string, bool) {
	if len(candidates) == 1 {
		return candidates[0], true
	}
	cwd := resolved(p.Path)
	for _, repo := range candidates {
		if within(cwd, resolved(repo)) {
			return repo, true
		}
	}
	return "", false
}

func resolved(path string) string {
	if real, err := filepath.EvalSymlinks(path); err == nil {
		return real
	}
	return filepath.Clean(path)
}

func firstPane(panes []tmux.Pane, session string) (tmux.Pane, bool) {
	for _, p := range panes {
		if p.Session == session {
			return p, true
		}
	}
	return tmux.Pane{}, false
}

type NewPane = host.NewPane
type StagePane = host.StagePane

func (e *Engine) panes() *host.Panes { return &host.Panes{TM: e.TM, Session: e.Session} }

func (e *Engine) NewFactoryPane(l Layout, n NewPane) (string, error) {
	pane, err := e.panes().NewFactoryPane(n)
	if err != nil {
		return pane, err
	}
	return pane, e.FitParking(l)
}

func (e *Engine) NewStagePane(l Layout, s StagePane) (string, error) {
	pane, err := e.panes().NewStagePane(s)
	if err != nil {
		return pane, err
	}
	return pane, e.FitParking(l)
}

func (e *Engine) Type(pane, text string) error { return e.panes().Type(pane, text) }

func (e *Engine) KillStages(repo, slug, role string) error {
	panes, err := e.TM.Panes()
	if err != nil {
		return err
	}
	for _, p := range panes {
		ofThisFactory := p.Repo == repo && p.StageOf == slug
		if !ofThisFactory || (role != "" && p.Stage != role) {
			continue
		}
		if err := e.killStage(p); err != nil {
			return err
		}
	}
	return nil
}

func (e *Engine) hideStageOf(l *Layout, repo, slug string) error {
	panes, err := e.TM.Panes()
	if err != nil {
		return err
	}
	if p, shown := byID(panes, l.Stage); shown && p.Repo == repo && p.StageOf == slug {
		return e.HideStage(l)
	}
	return nil
}

func (e *Engine) killStage(p tmux.Pane) error {
	if p.WindowID == e.home.WindowID {
		return e.TM.KillPane(p.ID)
	}
	return e.TM.KillWindow(p.WindowID)
}

func (e *Engine) Unshow(l *Layout, repo, slug string) error {
	if err := e.hideStageOf(l, repo, slug); err != nil {
		return err
	}
	panes, err := e.TM.Panes()
	if err != nil {
		return err
	}
	pane, ok := host.FactoryPane(panes, repo, slug)
	if !ok || pane.ID != l.Slot {
		return nil
	}
	if err := e.ensureSlotViewer(); err != nil {
		return err
	}
	if viewer, found := e.SlotViewer(); found {
		return e.Select(l, viewer)
	}
	return nil
}

func (m *Model) unshow(f factory.Factory) {
	if m.Engine != nil && f.Remote == nil {
		m.report(m.Engine.Unshow(&m.Layout, f.Repo, f.Slug))
	}
}

func (m *Model) fitNewPanes() {
	if m.Engine == nil {
		return
	}
	seen := map[string]bool{}
	arrived := false
	for _, p := range m.snap.Panes {
		seen[p.ID] = true
		arrived = arrived || !m.parked[p.ID]
	}
	m.parked = seen
	if arrived {
		m.Engine.FitParking(m.Layout)
	}
}
