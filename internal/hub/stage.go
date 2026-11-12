package hub

import (
	"encoding/json"

	tea "charm.land/bubbletea/v2"

	"github.com/afonsodelgado/factory/internal/host"
	"github.com/afonsodelgado/factory/internal/state"
)

type StageRequest = host.StageRequest
type StageReply = host.StageReply
type StageView = host.StageView
type queuedType = host.QueuedType

func (m *Model) stageRequest(e Event) tea.Cmd {
	var r StageRequest
	var reply StageReply
	err := json.Unmarshal(e.Data, &r)
	if err == nil {
		reply, err = m.stage(r)
	}
	if err != nil {
		e.Respond(Message{T: "error", Error: err.Error()})
		return m.load()
	}
	data, _ := json.Marshal(reply)
	e.Respond(Message{T: "done", Data: data})
	return m.load()
}

func (m *Model) stage(r StageRequest) (StageReply, error) { return m.execution().Stage(r) }

func (m *Model) writableDirs(repo, checkout string, setting state.AgentSetting) []string {
	return m.execution().WritableDirs(repo, checkout, setting)
}

func (m *Model) closeFinishedStages() { m.execution().CloseFinishedStages() }

func (m *Model) queueType(q queuedType) { m.execution().QueueType(q) }

func (m *Model) flushTypeQueue() {
	r := m.execution()
	r.FlushTypeQueue()
	if notice := r.TakeNotice(); notice != "" {
		m.notice = problem(notice)
	}
}

func writeFile(path, content string) error { return host.WriteFile(path, content) }
