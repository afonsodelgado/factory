package hub

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/afonsodelgado/factory/internal/factory"
	"github.com/afonsodelgado/factory/internal/host"
	"github.com/afonsodelgado/factory/internal/remote"
	"github.com/afonsodelgado/factory/internal/review"
	"github.com/afonsodelgado/factory/internal/state"
)

type ReviewRequest = host.ReviewRequest
type ReviewReply = host.ReviewReply

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

const (
	openReceipts   = 100
	stackReadyWait = 3 * time.Minute
)

func (m *Model) servedBy(f factory.Factory) string {
	l := m.linkOf(f)
	if l.view.Review.Socket == "" {
		return ""
	}
	served := f.Remote.Name
	if local, ok := remote.LocalPort(m.Config.StateDir, f.Remote.Name, remote.Tunnel{Server: l.server, Socket: l.view.Review.Socket}); ok {
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
	cfg, tunnels, open, home := m.Config, m.Tunnels, m.Open, l.view.Review
	return func() tea.Msg {
		if err := openThrough(cfg, tunnels, open, name, home, o); err != nil {
			return noticeMsg(problem("Could not open " + o.URL + " from " + name + ": " + err.Error() + ". It stays queued and opens when the hub starts again."))
		}
		if o.ID != "" {
			remote.UpdateCache(cfg.StateDir, name, func(c *remote.Cache) error {
				c.Opened = append(c.Opened, o.ID)
				c.Opened = c.Opened[max(0, len(c.Opened)-openReceipts):]
				return nil
			})
			acknowledge(cfg, name, []string{o.ID})()
		}
		return nil
	}
}

func openThrough(cfg *state.Config, tunnels *remote.Tunnels, open func(string), name string, home remote.ReviewHome, o remote.Open) error {
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
	if o.Health != "" {
		health, err := client.Resolve(ctx, tunnels, home, o.Health)
		if err == nil {
			err = host.WaitHealthy(health, stackReadyWait)
		}
		if err != nil {
			return err
		}
	}
	open(url)
	return nil
}
