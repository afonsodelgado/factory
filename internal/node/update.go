package node

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/afonsodelgado/factory/internal/release"
)

const (
	UpdateWait     = 2 * time.Minute
	updatePollWait = 2 * time.Second
)

const (
	Updated     = "updated"
	Current     = "current"
	Unreachable = "unreachable"
	TooOld      = "too_old"
	UpdateFail  = "failed"
	TimedOut    = "timed_out"
)

type UpdateRequest struct {
	From    string `json:"from"`
	Version string `json:"version"`
}

type UpdateStarted struct {
	Started bool `json:"started"`
}

type UpdateAll struct {
	Version string `json:"version"`
}

type NodeUpdate struct {
	Node    string `json:"node"`
	From    string `json:"from,omitempty"`
	To      string `json:"to"`
	Outcome string `json:"outcome"`
	Detail  string `json:"detail,omitempty"`
}

type Updates struct {
	Nodes []NodeUpdate `json:"nodes"`
}

func (n *Nodes) verifiedMember(ctx context.Context, id string) (Member, error) {
	m, member := n.Dir.Member(id)
	if !member || id == n.Self.ID {
		return m, errors.New("the request comes from a node that is not in this node list")
	}
	inv, err := n.Tailscale.Inventory(ctx)
	if err != nil {
		return m, err
	}
	if !n.stillTheSamePeer(inv, m) {
		return m, fmt.Errorf("tailscale status no longer shows %s as this owner's machine", m.Name)
	}
	return m, nil
}

func (n *Nodes) updateRoute(w http.ResponseWriter, r *http.Request, _ string) {
	var body UpdateRequest
	if err := decode(r, &body); err != nil || !release.Valid(body.Version) {
		fail(w, http.StatusBadRequest, "send {from, version: a release tag}")
		return
	}
	if _, err := n.verifiedMember(r.Context(), body.From); err != nil {
		fail(w, http.StatusForbidden, err.Error())
		return
	}
	if release.Valid(n.Version) && !release.Newer(body.Version, n.Version) {
		reply(w, http.StatusOK, UpdateStarted{})
		return
	}
	if n.Update == nil {
		fail(w, http.StatusNotImplemented, "this node cannot update itself")
		return
	}
	if err := n.Update(body.Version); err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	reply(w, http.StatusAccepted, UpdateStarted{Started: true})
}

func (n *Nodes) updateAllRoute(w http.ResponseWriter, r *http.Request, _ string) {
	var body UpdateAll
	if err := decode(r, &body); err != nil || !release.Valid(body.Version) {
		fail(w, http.StatusBadRequest, "send {version: a release tag}")
		return
	}
	reply(w, http.StatusOK, Updates{Nodes: n.UpdateOthers(r.Context(), body.Version)})
}

func (n *Nodes) UpdateOthers(ctx context.Context, version string) []NodeUpdate {
	var others []Member
	for _, m := range n.Dir.Roster().Members {
		if !m.Self {
			others = append(others, m.Member)
		}
	}
	inv, invErr := n.Tailscale.Inventory(ctx)
	results := make([]NodeUpdate, len(others))
	var wg sync.WaitGroup
	for i, m := range others {
		results[i] = NodeUpdate{Node: m.short(), To: version, Outcome: Unreachable, Detail: "tailscale status does not show it as your machine"}
		if invErr != nil || !n.stillTheSamePeer(inv, m) {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i] = n.updateOne(ctx, m, version)
		}()
	}
	wg.Wait()
	return results
}

func (n *Nodes) updateOne(ctx context.Context, m Member, version string) NodeUpdate {
	u := NodeUpdate{Node: m.short(), To: version}
	hello, err := n.helloOf(ctx, m)
	if err != nil {
		u.Outcome, u.Detail = Unreachable, "its node does not answer"
		return u
	}
	u.From = hello.Version
	if release.Valid(hello.Version) && !release.Newer(version, hello.Version) {
		u.Outcome = Current
		return u
	}
	var started UpdateStarted
	err = Remote(m.Name, n.HTTP).Do(ctx, http.MethodPost, "/api/node/update", UpdateRequest{From: n.Self.ID, Version: version}, &started)
	var status StatusError
	switch {
	case errors.As(err, &status) && status.Status == http.StatusNotFound:
		u.Outcome, u.Detail = TooOld, "its factory is too old to update from here: run factory update on "+m.short()
		return u
	case err != nil:
		u.Outcome, u.Detail = UpdateFail, err.Error()
		return u
	}
	return n.awaitVersion(ctx, m, u)
}

func (n *Nodes) updateWait() time.Duration {
	if n.UpdateWait == 0 {
		return UpdateWait
	}
	return n.UpdateWait
}

func (n *Nodes) awaitVersion(ctx context.Context, m Member, u NodeUpdate) NodeUpdate {
	wait := n.updateWait()
	deadline := time.Now().Add(wait)
	for time.Now().Before(deadline) {
		if hello, err := n.helloOf(ctx, m); err == nil && hello.Version == u.To {
			u.Outcome = Updated
			return u
		}
		select {
		case <-ctx.Done():
			u.Outcome, u.Detail = TimedOut, ctx.Err().Error()
			return u
		case <-time.After(min(updatePollWait, wait/3)):
		}
	}
	u.Outcome, u.Detail = TimedOut, fmt.Sprintf("it did not come back on %s within %s; factory node status on %s says why", u.To, wait, m.short())
	return u
}

func (n *Nodes) helloOf(ctx context.Context, m Member) (Hello, error) {
	ctx, cancel := context.WithTimeout(ctx, n.probeWait())
	defer cancel()
	var hello Hello
	err := Remote(m.Name, n.HTTP).Do(ctx, http.MethodGet, "/api/node/hello", nil, &hello)
	return hello, err
}
