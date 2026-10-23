package hub

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/afonsodelgado/factory/internal/factory"
	"github.com/afonsodelgado/factory/internal/tmux"
)

const pasteSettle = 150 * time.Millisecond

func (e *Engine) Tag(pane, repo, slug string) error {
	for name, value := range map[string]string{"@factory_repo": repo, "@factory_slug": slug} {
		if err := e.TM.SetPaneOption(pane, name, value); err != nil {
			return err
		}
	}
	return nil
}

func FactoryPane(panes []tmux.Pane, repo, slug string) (tmux.Pane, bool) {
	for _, p := range panes {
		if p.Repo == repo && p.Slug == slug {
			return p, true
		}
	}
	return tmux.Pane{}, false
}

func LivePane(panes []tmux.Pane, f factory.Factory) factory.Pane {
	p, ok := FactoryPane(panes, f.Repo, f.Slug)
	if !ok || p.Dead {
		return factory.Pane{}
	}
	return factory.Pane{Found: true, Shell: factory.IsShell(p.Command)}
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
	if err := e.TM.Swap(incoming, l.Slot); err != nil {
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
		if p.Session == e.Session && p.UI == "slot" {
			return p.ID, true
		}
	}
	return "", false
}

func parkingName(p tmux.Pane) string {
	if p.Slug == "" {
		return SlotParking
	}
	return ParkName(p.Slug)
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
		if _, tagged := FactoryPane(panes, repo, session); tagged {
			continue
		}
		if err := e.Tag(pane.ID, repo, session); err != nil {
			return adopted, err
		}
		if err := e.TM.Break(pane.ID, e.Session, ParkName(session)); err != nil {
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

type NewPane struct {
	Repo string
	Slug string
	Dir  string
	Env  []string
}

func (e *Engine) NewFactoryPane(l Layout, n NewPane) (string, error) {
	token := uuid.NewString()
	env := slices.Concat(n.Env, []string{"FACTORY_PANE_TOKEN=" + token})
	pane, err := e.TM.NewWindow(tmux.Window{Session: e.Session, Name: ParkName(n.Slug), Dir: n.Dir, Env: env, Command: loginShell()})
	if err != nil {
		return "", err
	}
	if err := e.Tag(pane, n.Repo, n.Slug); err != nil {
		return pane, err
	}
	if err := e.TM.SetPaneOption(pane, "@factory_token", token); err != nil {
		return pane, err
	}
	return pane, e.FitParking(l)
}

type StagePane struct {
	Repo  string
	Slug  string
	Role  string
	Token string
	Dir   string
}

func StageParkName(slug, role string) string {
	return StageParking + slug + "-" + role
}

func (e *Engine) NewStagePane(l Layout, s StagePane) (string, error) {
	env := []string{"FACTORY=1", "FACTORY_PANE_TOKEN=" + s.Token, factoryBin()}
	pane, err := e.TM.NewWindow(tmux.Window{Session: e.Session, Name: StageParkName(s.Slug, s.Role), Dir: s.Dir, Env: env, Command: loginShell()})
	if err != nil {
		return "", err
	}
	options := [][2]string{{"@factory_ui", "stage"}, {"@factory_repo", s.Repo}, {"@factory_stage_of", s.Slug}, {"@factory_stage", s.Role}, {"@factory_token", s.Token}}
	for _, o := range options {
		if err := e.TM.SetPaneOption(pane, o[0], o[1]); err != nil {
			return pane, err
		}
	}
	return pane, e.FitParking(l)
}

func stagePaneOf(panes []tmux.Pane, f factory.Factory, role, token string) (tmux.Pane, bool) {
	for _, p := range panes {
		if p.Repo == f.Repo && p.StageOf == f.Slug && p.Stage == role && p.Token == token {
			return p, true
		}
	}
	return tmux.Pane{}, false
}

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
	if p.WindowName == HubWindow {
		return e.TM.KillPane(p.ID)
	}
	return e.TM.KillWindow(p.WindowID)
}

func loginShell() string {
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/sh"
	}
	return shell + " -l"
}

func (e *Engine) Type(pane, text string) error {
	if err := e.TM.Paste(pane, bufferName(pane), text); err != nil {
		return err
	}
	time.Sleep(pasteSettle)
	return e.TM.SendKeys(pane, "Enter")
}

func bufferName(pane string) string {
	return "factory-" + strings.TrimPrefix(pane, "%")
}

func (e *Engine) Pause(l *Layout, repo, slug string) error {
	if err := e.hideStageOf(l, repo, slug); err != nil {
		return err
	}
	if err := e.KillStages(repo, slug, ""); err != nil {
		return err
	}
	panes, err := e.TM.Panes()
	if err != nil {
		return err
	}
	pane, ok := FactoryPane(panes, repo, slug)
	if !ok {
		return nil
	}
	if pane.ID == l.Slot {
		viewer, found := e.SlotViewer()
		if found {
			if err := e.Select(l, viewer); err != nil {
				return err
			}
		}
		panes, err = e.TM.Panes()
		if err != nil {
			return err
		}
		pane, _ = byID(panes, pane.ID)
	}
	return e.TM.KillWindow(pane.WindowID)
}
