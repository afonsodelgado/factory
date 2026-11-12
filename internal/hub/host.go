package hub

import (
	"time"

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
	r.SetSnapshot(m.snap)
	r.Pages = m.Review
	r.Queued = func(a host.Answer) {
		if it, ok := m.snap.Find(a.Factory); ok {
			a.Draft = m.draftPath(it.Factory, a.ItemID)
		}
		m.panel.sent[a.Factory+"|"+a.ItemID] = a
	}
	r.Pending = m.stillQueued
	r.Sent = func(a host.Answer, at time.Time) {
		if pending, ok := m.panel.sent[a.Factory+"|"+a.ItemID]; ok && a.Draft == "" {
			a.Draft = pending.Draft
		}
		m.recordSent(a, at)
	}
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
