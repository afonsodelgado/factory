package host

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/afonsodelgado/factory/internal/factory"
	"github.com/afonsodelgado/factory/internal/remote"
	"github.com/afonsodelgado/factory/internal/state"
)

const (
	StackStarting = "starting"
	StackReady    = "ready"
	StackFailed   = "failed"

	FailPorts       = "port_exhaustion"
	FailServe       = "serve_mapping"
	FailPhoneSignIn = "phone_sign_in_unsupported"
)

type StackLease struct {
	Factory  string `json:"factory"`
	Route    string `json:"route"`
	Label    string `json:"label"`
	Repo     string `json:"repo"`
	Slug     string `json:"slug"`
	Worktree string `json:"worktree"`
	Page     string `json:"page"`
}

type StackStatus struct {
	Factory string    `json:"factory"`
	State   string    `json:"state"`
	At      time.Time `json:"at"`
	Origin  string    `json:"origin,omitempty"`
	URL     string    `json:"url,omitempty"`
	Href    string    `json:"href,omitempty"`
	Failure string    `json:"failure,omitempty"`
	Reason  string    `json:"reason,omitempty"`
}

type Stacks interface {
	Start(ctx context.Context, lease StackLease, profile state.Profile) StackStatus
	Stop(key string)
	Status(key string) (StackStatus, bool)
	Keys() []string
	Recover() []StackLease
}

func LegacyStackPath(factoryDir string) string {
	return filepath.Join(factoryDir, "review", "stack.json")
}

func stackJob(key string) string { return "stack " + key }

func (m *Runtime) startStack(it Item, page string) (ReviewReply, error) {
	f := it.Factory
	switch {
	case m.Stacks == nil || m.Jobs == nil:
		return ReviewReply{}, errors.New("walkthrough stacks run only inside factory node run")
	case f.Worktree == "":
		return ReviewReply{}, errors.New("no factory owns this session")
	case stackEnded(it):
		return ReviewReply{}, fmt.Errorf("%s is paused, done, archived or has no supervisor pane, so its walkthrough would stop at once; wake it first", f.Label())
	}
	_, profile := m.repoConfig(f.Repo)
	os.Remove(LegacyStackPath(f.Dir))
	m.stopStack(f.Key())
	lease := StackLease{Factory: f.Key(), Route: ReviewKey(f), Label: f.Label(), Repo: f.Repo, Slug: f.Slug, Worktree: f.Worktree, Page: page}
	stacks := m.Stacks
	m.Jobs.Start(stackJob(lease.Factory), func(ctx context.Context) Message {
		data, _ := json.Marshal(stacks.Start(ctx, lease, profile))
		return Message{T: "stack", Data: data}
	})
	return ReviewReply{Stack: &StackStatus{Factory: lease.Factory, State: StackStarting, At: m.Now()}}, nil
}

func (m *Runtime) stackStatus(it Item) (ReviewReply, error) {
	status, ok := m.walkthrough(it.Factory.Key())
	if !ok {
		return ReviewReply{}, errors.New("no walkthrough stack runs for " + it.Factory.Label() + "; start one with factory review stack")
	}
	return ReviewReply{Stack: &status}, nil
}

func (m *Runtime) walkthrough(key string) (StackStatus, bool) {
	if m.Stacks == nil {
		return StackStatus{}, false
	}
	return m.Stacks.Status(key)
}

func (m *Runtime) StackReady(s StackStatus) {
	it, found := m.snap.Find(s.Factory)
	if !found || stackEnded(it) {
		m.stopStack(s.Factory)
		return
	}
	switch s.State {
	case StackFailed:
		m.note(it.Factory, "the walkthrough stack failed at "+s.Failure+": "+s.Reason)
	case StackReady:
		m.note(it.Factory, "the walkthrough runs at "+s.URL)
		if origin := m.Config.NodeOrigin(); origin != "" {
			m.QueueOpen(it, remote.Open{URL: origin + s.Href})
		}
	}
}

func (m *Runtime) stopStack(key string) {
	if m.Stacks == nil || m.Jobs == nil {
		return
	}
	m.Jobs.Cancel(stackJob(key))
	m.Stacks.Stop(key)
}

func (m *Runtime) ReleaseFinishedStacks() {
	if m.Stacks == nil {
		return
	}
	for _, key := range m.Stacks.Keys() {
		if it, found := m.snap.Find(key); !found || stackEnded(it) {
			m.stopStack(key)
		}
	}
}

func (m *Runtime) recoverStacks() {
	if m.Stacks == nil {
		return
	}
	for _, lease := range m.Stacks.Recover() {
		m.note(factory.Factory{Repo: lease.Repo, Slug: lease.Slug}, "the node restarted, so the walkthrough stack stopped and its tailnet port closed; run factory review stack again")
	}
}

func stackEnded(it Item) bool { return stagesEnded(it) || Paused(it.Factory) }

func stackItem(s StackStatus) AttentionItem {
	return AttentionItem{
		ID:    "stack",
		Kind:  "stack",
		Title: "the walkthrough stack failed at " + s.Failure,
		Body:  s.Reason,
		Since: s.At,
	}
}

func WaitHealthy(url string, limit time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), limit)
	defer cancel()
	return WaitFor(ctx, url, func(status int) bool { return status < 500 })
}

func WaitFor(ctx context.Context, url string, healthy func(status int) bool) error {
	client := http.Client{Timeout: 3 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	for {
		resp, err := client.Do(req)
		if err == nil {
			resp.Body.Close()
			if healthy(resp.StatusCode) {
				return nil
			}
			err = fmt.Errorf("%s answered %d", url, resp.StatusCode)
		}
		select {
		case <-ctx.Done():
			return err
		case <-time.After(time.Second):
		}
	}
}
