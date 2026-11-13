package hub

import (
	"encoding/json"
	"errors"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/afonsodelgado/factory/internal/host"
)

const (
	stackRole      = host.StackRole
	stackReadyWait = 3 * time.Minute
)

func StackPath(factoryDir string) string { return host.StackPath(factoryDir) }

func (m *Model) startStack(e Event, r ReviewRequest) tea.Cmd {
	fail := func(err error) tea.Cmd {
		e.Respond(Message{T: "error", Error: err.Error()})
		return nil
	}
	if m.Engine == nil {
		return fail(errors.New("the hub has no tmux session"))
	}
	it, ok := m.reviewItem(r)
	if !ok || it.Factory.Dir == "" || it.Factory.Worktree == "" {
		return fail(errors.New("no factory owns this session"))
	}
	stack, err := host.ReadStack(it.Factory.Dir)
	if err != nil {
		return fail(err)
	}
	if err := m.execution().RunStack(it.Factory, stack); err != nil {
		return fail(err)
	}
	data, _ := json.Marshal(ReviewReply{Target: stack.URL})
	e.Respond(Message{T: "done", Data: data})
	if m.Chrome == nil {
		return func() tea.Msg {
			return noticeMsg(news(stack.URL + " is starting; open it with factory review open once it answers."))
		}
	}
	chrome, f := m.Chrome, reviewFactory(it)
	return func() tea.Msg {
		if err := host.WaitHealthy(stack.HealthURL(), stackReadyWait); err != nil {
			return noticeMsg(problem(it.Factory.Label() + "'s stack did not come up: " + err.Error() + ". Its window shows why."))
		}
		if _, err := chrome.OpenApp(stack.URL, f, livePage(r.Page), stack.Cookies); err != nil {
			return noticeMsg(problem(err.Error()))
		}
		return nil
	}
}
