package host

import (
	"slices"
)

type RepoRequest struct {
	Repo   string `json:"repo"`
	Here   bool   `json:"here,omitempty"`
	Forget bool   `json:"forget,omitempty"`
	Notice string `json:"notice,omitempty"`
}

type DeleteRequest struct {
	Repo string `json:"repo"`
	Slug string `json:"slug"`
	Work string `json:"work,omitempty"`
}

func (m *Runtime) AddRepo(repo string) {
	if repo == "" || slices.Contains(m.Config.Repos, repo) {
		return
	}
	m.setRepos(append(slices.Clip(m.Config.Repos), repo))
}

func (m *Runtime) ForgetRepo(repo string) {
	m.setRepos(slices.DeleteFunc(slices.Clone(m.Config.Repos), func(r string) bool { return r == repo }))
}

func (m *Runtime) setRepos(repos []string) {
	if m.Loader != nil {
		m.Loader.SetRepos(repos)
		return
	}
	m.Config.Repos = repos
}
