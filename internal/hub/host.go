package hub

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"

	"github.com/afonsodelgado/factory/internal/host"
)

const nodeWait = time.Minute

type Node interface {
	Call(ctx context.Context, m host.Message) (host.Message, error)
}

type presentationMsg host.Presentation

type nodeLostMsg struct{ err error }

func (m *Model) ask(kind string, payload, into any) error {
	return m.call(kind, "", payload, into)
}

func (m *Model) effect(kind string, payload, into any) error {
	return m.call(kind, uuid.NewString(), payload, into)
}

func (m *Model) call(kind, op string, payload, into any) error {
	if m.Node == nil {
		return errors.New("the hub is not connected to a factory node")
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), nodeWait)
	defer cancel()
	answer, err := m.Node.Call(ctx, host.Message{T: kind, Op: op, Data: data})
	switch {
	case err != nil:
		return err
	case answer.T == "uncertain":
		return errors.New(answer.Error + "; the outcome is unknown, so look before trying again")
	case answer.T != "done":
		return errors.New(answer.Error)
	case into == nil:
		return nil
	}
	return json.Unmarshal(answer.Data, into)
}

func (m *Model) applyPresentation(p host.Presentation) {
	m.claims = p.Claims
	for _, settled := range m.newlySettled(p.Settled) {
		if settled.Outcome == host.StatusRefused && settled.Reason != "" {
			m.panel.refused[settled.Key()] = settled.Reason
			continue
		}
		delete(m.panel.drafts, settled.Key())
	}
	if p.Notice != "" {
		m.notice = problem(p.Notice)
	}
	if p.Repos != nil {
		m.Config.Repos = p.Repos
	}
	m.threads, m.prs, m.prsChecked = p.Threads, p.PRs, p.Checked
	m.apply(p.Snapshot)
}

func (m *Model) newlySettled(settled []host.Answer) []host.Answer {
	catchingUp := !m.presented
	m.presented = true
	last := m.lastSettled
	if len(settled) > 0 {
		m.lastSettled = settlementOf(settled[len(settled)-1])
	}
	if catchingUp {
		return nil
	}
	for i := len(settled) - 1; i >= 0; i-- {
		if settlementOf(settled[i]) == last {
			return settled[i+1:]
		}
	}
	return settled
}

func settlementOf(a host.Answer) string {
	return a.Key() + "|" + a.Operation + "|" + a.SettledAt.String() + "|" + a.At.String() + "|" + a.Outcome
}

func (m *Model) claim(key string) (sentMark, bool) {
	a, ok := m.claims[key]
	return a, ok
}

func (m *Model) putClaim(a sentMark) {
	if m.claims == nil {
		m.claims = map[string]host.Answer{}
	}
	m.claims[a.Key()] = a
}

func (m *Model) dropClaim(key string) {
	delete(m.claims, key)
	m.report(m.ask("drop-claim", key, nil))
}
