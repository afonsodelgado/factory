package host

import (
	"context"
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
	ctx, cancel := context.WithTimeout(context.Background(), limit)
	defer cancel()
	return WaitFor(ctx, url, func(status int) bool { return status < 500 })
}

func WaitFor(ctx context.Context, url string, healthy func(status int) bool) error {
	client := http.Client{Timeout: 3 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	for {
		resp, err := client.Do(req)
		if err == nil {
			resp.Body.Close()
			if healthy(resp.StatusCode) {
				return nil
			}
			err = fmt.Errorf("%s answered %d", url, resp.StatusCode)
		}
		select {
		case <-ctx.Done():
			return err
		case <-time.After(time.Second):
		}
	}
}
