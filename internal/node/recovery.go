package node

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/afonsodelgado/factory/internal/host"
)

const (
	TakeoverRoute     = "POST /api/node/transfer/takeover"
	holderPoll        = 30 * time.Second
	holderWait        = 20 * time.Second
	TakeoverWarning   = "Forcing a takeover starts %s on %s from the checkpoint %s took at %s. The node that holds %s now may only be out of reach: it can keep running %s until it reconnects and learns it lost it, and work done after that checkpoint may be lost or diverge."
	takeoverTimestamp = "15:04 on 2 Jan"
)

type TakeoverRequest struct {
	Operation string `json:"operation"`
	Factory   string `json:"factory"`
	Target    string `json:"target,omitempty"`
	Confirm   bool   `json:"confirm,omitempty"`
}

type Unconfirmed struct {
	Warning     string    `json:"warning"`
	Factory     string    `json:"factory"`
	Incarnation string    `json:"incarnation"`
	Checkpoint  string    `json:"checkpoint"`
	From        string    `json:"from"`
	At          time.Time `json:"at"`
}

func (u Unconfirmed) Error() string { return u.Warning }

func (ts *Transfers) Takeover(ctx context.Context, r TakeoverRequest) (Transfer, error) {
	t, found, err := LoadTransfer(ts.StateDir, r.Operation)
	switch {
	case err != nil:
		return t, err
	case found && t.Refusal != "":
		return t, errors.New(t.Refusal)
	case found && t.Phase == PhaseActive:
		return t, nil
	case found && t.reached(PhaseStaged):
		m, err := ts.stagedManifest(t)
		if err != nil {
			return ts.refused(t, err)
		}
		return ts.takeOver(ctx, t, m)
	}
	self := ts.Nodes.Self
	t = Transfer{Operation: r.Operation, Role: RoleTarget, Forced: true, Factory: r.Factory, Target: self.ID, TargetName: self.short()}
	cp, staged, ok := ts.retained(r.Factory)
	if !ok {
		return ts.refused(t, fmt.Errorf("this node has no checkpoint of %s, so it cannot take it over; a forced takeover restarts a factory only from a checkpoint a move left on this node", r.Factory))
	}
	m := cp.Manifest
	if !r.Confirm && !found {
		return Transfer{}, ts.unconfirmed(m)
	}
	t.Incarnation, t.Factory, t.Source, t.SourceName, t.GitURL, t.Checkpoint = m.Incarnation, m.Factory, m.Source, ts.nodeName(m.Source), m.GitURL, m.Digest()
	if t, err = ts.record(t, PhaseRequested); err != nil {
		return t, err
	}
	factories, err := ts.local(ctx)
	if err != nil {
		return ts.failed(t, err)
	}
	if slices.ContainsFunc(factories, func(f host.LocalFactory) bool { return f.Incarnation == m.Incarnation }) {
		return ts.refused(t, fmt.Errorf("%s already runs on this node", m.Factory))
	}
	if !staged {
		if err := ts.write(cp); err != nil {
			return ts.failed(t, err)
		}
	}
	if _, t.Lease, err = ts.Holders.Read(ctx, m.GitURL, m.Incarnation); err != nil {
		return ts.failed(t, err)
	}
	if t, err = ts.record(t, PhaseStaged); err != nil {
		return t, err
	}
	return ts.takeOver(ctx, t, m)
}

func (ts *Transfers) unconfirmed(m Manifest) Unconfirmed {
	from := ts.nodeName(m.Source)
	warning := fmt.Sprintf(TakeoverWarning, m.Factory, ts.Nodes.Self.short(), from, m.At.Local().Format(takeoverTimestamp), m.Factory, m.Factory)
	return Unconfirmed{Warning: warning, Factory: m.Factory, Incarnation: m.Incarnation, Checkpoint: m.Digest(), From: from, At: m.At}
}

func (ts *Transfers) nodeName(id string) string {
	if m, ok := ts.Nodes.Dir.Member(id); ok {
		return m.short()
	}
	return id
}

func (ts *Transfers) retained(factoryID string) (Checkpoint, bool, bool) {
	var newest Checkpoint
	staged, found := false, false
	consider := func(cp Checkpoint, fromStaged bool) {
		m := cp.Manifest
		if (m.Factory == factoryID || m.Incarnation == factoryID) && (!found || m.At.After(newest.Manifest.At)) {
			newest, staged, found = cp, fromStaged, true
		}
	}
	kept, _ := filepath.Glob(filepath.Join(ts.StateDir, "checkpoints", "*.json"))
	for _, path := range kept {
		var cp Checkpoint
		if data, err := os.ReadFile(path); err == nil && json.Unmarshal(data, &cp) == nil {
			consider(cp, false)
		}
	}
	dirs, _ := filepath.Glob(filepath.Join(ts.StateDir, "transfers", "staged", "*"))
	for _, dir := range dirs {
		if cp, err := readStaged(dir); err == nil {
			consider(cp, true)
		}
	}
	return newest, staged, found && newest.Validate(ts.limits()) == nil
}

func readStaged(dir string) (Checkpoint, error) {
	m, err := readManifest(dir)
	if err != nil {
		return Checkpoint{}, err
	}
	cp := Checkpoint{Manifest: m}
	files := filepath.Join(dir, "files")
	err = filepath.WalkDir(files, func(full string, d fs.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() {
			return err
		}
		rel, _ := filepath.Rel(files, full)
		data, err := os.ReadFile(full)
		cp.Files = append(cp.Files, Payload{Path: filepath.ToSlash(rel), Data: data})
		return err
	})
	return cp, err
}

func (ts *Transfers) watched(incarnation string) (string, bool) {
	ts.mu.Lock()
	gitURL, known := ts.remotes[incarnation]
	ts.mu.Unlock()
	if known {
		return gitURL, true
	}
	gitURL, retained := RetainedRemote(ts.StateDir, incarnation)
	if !retained {
		return "", false
	}
	ts.mu.Lock()
	defer ts.mu.Unlock()
	if ts.remotes == nil {
		ts.remotes = map[string]string{}
	}
	ts.remotes[incarnation] = gitURL
	return gitURL, true
}

func RetainedRemote(stateDir, incarnation string) (string, bool) {
	if m, err := readManifest(StagedDir(stateDir, incarnation)); err == nil {
		return m.GitURL, true
	}
	var kept struct{ Manifest Manifest }
	data, err := os.ReadFile(CheckpointPath(stateDir, incarnation))
	if err != nil || json.Unmarshal(data, &kept) != nil {
		return "", false
	}
	return kept.Manifest.GitURL, true
}

type Listed struct {
	Factory     string
	Incarnation string
}

func Diagnose(ctx context.Context, stateDir, self string, moves []Progress, listed []Listed) []Check {
	var checks []Check
	for _, p := range moves {
		if c, stuck := stuckMove(p); stuck {
			checks = append(checks, c)
		}
	}
	scratch, err := os.MkdirTemp("", "factory-doctor-holders-")
	if err != nil {
		return append(checks, Check{Name: "holders", Mark: MarkWarn, Detail: err.Error()})
	}
	defer os.RemoveAll(scratch)
	holders := &Holders{Dir: scratch}
	for _, f := range listed {
		gitURL, retained := RetainedRemote(stateDir, f.Incarnation)
		if !retained {
			continue
		}
		h, sha, err := holders.Read(ctx, gitURL, f.Incarnation)
		switch {
		case err != nil:
			checks = append(checks, Check{Name: "holder", Mark: MarkWarn, Detail: "could not check who holds " + f.Factory + ": " + err.Error()})
		case sha != "" && h.Node != self:
			checks = append(checks, Check{Name: "holder", Mark: MarkFail, Detail: fmt.Sprintf("the git remote says node %s holds %s at generation %d, but this node still lists it; a running node stops it at its next check", h.Node, f.Factory, h.Generation), Fix: "factory node run"})
		}
	}
	if len(checks) == 0 {
		checks = append(checks, Check{Name: "moves", Mark: MarkOK, Detail: "no move is stuck, and this node holds every moved factory it lists"})
	}
	return checks
}

func stuckMove(p Progress) (Check, bool) {
	switch {
	case p.State == StateRecoveryNeeded && p.Role == RoleSource:
		return Check{Name: "move", Mark: MarkFail, Detail: p.Recovery[0], Lines: p.Recovery[1:], Fix: "factory move " + p.Factory + " --to " + p.TargetName}, true
	case p.State == StateRecoveryNeeded, p.State == StateInterrupted && p.Role == RoleTarget:
		return Check{Name: "move", Mark: MarkFail, Detail: p.Recovery[0], Fix: "factory move " + p.Factory + " --to " + p.TargetName + " --force"}, true
	case p.State == StateInterrupted:
		return Check{Name: "move", Mark: MarkWarn, Detail: p.Recovery[0], Fix: "factory move " + p.Factory + " --to " + p.TargetName}, true
	}
	return Check{}, false
}

func (ts *Transfers) local(ctx context.Context) ([]host.LocalFactory, error) {
	var local host.Local
	err := ts.host(ctx, "local", nil, &local)
	return local.Factories, err
}

func (ts *Transfers) HoldUnverified(ctx context.Context) {
	factories, _ := ts.local(ctx)
	for _, f := range factories {
		if _, watched := ts.watched(f.Incarnation); watched && !ts.isVerified(f.Incarnation) {
			ts.hold(ctx, f.Key, host.Holding{Unverified: "this node is checking with the git remote who holds " + f.ID + " and does not resume it until it knows"})
		}
	}
}

func (ts *Transfers) Watch(ctx context.Context) {
	every := ts.HolderPoll
	if every == 0 {
		every = holderPoll
	}
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		ts.Reconcile(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (ts *Transfers) Reconcile(ctx context.Context) {
	factories, err := ts.local(ctx)
	if err != nil {
		return
	}
	for _, f := range factories {
		if gitURL, watched := ts.watched(f.Incarnation); watched {
			ts.check(ctx, f, gitURL)
		}
	}
}

func (ts *Transfers) check(ctx context.Context, f host.LocalFactory, gitURL string) {
	readCtx, cancel := context.WithTimeout(ctx, holderWait)
	h, sha, err := ts.Holders.Read(readCtx, gitURL, f.Incarnation)
	cancel()
	switch {
	case err != nil && !ts.isVerified(f.Incarnation):
		ts.hold(ctx, f.Key, host.Holding{Unverified: "this node cannot check with the git remote who holds " + f.ID + ", so it does not resume it: " + err.Error()})
	case err != nil:
	case sha != "" && h.Node != ts.Nodes.Self.ID:
		ts.lose(ctx, f, h)
	default:
		ts.markVerified(f.Incarnation)
		ts.hold(ctx, f.Key, host.Holding{Generation: h.Generation})
	}
}

func (ts *Transfers) lose(ctx context.Context, f host.LocalFactory, h Holder) {
	release := host.ReleaseRequest{Repo: f.Repo, Slug: strings.TrimPrefix(f.Key, f.Repo+":"), To: ts.nodeName(h.Node), Operation: h.Operation}
	if ts.host(ctx, "release", release, nil) == nil {
		ts.mu.Lock()
		delete(ts.sent, f.Key)
		delete(ts.verified, f.Incarnation)
		ts.mu.Unlock()
	}
}

func (ts *Transfers) hold(ctx context.Context, key string, h host.Holding) {
	ts.mu.Lock()
	if ts.sent == nil {
		ts.sent = map[string]host.Holding{}
	}
	previous, sent := ts.sent[key]
	ts.mu.Unlock()
	if sent && previous == h {
		return
	}
	if ts.host(ctx, "holding", host.HoldingRequest{Key: key, Holding: h}, nil) == nil {
		ts.mu.Lock()
		ts.sent[key] = h
		ts.mu.Unlock()
	}
}

func (ts *Transfers) markVerified(incarnation string) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	if ts.verified == nil {
		ts.verified = map[string]bool{}
	}
	ts.verified[incarnation] = true
}

func (ts *Transfers) isVerified(incarnation string) bool {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	return ts.verified[incarnation]
}
