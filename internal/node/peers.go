package node

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/afonsodelgado/factory/internal/phone"
)

const (
	ProbeWait   = 2 * time.Second
	probeFanout = 8
	UpdateFix   = "factory update  then factory node install"
)

var errRedirect = errors.New("a node answered with a redirect, and nodes never follow one")

type Peer struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	OS       string    `json:"os"`
	Login    string    `json:"login,omitempty"`
	Tags     []string  `json:"tags,omitempty"`
	Online   bool      `json:"online"`
	LastSeen time.Time `json:"last_seen,omitzero"`
	IPs      []string  `json:"ips,omitempty"`
}

func (p Peer) Short() string {
	name, _, _ := strings.Cut(p.Name, ".")
	return name
}

func (p Peer) runsNodes() bool { return p.OS == "macOS" || p.OS == "linux" }

type Inventory struct {
	Self  Self
	Peers []Peer
}

type NotFoundError struct{ Typed, Tailnet string }

func (e NotFoundError) Error() string {
	return "tailscale status lists no " + e.Typed + " on " + e.Tailnet + ". Check the spelling, or that the machine is logged in to this tailnet"
}

func (inv Inventory) Resolve(typed string) (Peer, error) {
	want := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(typed)), ".")
	address, isAddress := netip.ParseAddr(want)
	for _, p := range inv.Peers {
		named := p.Name == want || p.Short() == want
		addressed := isAddress == nil && slices.ContainsFunc(p.IPs, func(ip string) bool {
			parsed, err := netip.ParseAddr(ip)
			return err == nil && parsed == address
		})
		if named || addressed {
			return p, nil
		}
	}
	return Peer{}, NotFoundError{Typed: typed, Tailnet: inv.Self.Tailnet}
}

func (inv Inventory) Named(name string) (Peer, bool) {
	i := slices.IndexFunc(inv.Peers, func(p Peer) bool { return p.Name == name })
	if i < 0 {
		return Peer{}, false
	}
	return inv.Peers[i], true
}

type Refusal struct {
	Check   string
	Problem string
	Fix     string
	On      string
}

func (r Refusal) Error() string { return r.Problem }

func (inv Inventory) Verify(p Peer, owner string) error {
	sameTailnet := inv.Self.Tailnet != "" && strings.HasSuffix(p.Name, "."+inv.Self.Tailnet)
	switch {
	case len(p.Tags) > 0:
		return Refusal{Check: "owner", Problem: "tagged " + strings.Join(p.Tags, ", ") + ", so Tailscale sends no login with its requests", Fix: UntagFix, On: p.Short()}
	case !sameTailnet:
		return Refusal{Check: "owner", Problem: p.Name + " is not on " + inv.Self.Tailnet + ", this node's tailnet"}
	case !strings.EqualFold(p.Login, owner):
		return Refusal{Check: "owner", Problem: p.Short() + " belongs to " + p.Login + ", and this node belongs to " + owner}
	case !p.runsNodes():
		return Refusal{Check: "node", Problem: p.Short() + " runs " + p.OS + ", and nodes run only on macOS and Linux"}
	}
	return nil
}

type Hello struct {
	ID          string          `json:"id"`
	Name        string          `json:"name"`
	Version     string          `json:"version"`
	Protocol    int             `json:"protocol"`
	MinProtocol int             `json:"min_protocol"`
	OS          string          `json:"os"`
	Owner       string          `json:"owner"`
	Roster      string          `json:"roster"`
	Members     int             `json:"members"`
	Factories   int             `json:"factories"`
	Push        *phone.KeyState `json:"push,omitempty"`
}

func compatible(a, b Hello) bool {
	return max(a.MinProtocol, b.MinProtocol) <= min(a.Protocol, b.Protocol)
}

func incompatibility(self, other Hello, short, selfShort string) Refusal {
	older, newer := short, selfShort
	if other.Protocol > self.Protocol {
		older, newer = selfShort, short
	}
	return Refusal{Check: "node", Problem: fmt.Sprintf("%s runs %s (node API %d–%d), and %s runs %s (node API %d–%d); nodes need a shared API", short, other.Version, other.MinProtocol, other.Protocol, selfShort, self.Version, self.MinProtocol, self.Protocol), Fix: UpdateFix + ", to match " + newer, On: older}
}

func PeerHTTP() *http.Client {
	return &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return errRedirect }}
}

func Remote(name string, h *http.Client) *Client { return &Client{HTTP: h, Base: "https://" + name} }

type answer struct {
	Hello
	Error  string   `json:"error"`
	Funnel bool     `json:"funnel"`
	Fixes  []string `json:"fixes"`
}

type Candidate struct {
	Name       string    `json:"name"`
	Short      string    `json:"short"`
	OS         string    `json:"os"`
	State      string    `json:"state"`
	Detail     string    `json:"detail"`
	Fix        string    `json:"fix,omitempty"`
	On         string    `json:"on,omitempty"`
	LastSeen   time.Time `json:"last_seen,omitzero"`
	Selectable bool      `json:"selectable"`
	Hello      *Hello    `json:"hello,omitempty"`
}

const (
	StateNode         = "node"
	StateOffline      = "offline"
	StateTagged       = "tagged"
	StateNoNode       = "no-node"
	StateFunnel       = "funnel"
	StateOtherOwner   = "other-owner"
	StateIncompatible = "incompatible"
)

func (n *Nodes) probe(ctx context.Context, p Peer) Candidate {
	ctx, cancel := context.WithTimeout(ctx, n.probeWait())
	defer cancel()
	c := Candidate{Name: p.Name, Short: p.Short(), OS: p.OS, LastSeen: p.LastSeen}
	var a answer
	err := Remote(p.Name, n.HTTP).Do(ctx, http.MethodGet, "/api/node/hello", nil, &a)
	var status StatusError
	switch {
	case errors.As(err, &status) && a.Funnel:
		c.State, c.Detail, c.Fix, c.On = StateFunnel, "Funnel is on, so anyone on the internet reaches its node", strings.Join(a.Fixes, "  &&  "), p.Short()
	case errors.As(err, &status) && status.Status == http.StatusForbidden && a.Owner != "":
		c.State, c.Detail = StateOtherOwner, "its node belongs to "+a.Owner+", and this node belongs to "+n.Self.Owner
	case err != nil || a.ID == "":
		c.State, c.Detail, c.Fix, c.On = StateNoNode, "no node answers at https://"+p.Name, "factory node install", p.Short()
	case !compatible(n.hello(), a.Hello):
		r := incompatibility(n.hello(), a.Hello, p.Short(), n.Self.short())
		c.State, c.Detail, c.Fix, c.On = StateIncompatible, r.Problem, r.Fix, r.On
	default:
		c.State, c.Detail, c.Selectable = StateNode, "factory "+a.Version, true
	}
	if a.ID != "" {
		hello := a.Hello
		c.Hello = &hello
	}
	return c
}

func (n *Nodes) Discover(ctx context.Context) ([]Candidate, error) {
	inv, err := n.Tailscale.Inventory(ctx)
	if err != nil {
		return nil, err
	}
	roster := n.Dir.Roster()
	enrolled := func(p Peer) bool {
		return slices.ContainsFunc(roster.Members, func(m RosterMember) bool { return m.Name == p.Name || (m.Peer != "" && m.Peer == p.ID) })
	}
	var rows []Candidate
	var asking []int
	for _, p := range inv.Peers {
		ownMachine := strings.EqualFold(p.Login, n.Self.Owner) && strings.HasSuffix(p.Name, "."+inv.Self.Tailnet)
		switch {
		case enrolled(p) || !p.runsNodes():
		case len(p.Tags) > 0:
			rows = append(rows, Candidate{Name: p.Name, Short: p.Short(), OS: p.OS, State: StateTagged, Detail: "tagged " + strings.Join(p.Tags, ", ") + ", so it can't show it is yours", Fix: UntagFix, On: p.Short()})
		case !ownMachine:
		case !p.Online:
			rows = append(rows, Candidate{Name: p.Name, Short: p.Short(), OS: p.OS, State: StateOffline, Detail: "offline", LastSeen: p.LastSeen})
		default:
			asking = append(asking, len(rows))
			rows = append(rows, Candidate{Name: p.Name})
		}
	}
	n.probeAll(ctx, inv, rows, asking)
	return rows, nil
}

func (n *Nodes) probeAll(ctx context.Context, inv Inventory, rows []Candidate, asking []int) {
	slots := make(chan struct{}, probeFanout)
	var wg sync.WaitGroup
	for _, i := range asking {
		p, _ := inv.Named(rows[i].Name)
		wg.Add(1)
		go func() {
			defer wg.Done()
			slots <- struct{}{}
			defer func() { <-slots }()
			rows[i] = n.probe(ctx, p)
		}()
	}
	wg.Wait()
}
