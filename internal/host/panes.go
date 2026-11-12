package host

import (
	"os"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/afonsodelgado/factory/internal/tmux"
)

const pasteSettle = 150 * time.Millisecond

type Panes struct {
	TM      tmux.Client
	Session string
	Openers string
}

type NewPane struct {
	Repo string
	Slug string
	Dir  string
	Env  []string
}

type StagePane struct {
	Repo  string
	Slug  string
	Role  string
	Token string
	Dir   string
}

func ParkName(slug string) string { return "p-" + slug }

func StageParkName(slug, role string) string { return "s-" + slug + "-" + role }

func loginShell() string {
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/sh"
	}
	return "exec " + shell + " -l"
}

func (e *Panes) NewFactoryPane(n NewPane) (string, error) {
	token := uuid.NewString()
	env := slices.Concat(n.Env, []string{"FACTORY_PANE_TOKEN=" + token}, e.serverEnv())
	pane, err := e.TM.NewWindow(tmux.Window{Session: e.Session, Name: ParkName(n.Slug), Dir: n.Dir, Env: env, Command: loginShell()})
	if err != nil {
		return "", err
	}
	return pane, e.tag(pane, [][2]string{{"@factory_repo", n.Repo}, {"@factory_slug", n.Slug}, {"@factory_token", token}})
}

func (e *Panes) NewStagePane(s StagePane) (string, error) {
	env := slices.Concat([]string{"FACTORY=1", "FACTORY_PANE_TOKEN=" + s.Token, factoryBin()}, e.serverEnv())
	pane, err := e.TM.NewWindow(tmux.Window{Session: e.Session, Name: StageParkName(s.Slug, s.Role), Dir: s.Dir, Env: env, Command: loginShell()})
	if err != nil {
		return "", err
	}
	return pane, e.tag(pane, [][2]string{{"@factory_ui", "stage"}, {"@factory_repo", s.Repo}, {"@factory_stage_of", s.Slug}, {"@factory_stage", s.Role}, {"@factory_token", s.Token}})
}

func (e *Panes) serverEnv() []string {
	if e.Openers == "" {
		return nil
	}
	return []string{"FACTORY_SERVER=1", "PATH=" + e.Openers + string(os.PathListSeparator) + os.Getenv("PATH")}
}

func (e *Panes) tag(pane string, options [][2]string) error {
	for _, o := range options {
		if err := e.TM.SetPaneOption(pane, o[0], o[1]); err != nil {
			return err
		}
	}
	return nil
}

func (e *Panes) Type(pane, text string) error {
	if err := e.TM.Paste(pane, "factory-"+strings.TrimPrefix(pane, "%"), text); err != nil {
		return err
	}
	time.Sleep(pasteSettle)
	return e.TM.SendKeys(pane, "Enter")
}

func (e *Panes) KillStages(repo, slug, role string) error {
	panes, err := e.TM.Panes()
	if err != nil {
		return err
	}
	for _, p := range panes {
		if p.Repo != repo || p.StageOf != slug || (role != "" && p.Stage != role) {
			continue
		}
		if inHubWindow(panes, p) {
			err = e.TM.KillPane(p.ID)
		} else {
			err = e.TM.KillWindow(p.WindowID)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func inHubWindow(panes []tmux.Pane, p tmux.Pane) bool {
	return slices.ContainsFunc(panes, func(q tmux.Pane) bool { return q.UI == "selector" && q.WindowID == p.WindowID })
}

func (e *Panes) Pause(repo, slug string) error {
	if err := e.KillStages(repo, slug, ""); err != nil {
		return err
	}
	panes, err := e.TM.Panes()
	if err != nil {
		return err
	}
	p, ok := FactoryPane(panes, repo, slug)
	if !ok {
		return nil
	}
	return e.TM.KillWindow(p.WindowID)
}
