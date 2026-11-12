package hub

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/afonsodelgado/factory/internal/factory"
	"github.com/afonsodelgado/factory/internal/host"
	"github.com/afonsodelgado/factory/internal/remote"
	"github.com/afonsodelgado/factory/internal/review"
	"github.com/afonsodelgado/factory/internal/state"
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
	if err == nil && r.Action == "link" && m.Open != nil {
		e.Respond(Message{T: "done", Data: json.RawMessage("{}")})
		open, url := m.Open, r.URL
		return func() tea.Msg {
			open(url)
			return nil
		}
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

func (m *Model) threadCounter(f factory.Factory) threadCounter {
	if m.Review == nil || f.Remote != nil {
		return nil
	}
	return m.Review.OpenCount
}

func (m *Model) openLive(url string) tea.Cmd {
	if m.Chrome == nil {
		return nil
	}
	for _, it := range localOnly(m.snap).Items {
		page, ok := liveStageOf(it.Factory, url)
		if !ok {
			continue
		}
		chrome, f, fallback := m.Chrome, reviewFactory(it), m.Open
		return func() tea.Msg {
			if _, err := chrome.OpenApp(url, f, page, nil); err != nil {
				fallback(url)
			}
			return nil
		}
	}
	return nil
}

func liveStageOf(f factory.Factory, url string) (string, bool) {
	if f.State == nil || !strings.HasPrefix(url, "http") {
		return "", false
	}
	for _, key := range []string{"design", "walkthrough"} {
		for _, a := range f.State.Stages[key].Artifacts {
			if a.URL == url {
				return livePageOf(key), true
			}
		}
	}
	return "", false
}

func livePageOf(stage string) string {
	if stage == "design" {
		return review.Prototype
	}
	return review.App
}

const openReceipts = 100

func (m *Model) servedBy(f factory.Factory) string {
	l := m.linkOf(f)
	port, ok := remote.LoopbackPort(l.view.Review)
	if !ok {
		return ""
	}
	served := f.Remote.Name + ":" + strconv.Itoa(port)
	if local, ok := remote.LocalPort(m.Config.StateDir, f.Remote.Name, l.server, port); ok {
		return served + ", through the tunnel at 127.0.0.1:" + strconv.Itoa(local)
	}
	return served + ", through a tunnel that ctrl+o opens"
}

func (m *Model) restoreTunnels(name string) tea.Cmd {
	if m.Tunnels == nil || len(remote.LoadTunnels(m.Config.StateDir, name)) == 0 {
		return nil
	}
	cfg, tunnels, home := m.Config, m.Tunnels, m.link(name).view.Review
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), remoteWait)
		defer cancel()
		client, err := remote.For(cfg, name)
		if err == nil {
			err = client.Restore(ctx, tunnels, home)
		}
		if err != nil {
			return noticeMsg(problem("The tunnels to " + name + " did not all come back: " + err.Error()))
		}
		return nil
	}
}

func (m *Model) settleOpens(name string) tea.Cmd {
	if m.Tunnels == nil {
		return nil
	}
	if m.opening == nil {
		m.opening = map[string]bool{}
	}
	cache, _ := remote.LoadCache(m.Config.StateDir, name)
	var opened []string
	var cmds []tea.Cmd
	for _, o := range m.link(name).view.Opens {
		switch {
		case slices.Contains(cache.Opened, o.ID):
			opened = append(opened, o.ID)
		case !m.opening[o.ID]:
			m.opening[o.ID] = true
			cmds = append(cmds, m.openRemote(name, o))
		}
	}
	if len(opened) > 0 {
		cmds = append(cmds, acknowledge(m.Config, name, opened))
	}
	return tea.Batch(cmds...)
}

func acknowledge(cfg *state.Config, name string, ids []string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), remoteWait)
		defer cancel()
		if client, err := remote.For(cfg, name); err == nil {
			client.Read(ctx, "opened", ids, nil)
		}
		return nil
	}
}

func (m *Model) openRemote(name string, o remote.Open) tea.Cmd {
	l := m.link(name)
	if l.state(m.Now()) != connected {
		m.notice = problem(name + " is unreachable, so " + o.URL + " cannot open through a tunnel now.")
		return nil
	}
	cfg, tunnels, chrome, open, home := m.Config, m.Tunnels, m.Chrome, m.Open, l.view.Review
	return func() tea.Msg {
		err := openThrough(cfg, tunnels, chrome, open, name, home, o)
		if o.ID != "" {
			remote.UpdateCache(cfg.StateDir, name, func(c *remote.Cache) error {
				c.Opened = append(c.Opened, o.ID)
				c.Opened = c.Opened[max(0, len(c.Opened)-openReceipts):]
				return nil
			})
			acknowledge(cfg, name, []string{o.ID})()
		}
		if err != nil {
			return noticeMsg(problem("Could not open " + o.URL + " from " + name + ": " + err.Error()))
		}
		return nil
	}
}

func openThrough(cfg *state.Config, tunnels *remote.Tunnels, chrome *review.Browser, open func(string), name, home string, o remote.Open) error {
	ctx, cancel := context.WithTimeout(context.Background(), remoteWait)
	defer cancel()
	client, err := remote.For(cfg, name)
	if err != nil {
		return err
	}
	url, err := client.Resolve(ctx, tunnels, home, o.URL)
	if err != nil {
		return err
	}
	health := ""
	if o.Health != "" {
		if health, err = client.Resolve(ctx, tunnels, home, o.Health); err != nil {
			return err
		}
	}
	var endpoint review.Endpoint
	if chrome != nil && home != "" {
		api, err := client.Resolve(ctx, tunnels, home, home)
		if err != nil {
			return err
		}
		endpoint = remote.EndpointOf(api)
	}
	if health != "" {
		if err := host.WaitHealthy(health, stackReadyWait); err != nil {
			return err
		}
	}
	f := review.Factory{Key: o.Key, Label: o.Factory + " on " + name}
	switch {
	case endpoint.Origin != "" && review.Live(o.Page):
		_, err = chrome.OpenAppAt(endpoint, url, f, o.Page, o.Cookies)
	case endpoint.Origin != "" && strings.HasPrefix(url, endpoint.Origin+"/r/"):
		err = chrome.OpenPageAt(endpoint, url, f)
	default:
		open(url)
	}
	if err != nil {
		open(url)
	}
	return nil
}
