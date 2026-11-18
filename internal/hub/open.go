package hub

import (
	"encoding/json"

	tea "charm.land/bubbletea/v2"

	"github.com/afonsodelgado/factory/internal/host"
)

type RepoRequest = host.RepoRequest
type DeleteRequest = host.DeleteRequest

type ShowRequest struct {
	Here   string `json:"here,omitempty"`
	Notice string `json:"notice,omitempty"`
}

func (m *Model) showRequest(e Event) tea.Cmd {
	var r ShowRequest
	err := json.Unmarshal(e.Data, &r)
	if err == nil {
		m.show(r)
	}
	m.reply(e, err)
	return m.load()
}

func (m *Model) show(r ShowRequest) {
	if r.Here != "" {
		m.here = r.Here
	}
	if r.Notice != "" {
		m.notice = news(r.Notice)
	}
}

func (m *Model) reply(e Event, err error) {
	if e.From == nil && e.Reply == nil {
		m.report(err)
		return
	}
	reply := Message{T: "done"}
	if err != nil {
		reply = Message{T: "error", Error: err.Error()}
	}
	e.Respond(reply)
}
