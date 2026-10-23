package hub

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
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
	if slug == "" {
		return fmt.Errorf("%q has none of the letters a–z or digits 0–9 a factory name is made of. Type a ticket id, such as ENG-1234, or a feature name", r.Name)
	}
	repos := m.Config.Repos
	if r.Repo != "" {
		if !slices.Contains(m.Config.Repos, r.Repo) {
			return fmt.Errorf("the running hub does not list %s; quit it with q and run factory open again to reload the config", r.Repo)
		}
		repos = []string{r.Repo}
	}
	for _, repo := range repos {
		if key, ok := m.existingFactory(repo, ticket, slug); ok {
			return m.selectExisting(key)
		}
	}
	if len(repos) == 0 {
		return errors.New("no repos configured")
	}
	repo := repos[0]
	if earlier, ok := m.retiredMatch(repos, ticket, slug); ok {
		repo = earlier
	}
	return m.create(dialog.NewResult{Repo: repo, Ticket: ticket, Feature: feature, Slug: slug})
}
