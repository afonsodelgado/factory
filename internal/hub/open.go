package hub

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/afonsodelgado/factory/internal/factory"
	"github.com/afonsodelgado/factory/internal/state"
	"github.com/afonsodelgado/factory/internal/ui/dialog"
)

type OpenRequest struct {
	Name       string                        `json:"name"`
	Repo       string                        `json:"repo"`
	Prompt     string                        `json:"prompt,omitempty"`
	Pipeline   string                        `json:"pipeline,omitempty"`
	Stages     []string                      `json:"stages,omitempty"`
	Agents     map[string]state.AgentSetting `json:"agents,omitempty"`
	Supervisor state.AgentSetting            `json:"supervisor,omitzero"`
}

type DeleteRequest struct {
	Repo string `json:"repo"`
	Slug string `json:"slug"`
}

func (m *Model) deleteRequest(e Event) tea.Cmd {
	var r DeleteRequest
	var steps []factory.RemovalStep
	err := json.Unmarshal(e.Data, &r)
	if err == nil {
		steps, err = m.delete(state.Key(r.Repo, r.Slug))
	}
	report, _ := json.Marshal(factory.Report(steps))
	reply := Message{T: "done", Data: report}
	if err != nil {
		reply = Message{T: "error", Error: err.Error(), Data: report}
	}
	e.From.Send(reply)
	return m.load()
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
	if factory.IsReservedID(r.Name) {
		return errors.New(factory.ReservedIDRefusal(r.Name))
	}
	id := factory.Slugify(r.Name)
	if id == "" {
		return fmt.Errorf("%q has none of the letters a–z or digits 0–9 a factory id is made of. Type an id such as csv-export", r.Name)
	}
	repos := m.Config.Repos
	if r.Repo != "" {
		if !slices.Contains(m.Config.Repos, r.Repo) {
			return fmt.Errorf("the running hub does not list %s; quit it with q and run factory open again to reload the config", r.Repo)
		}
		repos = []string{r.Repo}
	}
	for _, repo := range repos {
		if key, ok := m.existingFactory(repo, id); ok {
			return m.selectExisting(key)
		}
	}
	if len(repos) == 0 {
		return errors.New("no repos configured; add one with factory init <repo>, then quit the hub with q and run factory again")
	}
	repo := repos[0]
	if earlier, ok := m.retiredMatch(repos, id); ok {
		repo = earlier
	}
	return m.create(dialog.NewResult{Repo: repo, Slug: id, Prompt: r.Prompt, Pipeline: r.Pipeline, Stages: r.Stages, Agents: r.Agents, Supervisor: r.Supervisor})
}
