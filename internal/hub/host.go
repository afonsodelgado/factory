package hub

import (
	"github.com/afonsodelgado/factory/internal/host"
)

type hostEngine struct{ model *Model }

func (e hostEngine) NewStagePane(s host.StagePane) (string, error) {
	return e.model.Engine.NewStagePane(e.model.Layout, s)
}

func (e hostEngine) KillStages(repo, slug, role string) error {
	return e.model.Engine.KillStages(repo, slug, role)
}

func (e hostEngine) Type(pane, text string) error { return e.model.Engine.Type(pane, text) }

func (m *Model) execution() *host.Runtime {
	first := m.host == nil
	if m.host == nil {
		m.host = host.New(host.Runtime{})
	}
	r := m.host
	r.ShowArchived = m.showArchived
	r.Config, r.Loader, r.Typist, r.Now = m.Config, m.Loader, m.Typist, m.Now
	if m.Engine != nil {
		r.Engine = hostEngine{m}
		r.TM = m.Engine.TM
		r.Session = m.Engine.Session
	}
	r.SetSnapshot(localOnly(m.snap))
	r.Pages = m.Review
	r.Refused = m.dropAnswer
	if first && m.Config.StateDir != "" {
		m.report(r.Recover())
	}
	return r
}

func (e hostEngine) NewFactoryPane(n host.NewPane) (string, error) {
	return e.model.Engine.NewFactoryPane(e.model.Layout, n)
}

func (e hostEngine) Pause(repo, slug string) error {
	return e.model.Engine.Pause(&e.model.Layout, repo, slug)
}
