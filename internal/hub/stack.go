package hub

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/afonsodelgado/factory/internal/factory"
	"github.com/afonsodelgado/factory/internal/review"
	"github.com/afonsodelgado/factory/internal/tmux"
)

const (
	stackRole      = "stack"
	stackReadyWait = 3 * time.Minute
)

type Stack struct {
	URL     string          `json:"url"`
	Script  string          `json:"script"`
	Health  string          `json:"health,omitempty"`
	Cookies []review.Cookie `json:"cookies,omitempty"`
}

func StackPath(factoryDir string) string {
	return filepath.Join(factoryDir, "review", "stack.json")
}

func readStack(factoryDir string) (Stack, error) {
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

func (m *Model) startStack(e Event, r ReviewRequest) tea.Cmd {
	fail := func(err error) tea.Cmd {
		e.respond(Message{T: "error", Error: err.Error()})
		return nil
	}
	if m.Engine == nil {
		return fail(errors.New("the hub has no tmux session"))
	}
	it, ok := m.reviewItem(r)
	if !ok || it.Factory.Dir == "" || it.Factory.Worktree == "" {
		return fail(errors.New("no factory owns this session"))
	}
	stack, err := readStack(it.Factory.Dir)
	if err != nil {
		return fail(err)
	}
	if err := m.runStack(it.Factory, stack); err != nil {
		return fail(err)
	}
	data, _ := json.Marshal(ReviewReply{Target: stack.URL})
	e.respond(Message{T: "done", Data: data})
	if m.Chrome == nil {
		return func() tea.Msg {
			return noticeMsg(stack.URL + " is starting; open it with factory review open once it answers.")
		}
	}
	chrome, f := m.Chrome, reviewFactory(it)
	return func() tea.Msg {
		if err := waitHealthy(stack.healthURL(), stackReadyWait); err != nil {
			return noticeMsg(it.Factory.Label() + "'s stack did not come up: " + err.Error() + ". Its window shows why.")
		}
		if _, err := chrome.OpenApp(stack.URL, f, stack.Cookies); err != nil {
			return noticeMsg(err.Error())
		}
		return nil
	}
}

func (m *Model) runStack(f factory.Factory, s Stack) error {
	if err := m.Engine.KillStages(f.Repo, f.Slug, stackRole); err != nil {
		return err
	}
	pane, err := m.Engine.TM.NewWindow(tmux.Window{Session: m.Engine.Session, Name: "s-" + f.Slug, Dir: f.Worktree, Command: "sh " + factory.ShellQuote(s.Script)})
	if err != nil {
		return err
	}
	for _, o := range [][2]string{{"@factory_ui", stackRole}, {"@factory_repo", f.Repo}, {"@factory_stage_of", f.Slug}, {"@factory_stage", stackRole}} {
		if err := m.Engine.TM.SetPaneOption(pane, o[0], o[1]); err != nil {
			return err
		}
	}
	return nil
}

func (s Stack) healthURL() string {
	if s.Health != "" {
		return s.Health
	}
	return s.URL
}

func waitHealthy(url string, limit time.Duration) error {
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
