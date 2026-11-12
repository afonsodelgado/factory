package host

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/afonsodelgado/factory/internal/factory"
	"github.com/afonsodelgado/factory/internal/review"
	"github.com/afonsodelgado/factory/internal/tmux"
)

const StackRole = "stack"

type Stack struct {
	URL     string          `json:"url"`
	Script  string          `json:"script"`
	Health  string          `json:"health,omitempty"`
	Cookies []review.Cookie `json:"cookies,omitempty"`
}

func StackPath(factoryDir string) string {
	return filepath.Join(factoryDir, "review", "stack.json")
}

func ReadStack(factoryDir string) (Stack, error) {
	path := StackPath(factoryDir)
	data, err := os.ReadFile(path)
	if err != nil {
		return Stack{}, fmt.Errorf("no stack to start: %w; factory-qa writes it in stack mode", err)
	}
	var s Stack
	if err := json.Unmarshal(data, &s); err != nil {
		return Stack{}, fmt.Errorf("%s: %w", path, err)
	}
	if s.URL == "" || s.Script == "" {
		return Stack{}, fmt.Errorf("%s needs url and script", path)
	}
	if _, err := os.Stat(s.Script); err != nil {
		return Stack{}, fmt.Errorf("the stack script: %w", err)
	}
	return s, nil
}

func (m *Runtime) RunStack(f factory.Factory, s Stack) error {
	if err := m.Engine.KillStages(f.Repo, f.Slug, StackRole); err != nil {
		return err
	}
	pane, err := m.TM.NewWindow(tmux.Window{Session: m.Session, Name: "s-" + f.Slug, Dir: f.Worktree, Command: "sh " + factory.ShellQuote(s.Script)})
	if err != nil {
		return err
	}
	for _, o := range [][2]string{{"@factory_ui", StackRole}, {"@factory_repo", f.Repo}, {"@factory_stage_of", f.Slug}, {"@factory_stage", StackRole}} {
		if err := m.TM.SetPaneOption(pane, o[0], o[1]); err != nil {
			return err
		}
	}
	return nil
}

func (s Stack) HealthURL() string {
	if s.Health != "" {
		return s.Health
	}
	return s.URL
}

func WaitHealthy(url string, limit time.Duration) error {
	client := http.Client{Timeout: 3 * time.Second}
	deadline := time.Now().Add(limit)
	for {
		resp, err := client.Get(url)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode < 500 {
				return nil
			}
			err = fmt.Errorf("%s answered %d", url, resp.StatusCode)
		}
		if time.Now().After(deadline) {
			return err
		}
		time.Sleep(time.Second)
	}
}
