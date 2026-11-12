package hub

import (
	"encoding/json"
	"fmt"

	tea "charm.land/bubbletea/v2"

	"github.com/afonsodelgado/factory/internal/factory"
	"github.com/afonsodelgado/factory/internal/host"
	"github.com/afonsodelgado/factory/internal/state"
)

type OpenRequest = host.OpenRequest
type RepoRequest = host.RepoRequest
type DeleteRequest = host.DeleteRequest

func (m *Model) deleteRequest(e Event) tea.Cmd {
	var r DeleteRequest
	var steps []factory.RemovalStep
	err := json.Unmarshal(e.Data, &r)
	if err == nil {
		steps, err = m.deleteRequested(r)
	}
	report, _ := json.Marshal(factory.Report(steps))
	reply := Message{T: "done", Data: report}
	if err != nil {
		reply = Message{T: "error", Error: err.Error(), Data: report}
	}
	e.From.Send(reply)
	return m.load()
}

func (m *Model) deleteRequested(r DeleteRequest) ([]factory.RemovalStep, error) {
	key := state.Key(r.Repo, r.Slug)
	if _, listed := m.snap.Find(key); listed || m.Loader == nil {
		return m.delete(key)
	}
	f, ok := m.Loader.Lookup(r.Repo, r.Slug)
	if !ok {
		return nil, fmt.Errorf("the hub does not list %s", key)
	}
	return m.deleteFactory(f)
}

func (m *Model) repoRequest(e Event) tea.Cmd {
	var r RepoRequest
	err := json.Unmarshal(e.Data, &r)
	if err == nil {
		m.addRepo(r)
	}
	m.reply(e, err)
	return m.load()
}

func (m *Model) reply(e Event, err error) {
	if e.From == nil {
		m.report(err)
		return
	}
	reply := Message{T: "done"}
	if err != nil {
		reply = Message{T: "error", Error: err.Error()}
	}
	e.From.Send(reply)
}

func (m *Model) addRepo(r RepoRequest) {
	if r.Forget {
		m.forgetRepo(r.Repo)
		return
	}
	if r.Here {
		m.here = r.Repo
	}
	if r.Notice != "" {
		m.notice = news(r.Notice)
	}
	m.execution().AddRepo(r.Repo)
}

func (m *Model) forgetRepo(repo string) {
	if m.here == repo {
		m.here = ""
	}
	m.execution().ForgetRepo(repo)
}

func (m *Model) openRequest(e Event) tea.Cmd {
	var r OpenRequest
	err := json.Unmarshal(e.Data, &r)
	if err == nil {
		err = m.open(r)
	}
	m.reply(e, err)
	return m.load()
}

func (m *Model) open(r OpenRequest) error {
	key, err := m.execution().Open(r)
	if key != "" {
		m.choose(key)
	}
	return err
}
