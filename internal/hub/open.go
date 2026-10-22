package hub

import (
	"encoding/json"
	"errors"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/afonsodelgado/factory/internal/factory"
	"github.com/afonsodelgado/factory/internal/ui/dialog"
)

type OpenRequest struct {
	Name string `json:"name"`
	Repo string `json:"repo"`
}

func (m *Model) openRequest(e Event) tea.Cmd {
	var r OpenRequest
	err := json.Unmarshal(e.Data, &r)
	if err == nil {
		err = m.open(r)
	}
	reply := Message{T: "done"}
	if err != nil {
		reply = Message{T: "error", Error: err.Error()}
	}
	e.From.Send(reply)
	return m.load()
}

func (m *Model) open(r OpenRequest) error {
	if strings.TrimSpace(r.Name) == "" {
		return nil
	}
	ticket, feature := factory.ParseName(r.Name)
	slug := factory.SlugFor(ticket, feature)
	repos := m.Config.Repos
	if r.Repo != "" {
		repos = []string{r.Repo}
	}
	for _, repo := range repos {
		if key, ok := m.existingFactory(repo, ticket, slug); ok {
			m.choose(key)
			return nil
		}
	}
	if len(repos) == 0 {
		return errors.New("no repos configured")
	}
	return m.create(dialog.NewResult{Repo: repos[0], Ticket: ticket, Feature: feature, Slug: slug})
}
