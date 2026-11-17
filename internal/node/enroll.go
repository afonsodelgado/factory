package node

import (
	"cmp"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/afonsodelgado/factory/internal/gitclone"
	"github.com/afonsodelgado/factory/internal/phone"
)

const (
	ChallengeLife = 2 * time.Minute
	SyncEvery     = 15 * time.Second
	joinWait      = time.Minute
)

type Nodes struct {
	Self        Member
	Version     string
	Protocol    int
	MinProtocol int
	Dir         *Directory
	Tailscale   Tailscale
	HTTP        *http.Client
	Ready       Readiness
	Factories   func() int
	Repos       func() map[string]string
	Clone       func(ctx context.Context, url string) (string, error)
	Phone       *phone.Store
	ProbeWait   time.Duration
	Now         func() time.Time

	mu      sync.Mutex
	joins   map[string]*join
	invites map[string]*invite
	intents map[string]time.Time
	kick    chan struct{}
}

type join struct {
	target    string
	node      string
	challenge string
}

type invite struct {
	coordinator string
	challenge   string
	expires     time.Time
}

func (n *Nodes) now() time.Time {
	if n.Now == nil {
		return time.Now()
	}
	return n.Now()
}

func (n *Nodes) probeWait() time.Duration {
	if n.ProbeWait == 0 {
		return ProbeWait
	}
	return n.ProbeWait
}

func (n *Nodes) hello() Hello {
	h := Hello{ID: n.Self.ID, Name: n.Self.Name, Version: n.Version, Protocol: n.Protocol, MinProtocol: n.MinProtocol, OS: n.Self.OS, Owner: n.Self.Owner}
	if h.Protocol == 0 {
		h.Protocol, h.MinProtocol = Protocol, MinProtocol
	}
	roster := n.Dir.Roster()
	h.Roster, h.Members = roster.ID, len(roster.Members)
	if n.Factories != nil {
		h.Factories = n.Factories()
	}
	h.Push = n.pushState()
	return h
}

func (n *Nodes) Runnable(ctx context.Context, repo string) error {
	return n.Ready.Run(ctx, n.Self.Name, Scope{Repo: repo}).Runnable(cmp.Or(n.Self.short(), "this node"))
}

var ErrRemoved = errors.New("this node was removed from its node list, so it changes no list; add it again from a node in the list")

func (n *Nodes) removed() bool { return n.Dir.Roster().Removed }

func (n *Nodes) repos() map[string]string {
	if n.Repos == nil {
		return nil
	}
	return n.Repos()
}

func (n *Nodes) repoQuery() string {
	origins := slices.Sorted(maps.Keys(n.repos()))
	if len(origins) == 0 {
		return ""
	}
	query := url.Values{"repo": origins}
	return "?" + query.Encode()
}

type CheckReport struct {
	Target       Candidate `json:"target"`
	Checks       []Check   `json:"checks"`
	Refusal      string    `json:"refusal,omitempty"`
	Problems     int       `json:"problems"`
	PushConflict bool      `json:"push_conflict,omitempty"`
	At           time.Time `json:"at"`
}

func (c CheckReport) refused() bool { return c.Refusal != "" }

type checked struct {
	report    CheckReport
	peer      Peer
	hello     Hello
	direction direction
}

func (n *Nodes) refuse(c checked, check string, r Refusal) checked {
	c.report.Refusal = r.Problem
	c.report.Checks = append(c.report.Checks, Check{Name: cmp.Or(r.Check, check), Mark: MarkFail, Detail: r.Problem, Fix: r.Fix, On: r.On, At: n.now()})
	return c
}

func (n *Nodes) pass(c checked, name, detail string) checked {
	c.report.Checks = append(c.report.Checks, Check{Name: name, Mark: MarkOK, Detail: detail, At: n.now()})
	return c
}

func (n *Nodes) Check(ctx context.Context, typed string) (CheckReport, error) {
	c, err := n.check(ctx, typed, adding)
	return c.report, err
}

type direction int

const (
	adding direction = iota
	addingOnRequest
	joining
)

func (n *Nodes) listConflict(c checked) *Refusal {
	mine := n.Dir.Roster()
	sharedList := c.hello.Roster == mine.ID
	switch {
	case sharedList:
	case c.direction == joining && len(mine.Members) > 1:
		return &Refusal{Check: "node", Problem: "this node already shares a node list with " + fmt.Sprint(len(mine.Members)-1) + " other nodes; remove it there first"}
	case c.direction != joining && c.hello.Members > 1:
		return &Refusal{Check: "node", Problem: c.peer.Short() + " already shares another node list with " + fmt.Sprint(c.hello.Members-1) + " other nodes; remove it there first"}
	}
	return nil
}

func (n *Nodes) check(ctx context.Context, typed string, d direction) (checked, error) {
	c := checked{report: CheckReport{At: n.now()}, direction: d}
	inv, err := n.Tailscale.Inventory(ctx)
	if err != nil {
		return c, err
	}
	peer, err := inv.Resolve(typed)
	if err != nil {
		return c, err
	}
	c.peer = peer
	c.report.Target = Candidate{Name: peer.Name, Short: peer.Short(), OS: peer.OS, LastSeen: peer.LastSeen}
	if err := inv.Verify(peer, n.Self.Owner); err != nil {
		return n.refuse(c, "owner", err.(Refusal)), nil
	}
	if n.enrolled(peer) && d != joining {
		return n.refuse(c, "node", Refusal{Check: "node", Problem: peer.Short() + " is already in the node list"}), nil
	}
	if !peer.Online {
		return n.refuse(c, "tailscale", Refusal{Check: "tailscale", Problem: peer.Short() + " is offline; Tailscale last saw it at " + peer.LastSeen.Format(time.DateTime)}), nil
	}
	c = n.pass(c, "tailscale", peer.Short()+" is online, logged in as "+peer.Login)
	candidate := n.probe(ctx, peer)
	c.report.Target = candidate
	if candidate.State != StateNode {
		return n.refuse(c, "serve", Refusal{Check: refusalCheck(candidate.State), Problem: candidate.Detail, Fix: candidate.Fix, On: candidate.On}), nil
	}
	c.hello = *candidate.Hello
	if refusal := n.listConflict(c); refusal != nil {
		return n.refuse(c, "node", *refusal), nil
	}
	c = n.pass(c, "node", c.hello.Version+" · "+peer.OS)
	c = n.pass(c, "owner", c.hello.Owner+", same as you")
	c = n.pass(c, "funnel", "off")
	switch d {
	case joining:
		return c, nil
	case addingOnRequest:
		return n.pushCheck(c), nil
	}
	c, err = n.withReadiness(ctx, c)
	if c.report.refused() {
		return c, err
	}
	return n.pushCheck(c), err
}

func refusalCheck(state string) string {
	switch state {
	case StateFunnel:
		return "funnel"
	case StateOtherOwner:
		return "owner"
	case StateIncompatible:
		return "node"
	}
	return "serve"
}

func (n *Nodes) withReadiness(ctx context.Context, c checked) (checked, error) {
	wait := n.Ready.Wait
	if wait == 0 {
		wait = ReadyWait
	}
	ctx, cancel := context.WithTimeout(ctx, wait+n.probeWait())
	defer cancel()
	var report Report
	err := Remote(c.peer.Name, n.HTTP).Do(ctx, http.MethodGet, "/api/node/readiness"+n.repoQuery(), nil, &report)
	switch {
	case errors.Is(err, context.Canceled):
		return c, err
	case err != nil:
		return n.refuse(c, "node", Refusal{Check: "node", Problem: c.peer.Short() + " stopped answering during the check; nothing was written"}), nil
	}
	for _, check := range report.Checks {
		if check.Fix != "" && check.On == "" {
			check.On = c.peer.Short()
		}
		c.report.Checks = append(c.report.Checks, check)
	}
	c.report.Problems = report.Problems
	return c, nil
}

func (n *Nodes) enrolled(p Peer) bool {
	return slices.ContainsFunc(n.Dir.Roster().Members, func(m RosterMember) bool { return m.Name == p.Name || (m.Peer != "" && m.Peer == p.ID) })
}

type AddResult struct {
	Member Member      `json:"member"`
	Report CheckReport `json:"report"`
	Roster Roster      `json:"roster"`
	Push   PushOutcome `json:"push"`
}

type RefusedError struct{ Report CheckReport }

func (e RefusedError) Error() string { return e.Report.Refusal }

func (n *Nodes) Add(ctx context.Context, op, typed string, replaceKey bool) (AddResult, error) {
	if done, ok := n.Dir.Op(op); ok {
		return AddResult{Member: done.Member, Roster: n.Dir.Roster(), Push: n.helloPush(ctx, done.Member)}, nil
	}
	if n.removed() {
		return AddResult{}, ErrRemoved
	}
	c, err := n.check(ctx, typed, adding)
	if err != nil {
		return AddResult{Report: c.report}, err
	}
	if c.report.refused() {
		return AddResult{Report: c.report}, RefusedError{Report: c.report}
	}
	m, push, err := n.enroll(ctx, op, c, "", replaceKey)
	return AddResult{Member: m, Report: c.report, Roster: n.Dir.Roster(), Push: push}, err
}

func (n *Nodes) memberOf(c checked) Member {
	_, tailnet, _ := strings.Cut(c.peer.Name, ".")
	return Member{ID: c.hello.ID, Peer: c.peer.ID, Name: c.peer.Name, Owner: c.peer.Login, Tailnet: tailnet, OS: c.peer.OS}
}

func (n *Nodes) enroll(ctx context.Context, op string, c checked, intent string, replaceKey bool) (Member, PushOutcome, error) {
	id := uuid.NewString()
	n.mu.Lock()
	if n.joins == nil {
		n.joins = map[string]*join{}
	}
	pending := &join{target: c.peer.Name}
	n.joins[id] = pending
	n.mu.Unlock()
	defer func() {
		n.mu.Lock()
		delete(n.joins, id)
		n.mu.Unlock()
	}()
	ctx, cancel := context.WithTimeout(ctx, joinWait)
	defer cancel()
	target := Remote(c.peer.Name, n.HTTP)
	var theirs Ledger
	if err := target.Do(ctx, http.MethodPost, "/api/node/pull", Pull{From: n.Self.ID}, &theirs); err != nil {
		return Member{}, PushOutcome{}, fmt.Errorf("%s stopped answering before it joined; nothing was written: %w", c.peer.Short(), err)
	}
	var prepared Joined
	if err := target.Do(ctx, http.MethodPost, "/api/node/join/prepare", Prepare{Join: id, Coordinator: n.Self.Name, Intent: intent}, &prepared); err != nil {
		return Member{}, PushOutcome{}, fmt.Errorf("%s refused to join; nothing was written: %w", c.peer.Short(), err)
	}
	n.mu.Lock()
	challenge, called := pending.challenge, pending.node
	n.mu.Unlock()
	if challenge == "" || called != prepared.Node || prepared.Node != c.hello.ID {
		return Member{}, PushOutcome{}, fmt.Errorf("%s did not prove it is the node that answered the check; nothing was written", c.peer.Short())
	}
	m := n.memberOf(c)
	ledger := n.Dir.Draft(m, op, theirs)
	if _, stillListed := ledger.present()[n.Self.ID]; !stillListed {
		return Member{}, PushOutcome{}, fmt.Errorf("%s's history removes this node, so this node cannot add it; nothing was written", c.peer.Short())
	}
	push, sharedKey, err := n.pushToSend(prepared.Push, replaceKey)
	if err != nil {
		return Member{}, PushOutcome{}, err
	}
	var committed Joined
	commit := Commit{Join: id, Challenge: challenge, Ledger: ledger, Push: push, SharedKey: sharedKey, ReplaceKey: replaceKey}
	if err := target.Do(ctx, http.MethodPost, "/api/node/join/commit", commit, &committed); err != nil {
		return Member{}, PushOutcome{}, fmt.Errorf("%s did not take the node list: %w", c.peer.Short(), err)
	}
	if committed.Node != c.hello.ID {
		return Member{}, PushOutcome{}, fmt.Errorf("%s answered as another node during the join", c.peer.Short())
	}
	if err := n.Dir.Merge(ledger); err != nil {
		return Member{}, PushOutcome{}, err
	}
	n.Dir.Ack(m.ID, committed.Clock)
	n.Kick()
	return m, n.pushOutcome(c.peer.Short(), committed.Push), nil
}

func (n *Nodes) Remove(op, id string) (Roster, error) {
	switch {
	case id == n.Self.ID:
		return Roster{}, ErrRemoveSelf
	case n.removed():
		return Roster{}, ErrRemoved
	}
	if err := n.Dir.Remove(id, op); err != nil {
		return Roster{}, err
	}
	n.Kick()
	return n.Dir.Roster(), nil
}

var ErrRemoveSelf = errors.New("this is the machine the node runs on. Removing it from the list does not stop it; to stop this node, run factory node uninstall here")

func (n *Nodes) Join(ctx context.Context, op, typed string) (AddResult, error) {
	c, err := n.check(ctx, typed, joining)
	if err != nil {
		return AddResult{Report: c.report}, err
	}
	if c.report.refused() {
		return AddResult{Report: c.report}, RefusedError{Report: c.report}
	}
	n.mu.Lock()
	if n.intents == nil {
		n.intents = map[string]time.Time{}
	}
	n.intents[op] = n.now().Add(joinWait)
	n.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, joinWait)
	defer cancel()
	var result AddResult
	err = Remote(c.peer.Name, n.HTTP).Do(ctx, http.MethodPost, "/api/node/join/request", JoinRequest{Operation: op, Name: n.Self.Name}, &result)
	if err != nil {
		return AddResult{Report: c.report}, fmt.Errorf("%s did not add this node: %w", c.peer.Short(), err)
	}
	result.Report = c.report
	result.Roster = n.Dir.Roster()
	return result, nil
}

func (n *Nodes) Kick() {
	n.mu.Lock()
	if n.kick == nil {
		n.kick = make(chan struct{}, 1)
	}
	kick := n.kick
	n.mu.Unlock()
	select {
	case kick <- struct{}{}:
	default:
	}
}

func (n *Nodes) Replicate(ctx context.Context) {
	n.Kick()
	n.mu.Lock()
	kick := n.kick
	n.mu.Unlock()
	ticker := time.NewTicker(SyncEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-kick:
		}
		n.Sync(ctx)
	}
}

func (n *Nodes) Sync(ctx context.Context) {
	roster := n.Dir.Roster()
	if roster.Removed {
		return
	}
	inv, err := n.Tailscale.Inventory(ctx)
	if err != nil {
		return
	}
	var wg sync.WaitGroup
	reach := func(m Member, step func(context.Context, Member)) {
		if !n.stillTheSamePeer(inv, m) {
			return
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			step(ctx, m)
		}()
	}
	for _, m := range roster.Members {
		if !m.Self {
			reach(m.Member, n.exchange)
		}
	}
	for _, m := range n.Dir.Removed() {
		reach(m, n.tellRemoved)
	}
	wg.Wait()
}

func (n *Nodes) stillTheSamePeer(inv Inventory, m Member) bool {
	p, ok := inv.Named(m.Name)
	samePeer := m.Peer == "" || p.ID == m.Peer
	return ok && samePeer && inv.Verify(p, n.Self.Owner) == nil
}

func (n *Nodes) exchange(ctx context.Context, m Member) {
	ctx, cancel := context.WithTimeout(ctx, n.probeWait()*2)
	defer cancel()
	var theirs Ledger
	if err := Remote(m.Name, n.HTTP).Do(ctx, http.MethodPost, "/api/node/pull", Pull{From: n.Self.ID}, &theirs); err == nil {
		n.Dir.Merge(theirs)
	}
	n.sendRecords(ctx, m)
	n.tellRemoved(ctx, m)
}

func (n *Nodes) tellRemoved(ctx context.Context, m Member) {
	var kicked Kicked
	if err := Remote(m.Name, n.HTTP).Do(ctx, http.MethodPost, "/api/node/kick", Pull{From: n.Self.ID}, &kicked); err == nil {
		n.Dir.Ack(m.ID, kicked.Clock)
	}
}

func (n *Nodes) pullFrom(ctx context.Context, m Member) error {
	inv, err := n.Tailscale.Inventory(ctx)
	if err != nil {
		return err
	}
	if !n.stillTheSamePeer(inv, m) {
		return fmt.Errorf("tailscale status no longer shows %s as this owner's machine", m.Name)
	}
	ctx, cancel := context.WithTimeout(ctx, n.probeWait()*2)
	defer cancel()
	var theirs Ledger
	if err := Remote(m.Name, n.HTTP).Do(ctx, http.MethodPost, "/api/node/pull", Pull{From: n.Self.ID}, &theirs); err != nil {
		return err
	}
	return n.Dir.Merge(theirs)
}

func (n *Nodes) verifiedPeer(ctx context.Context, name string) (Peer, error) {
	inv, err := n.Tailscale.Inventory(ctx)
	if err != nil {
		return Peer{}, err
	}
	p, ok := inv.Named(name)
	if !ok {
		return Peer{}, NotFoundError{Typed: name, Tailnet: inv.Self.Tailnet}
	}
	return p, inv.Verify(p, n.Self.Owner)
}

func (n *Nodes) prepare(ctx context.Context, p Prepare) (Joined, error) {
	coordinator, err := n.verifiedPeer(ctx, p.Coordinator)
	if err != nil {
		return Joined{}, err
	}
	if p.Intent != "" && !n.takeIntent(p.Intent) {
		return Joined{}, errors.New("this node asked to join no such operation")
	}
	challenge, err := newChallenge()
	if err != nil {
		return Joined{}, err
	}
	n.mu.Lock()
	if n.invites == nil {
		n.invites = map[string]*invite{}
	}
	n.invites[p.Join] = &invite{coordinator: coordinator.Name, challenge: challenge, expires: n.now().Add(ChallengeLife)}
	n.mu.Unlock()
	var answered Joined
	err = Remote(coordinator.Name, n.HTTP).Do(ctx, http.MethodPost, "/api/node/join/verify", Verify{Join: p.Join, Node: n.Self.ID, Name: n.Self.Name, Challenge: challenge}, &answered)
	if err != nil {
		n.dropInvite(p.Join)
		return Joined{}, fmt.Errorf("%s did not confirm the join: %w", coordinator.Short(), err)
	}
	return Joined{Node: n.Self.ID, Push: n.pushState()}, nil
}

func (n *Nodes) dropInvite(id string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	delete(n.invites, id)
}

func (n *Nodes) takeIntent(op string) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	expires, ok := n.intents[op]
	delete(n.intents, op)
	return ok && n.now().Before(expires)
}

func newChallenge() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

var errNoJoin = errors.New("no such join is waiting here")

func (n *Nodes) verify(v Verify) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	j := n.joins[v.Join]
	switch {
	case j == nil:
		return errNoJoin
	case j.target != v.Name || j.challenge != "":
		return errors.New("this join waits for another node")
	}
	j.node, j.challenge = v.Node, v.Challenge
	return nil
}

func (n *Nodes) commit(ctx context.Context, c Commit) (Joined, error) {
	n.mu.Lock()
	inv := n.invites[c.Join]
	delete(n.invites, c.Join)
	n.mu.Unlock()
	switch {
	case inv == nil:
		return Joined{}, errNoJoin
	case !n.now().Before(inv.expires):
		return Joined{}, errors.New("this join expired; add the node again")
	case subtle.ConstantTimeCompare([]byte(inv.challenge), []byte(c.Challenge)) != 1:
		return Joined{}, errors.New("this join's challenge does not match")
	}
	if _, err := n.verifiedPeer(ctx, inv.coordinator); err != nil {
		return Joined{}, err
	}
	if listed, ok := c.Ledger.present()[n.Self.ID]; !ok || listed.Name != n.Self.Name || !strings.HasPrefix(c.Ledger.Home, "https://") {
		return Joined{}, errors.New("the node list sent with this join does not list this node under its own name with an https home")
	}
	if err := n.stagePush(c); err != nil {
		return Joined{}, err
	}
	if err := n.Dir.Adopt(c.Ledger); err != nil {
		n.unstagePush()
		return Joined{}, err
	}
	pushed, err := n.takePush(c)
	n.Kick()
	return Joined{Node: n.Self.ID, Clock: n.Dir.Clock(), Push: pushed}, err
}

func (n *Nodes) requested(ctx context.Context, r JoinRequest) (AddResult, error) {
	if n.removed() {
		return AddResult{}, ErrRemoved
	}
	c, err := n.check(ctx, r.Name, addingOnRequest)
	if err != nil {
		return AddResult{}, err
	}
	if c.report.refused() {
		return AddResult{Report: c.report}, RefusedError{Report: c.report}
	}
	if done, ok := n.Dir.Op(r.Operation); ok {
		return AddResult{Member: done.Member, Roster: n.Dir.Roster(), Push: n.helloPush(ctx, done.Member)}, nil
	}
	m, push, err := n.enroll(ctx, r.Operation, c, r.Operation, false)
	return AddResult{Member: m, Report: c.report, Roster: n.Dir.Roster(), Push: push}, err
}

func (n *Nodes) kicked(ctx context.Context, from string) (Kicked, bool) {
	if n.Dir.Roster().Removed {
		return Kicked{Clock: n.Dir.Clock()}, true
	}
	m, member := n.Dir.Member(from)
	if !member {
		return Kicked{}, false
	}
	n.pullFrom(ctx, m)
	return Kicked{Clock: n.Dir.Clock()}, true
}

func (n *Nodes) routes(route func(string, func(http.ResponseWriter, *http.Request, string))) {
	route("GET /api/nodes", func(w http.ResponseWriter, r *http.Request, _ string) { reply(w, http.StatusOK, n.Dir.Roster()) })
	route("GET /api/nodes/candidates", n.candidates)
	route("POST /api/nodes/check", n.homeOnly(n.checkRoute))
	route("POST /api/nodes", n.homeOnly(n.addRoute))
	route("POST /api/nodes/{id}/remove", n.homeOnly(n.removeRoute))
	route("POST /api/nodes/join", socketOnly(n.joinRoute))
	route("GET /api/readiness", n.readinessWithPush)
	route("GET /api/repos", func(w http.ResponseWriter, r *http.Request, _ string) { reply(w, http.StatusOK, n.repos()) })
	route("POST /api/clone", n.homeOrNative(n.cloneRoute))
	route("GET /api/node/hello", nativeOnly(func(w http.ResponseWriter, r *http.Request, _ string) { reply(w, http.StatusOK, n.hello()) }))
	route("GET /api/node/readiness", nativeOnly(n.readinessRoute))
	route("POST /api/node/pull", nativeOnly(n.pullRoute))
	route("POST /api/node/kick", nativeOnly(n.kickRoute))
	route("POST /api/node/join/request", nativeOnly(n.requestRoute))
	route("POST /api/node/join/prepare", nativeOnly(n.prepareRoute))
	route("POST /api/node/join/verify", nativeOnly(n.verifyRoute))
	route("POST /api/node/join/commit", nativeOnly(n.commitRoute))
	route("POST /api/node/push/records", nativeOnly(n.recordsRoute))
}

func socketOnly(serve func(http.ResponseWriter, *http.Request, string)) func(http.ResponseWriter, *http.Request, string) {
	return func(w http.ResponseWriter, r *http.Request, caller string) {
		if !viaSocket(r) {
			fail(w, http.StatusNotFound, "joining runs only from factory node install on this machine")
			return
		}
		serve(w, r, caller)
	}
}

type Candidates struct {
	Candidates []Candidate `json:"candidates"`
	At         time.Time   `json:"at"`
}

func (n *Nodes) candidates(w http.ResponseWriter, r *http.Request, _ string) {
	rows, err := n.Discover(r.Context())
	n.answer(w, Candidates{Candidates: rows, At: n.now()}, err)
}

func (n *Nodes) checkRoute(w http.ResponseWriter, r *http.Request, _ string) {
	var body NodeRequest
	if err := decode(r, &body); err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	report, err := n.Check(r.Context(), body.Name)
	n.answer(w, report, err)
}

func (n *Nodes) addRoute(w http.ResponseWriter, r *http.Request, _ string) {
	var body NodeRequest
	if err := decode(r, &body); err != nil || uuid.Validate(body.Operation) != nil {
		fail(w, http.StatusBadRequest, "send {operation: a UUID, name}")
		return
	}
	result, err := n.Add(r.Context(), body.Operation, body.Name, body.ReplaceKey)
	n.answer(w, result, err)
}

func (n *Nodes) joinRoute(w http.ResponseWriter, r *http.Request, _ string) {
	var body NodeRequest
	if err := decode(r, &body); err != nil || uuid.Validate(body.Operation) != nil {
		fail(w, http.StatusBadRequest, "send {operation: a UUID, name}")
		return
	}
	result, err := n.Join(r.Context(), body.Operation, body.Name)
	n.answer(w, result, err)
}

func (n *Nodes) removeRoute(w http.ResponseWriter, r *http.Request, _ string) {
	var body RemoveRequest
	if err := decode(r, &body); err != nil || uuid.Validate(body.Operation) != nil {
		fail(w, http.StatusBadRequest, "send {operation: a UUID}")
		return
	}
	roster, err := n.Remove(body.Operation, r.PathValue("id"))
	n.answer(w, roster, err)
}

func (n *Nodes) readiness(r *http.Request) Report {
	wanted := slices.DeleteFunc(r.URL.Query()["repo"], func(url string) bool { return gitclone.CheckURL(url) != nil })
	return n.Ready.Run(r.Context(), n.Self.Name, Scope{Wanted: wanted})
}

func (n *Nodes) readinessRoute(w http.ResponseWriter, r *http.Request, _ string) {
	reply(w, http.StatusOK, n.readiness(r))
}

type CloneRequest struct {
	Operation string `json:"operation"`
	URL       string `json:"url"`
}

type Cloned struct {
	Repo string `json:"repo"`
}

func (n *Nodes) cloneRoute(w http.ResponseWriter, r *http.Request, _ string) {
	var body CloneRequest
	if err := decode(r, &body); err != nil || uuid.Validate(body.Operation) != nil {
		fail(w, http.StatusBadRequest, "send {operation: a UUID, url}")
		return
	}
	if err := gitclone.CheckURL(body.URL); err != nil || n.Clone == nil {
		fail(w, http.StatusBadRequest, fmt.Sprintf("this node clones only network git remotes: %v", err))
		return
	}
	repo, err := n.Clone(r.Context(), body.URL)
	n.answer(w, Cloned{Repo: repo}, err)
}

func (n *Nodes) pullRoute(w http.ResponseWriter, r *http.Request, _ string) {
	var body Pull
	if err := decode(r, &body); err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	reply(w, http.StatusOK, n.Dir.For(body.From))
}

func (n *Nodes) kickRoute(w http.ResponseWriter, r *http.Request, _ string) {
	var body Pull
	if err := decode(r, &body); err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	kicked, ok := n.kicked(r.Context(), body.From)
	if !ok {
		fail(w, http.StatusGone, "that node is not in this node list")
		return
	}
	reply(w, http.StatusOK, kicked)
}

func (n *Nodes) requestRoute(w http.ResponseWriter, r *http.Request, _ string) {
	var body JoinRequest
	if err := decode(r, &body); err != nil || uuid.Validate(body.Operation) != nil {
		fail(w, http.StatusBadRequest, "send {operation: a UUID, name}")
		return
	}
	result, err := n.requested(r.Context(), body)
	n.answer(w, result, err)
}

func (n *Nodes) prepareRoute(w http.ResponseWriter, r *http.Request, _ string) {
	var body Prepare
	if err := decode(r, &body); err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	joined, err := n.prepare(r.Context(), body)
	n.answer(w, joined, err)
}

func (n *Nodes) verifyRoute(w http.ResponseWriter, r *http.Request, _ string) {
	var body Verify
	if err := decode(r, &body); err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	n.answer(w, Joined{Node: n.Self.ID}, n.verify(body))
}

func (n *Nodes) commitRoute(w http.ResponseWriter, r *http.Request, _ string) {
	var body Commit
	if err := decode(r, &body); err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	joined, err := n.commit(r.Context(), body)
	n.answer(w, joined, err)
}

func (n *Nodes) answer(w http.ResponseWriter, value any, err error) {
	var refused RefusedError
	var notFound NotFoundError
	var refusal Refusal
	switch {
	case err == nil:
		reply(w, http.StatusOK, value)
	case errors.As(err, &refused):
		reply(w, http.StatusConflict, map[string]any{"error": err.Error(), "report": refused.Report})
	case errors.As(err, &notFound), errors.Is(err, errNoJoin):
		fail(w, http.StatusNotFound, err.Error())
	case errors.As(err, &refusal):
		reply(w, http.StatusForbidden, map[string]any{"error": refusal.Problem, "fix": refusal.Fix, "on": refusal.On})
	default:
		fail(w, http.StatusConflict, err.Error())
	}
}
