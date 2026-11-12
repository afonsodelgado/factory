package hub

import (
	"encoding/json"
	"errors"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/afonsodelgado/factory/internal/factory"
	"github.com/afonsodelgado/factory/internal/host"
	"github.com/afonsodelgado/factory/internal/review"
)

type ReviewRequest = host.ReviewRequest
type ReviewReply = host.ReviewReply

func ReviewActions(events chan<- Event) func(review.Action) (any, error) {
	return host.ReviewActions(events)
}

func (m *Model) reviewRequest(e Event) tea.Cmd {
	var r ReviewRequest
	var reply ReviewReply
	err := json.Unmarshal(e.Data, &r)
	if err == nil && r.Action == "open" {
		return m.openApp(e, r)
	}
	if err == nil && r.Action == "stack" {
		return m.startStack(e, r)
	}
	if err == nil {
		reply, err = m.review(r)
	}
	if err != nil {
		e.Respond(Message{T: "error", Error: err.Error()})
		return nil
	}
	data, _ := json.Marshal(reply)
	e.Respond(Message{T: "done", Data: data})
	return m.load()
}

func (m *Model) review(r ReviewRequest) (ReviewReply, error) { return m.execution().Review(r) }

func (m *Model) openApp(e Event, r ReviewRequest) tea.Cmd {
	fail := func(err error) tea.Cmd {
		e.Respond(Message{T: "error", Error: err.Error()})
		return nil
	}
	if m.Chrome == nil || m.Review == nil {
		return fail(errors.New("the factory's Chrome is off; set review.browser to chrome"))
	}
	it, ok := m.reviewItem(r)
	if !ok {
		return fail(errors.New("no factory owns this session; pass --factory <id>"))
	}
	chrome, f, url := m.Chrome, reviewFactory(it), r.URL
	return func() tea.Msg {
		if _, err := chrome.OpenApp(url, f, livePage(r.Page), nil); err != nil {
			e.Respond(Message{T: "error", Error: err.Error()})
			return nil
		}
		data, _ := json.Marshal(ReviewReply{Target: f.Label})
		e.Respond(Message{T: "done", Data: data})
		return nil
	}
}

func (m *Model) reviewItem(r ReviewRequest) (Item, bool) { return m.execution().ReviewItem(r) }

func reviewKey(f factory.Factory) string { return host.ReviewKey(f) }

func reviewFactory(it Item) review.Factory { return host.ReviewFactory(it) }

func livePage(page string) string {
	if page == review.Prototype {
		return review.Prototype
	}
	return review.App
}

func (m *Model) syncReview() { m.execution().SyncReview() }

func (m *Model) reviewURL(url string) string {
	if m.Review == nil {
		return url
	}
	if page, ok := m.Review.PageURL(url); ok {
		return page
	}
	return url
}

func (m *Model) threadCounter() threadCounter {
	if m.Review == nil {
		return nil
	}
	return m.Review.OpenCount
}

func (m *Model) openLive(url string) tea.Cmd {
	if m.Chrome == nil || !strings.HasPrefix(url, "http") {
		return nil
	}
	for _, it := range localOnly(m.snap).Items {
		if it.Factory.State == nil {
			continue
		}
		for _, key := range []string{"design", "walkthrough"} {
			for _, a := range it.Factory.State.Stages[key].Artifacts {
				if a.URL != url {
					continue
				}
				chrome, f, page, fallback := m.Chrome, reviewFactory(it), livePageOf(key), m.Open
				return func() tea.Msg {
					if _, err := chrome.OpenApp(url, f, page, nil); err != nil {
						fallback(url)
					}
					return nil
				}
			}
		}
	}
	return nil
}

func livePageOf(stage string) string {
	if stage == "design" {
		return review.Prototype
	}
	return review.App
}
