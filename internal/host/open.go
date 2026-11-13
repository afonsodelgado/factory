package host

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/afonsodelgado/factory/internal/factory"
	"github.com/afonsodelgado/factory/internal/state"
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

type RepoRequest struct {
	Repo   string `json:"repo"`
	Here   bool   `json:"here,omitempty"`
	Forget bool   `json:"forget,omitempty"`
	Notice string `json:"notice,omitempty"`
}

type DeleteRequest struct {
	Repo string `json:"repo"`
	Slug string `json:"slug"`
}

func (m *Runtime) Open(r OpenRequest) (string, error) {
	if strings.TrimSpace(r.Name) == "" {
		return "", nil
	}
	if factory.IsReservedID(r.Name) {
		return "", errors.New(factory.ReservedIDRefusal(r.Name))
	}
	id := factory.Slugify(r.Name)
	if id == "" {
		return "", fmt.Errorf("%q has none of the letters a–z or digits 0–9 a factory id is made of. Type an id such as csv-export", r.Name)
	}
	repos := m.Config.Repos
	if r.Repo != "" {
		if !slices.Contains(m.Config.Repos, r.Repo) {
			return "", fmt.Errorf("the running hub does not list %s; run factory open again from inside that repo to add it", r.Repo)
		}
		repos = []string{r.Repo}
	}
	for _, repo := range repos {
		if key, ok := m.ExistingFactory(repo, id); ok {
			return m.selectExisting(key)
		}
	}
	if len(repos) == 0 {
		return "", errors.New("no repos yet; run factory open again from inside a repo to add it")
	}
	repo := repos[0]
	if earlier, ok := m.RetiredMatch(repos, id); ok {
		repo = earlier
	}
	return m.Create(CreateRequest{Repo: repo, Slug: id, Prompt: r.Prompt, Pipeline: r.Pipeline, Stages: r.Stages, Agents: r.Agents, Supervisor: r.Supervisor})
}
