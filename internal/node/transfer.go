package node

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/afonsodelgado/factory/internal/gitclone"
	"github.com/afonsodelgado/factory/internal/host"
	"github.com/afonsodelgado/factory/internal/state"
)

const (
	TransferSchema  = 1
	PhaseRequested  = "requested"
	PhasePreflight  = "preflight"
	PhaseCheckpoint = "checkpoint"
	PhaseStaged     = "staged"
	PhaseReleased   = "released"
	PhaseActive     = "active"
	RoleSource      = "source"
	RoleTarget      = "target"
	checkpointPoll  = 2 * time.Second
	PreflightRoute  = "POST /api/node/transfer/preflight"
	StageRoute      = "POST /api/node/transfer/stage"
)

var phases = []string{PhaseRequested, PhasePreflight, PhaseCheckpoint, PhaseStaged, PhaseReleased, PhaseActive}

type Transfer struct {
	Schema      int       `json:"schema"`
	Operation   string    `json:"operation"`
	Role        string    `json:"role"`
	Incarnation string    `json:"incarnation"`
	Factory     string    `json:"factory"`
	Source      string    `json:"source"`
	Target      string    `json:"target"`
	Phase       string    `json:"phase"`
	Checkpoint  string    `json:"checkpoint,omitempty"`
	Refusal     string    `json:"refusal,omitempty"`
	At          time.Time `json:"at"`
}

func TransferPath(stateDir, operation string) string {
	return filepath.Join(stateDir, "transfers", operation+".json")
}

func StagedDir(stateDir, incarnation string) string {
	return filepath.Join(stateDir, "transfers", "staged", incarnation)
}

func LoadTransfer(stateDir, operation string) (Transfer, bool, error) {
	var t Transfer
	data, err := os.ReadFile(TransferPath(stateDir, operation))
	if errors.Is(err, fs.ErrNotExist) {
		return t, false, nil
	}
	if err != nil {
		return t, false, err
	}
	if err := json.Unmarshal(data, &t); err != nil {
		return t, false, err
	}
	if t.Schema != TransferSchema {
		return t, false, fmt.Errorf("transfer %s has schema %d, not %d", operation, t.Schema, TransferSchema)
	}
	return t, true, nil
}

func (t Transfer) reached(phase string) bool {
	return slices.Index(phases, t.Phase) >= slices.Index(phases, phase)
}

func (ts *Transfers) record(t Transfer, phase string) (Transfer, error) {
	if t.Phase == "" || !t.reached(phase) {
		t.Phase = phase
	}
	t.Schema, t.At = TransferSchema, ts.Nodes.now()
	return t, host.SavePrivate(TransferPath(ts.StateDir, t.Operation), t)
}

func (ts *Transfers) refused(t Transfer, err error) (Transfer, error) {
	interrupted := errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
	if interrupted {
		return t, err
	}
	t.Refusal, t.At = err.Error(), ts.Nodes.now()
	if saveErr := host.SavePrivate(TransferPath(ts.StateDir, t.Operation), t); saveErr != nil {
		return t, errors.Join(err, saveErr)
	}
	return t, err
}

type Transfers struct {
	Commands
	Nodes    *Nodes
	StateDir string
	Holds    func(incarnation string) bool
	Limits   Limits
	Poll     time.Duration
}

func (ts *Transfers) limits() Limits {
	if ts.Limits == (Limits{}) {
		return DefaultLimits
	}
	return ts.Limits
}

func (ts *Transfers) poll() time.Duration {
	if ts.Poll == 0 {
		return checkpointPoll
	}
	return ts.Poll
}

func (ts *Transfers) Stage(ctx context.Context, operation, key, target string) (Transfer, error) {
	t, found, err := LoadTransfer(ts.StateDir, operation)
	switch {
	case err != nil:
		return t, err
	case found && t.Refusal != "":
		return t, errors.New(t.Refusal)
	case found && t.reached(PhaseStaged):
		return t, nil
	case !found:
		t = Transfer{Operation: operation, Role: RoleSource, Source: ts.Nodes.Self.ID}
	}
	movable, err := ts.movable(ctx, key)
	if err != nil {
		return ts.refused(t, err)
	}
	t.Incarnation, t.Factory = movable.Incarnation, movable.ID
	peer, err := ts.destination(ctx, target)
	if err != nil {
		return ts.refused(t, err)
	}
	t.Target = peer.ID
	if t, err = ts.record(t, PhaseRequested); err != nil {
		return t, err
	}
	gitURL, err := gitclone.RemoteURL(movable.Repo, "")
	if err != nil {
		return ts.refused(t, err)
	}
	if t, err = ts.preflight(ctx, t, peer, movable, gitURL); err != nil {
		return t, err
	}
	cp, err := ts.checkpoint(ctx, operation, key, gitURL)
	if err != nil {
		return ts.refused(t, err)
	}
	t.Checkpoint = cp.Manifest.Digest()
	if t, err = ts.record(t, PhaseCheckpoint); err != nil {
		return t, err
	}
	var staged Staged
	if err := Remote(peer.Name, ts.Nodes.HTTP).Do(ctx, http.MethodPost, routePath(StageRoute), cp, &staged); err != nil {
		return ts.refused(t, fmt.Errorf("%s did not stage the checkpoint: %w", peer.short(), err))
	}
	if staged.Checkpoint != t.Checkpoint {
		return ts.refused(t, fmt.Errorf("%s staged checkpoint %s, not %s", peer.short(), staged.Checkpoint, t.Checkpoint))
	}
	return ts.record(t, PhaseStaged)
}

func routePath(route string) string {
	_, p, _ := strings.Cut(route, " ")
	return p
}

func (ts *Transfers) movable(ctx context.Context, key string) (host.Movable, error) {
	var m host.Movable
	answer, err := ts.call(ctx, "movable", "", host.CheckpointRequest{Key: key}, &m)
	if err == nil && answer.T != "done" {
		err = errors.New(answer.Error)
	}
	return m, err
}

func (ts *Transfers) destination(ctx context.Context, target string) (Member, error) {
	n := ts.Nodes
	i := slices.IndexFunc(n.Dir.Roster().Members, func(m RosterMember) bool { return m.ID == target || m.Name == target || m.short() == target })
	if i < 0 {
		return Member{}, fmt.Errorf("no node %s in the node list", target)
	}
	m := n.Dir.Roster().Members[i]
	if m.Self {
		return Member{}, errors.New("the factory already runs on this node")
	}
	inv, err := n.Tailscale.Inventory(ctx)
	if err != nil {
		return Member{}, err
	}
	if !n.stillTheSamePeer(inv, m.Member) {
		return Member{}, fmt.Errorf("tailscale status no longer shows %s as this owner's machine", m.Name)
	}
	return m.Member, nil
}

func (ts *Transfers) preflight(ctx context.Context, t Transfer, peer Member, m host.Movable, gitURL string) (Transfer, error) {
	t, err := ts.record(t, PhasePreflight)
	if err != nil {
		return t, err
	}
	config, _ := os.ReadFile(state.ProfilePath(m.Repo))
	request := PreflightRequest{Operation: t.Operation, Source: ts.Nodes.Self.ID, Incarnation: m.Incarnation, Factory: m.ID, GitURL: gitURL, Branch: m.Branch, Config: string(config), Protocol: Protocol}
	var report Preflight
	if err := Remote(peer.Name, ts.Nodes.HTTP).Do(ctx, http.MethodPost, routePath(PreflightRoute), request, &report); err != nil {
		return ts.refused(t, fmt.Errorf("%s did not answer its preflight: %w", peer.short(), err))
	}
	if report.refused() {
		return ts.refused(t, RefusedPreflight{Node: peer.short(), Report: report})
	}
	return t, nil
}

type RefusedPreflight struct {
	Node   string
	Report Preflight
}

func (e RefusedPreflight) Error() string {
	return e.Node + " cannot run this factory: " + e.Report.Refusal
}

func (ts *Transfers) checkpoint(ctx context.Context, operation, key, gitURL string) (Checkpoint, error) {
	m, err := ts.settled(ctx, operation, key)
	if err != nil {
		return Checkpoint{}, err
	}
	stack, err := PushStack(ctx, m, gitURL)
	if err != nil {
		return Checkpoint{}, err
	}
	manifest := Manifest{Operation: operation, Incarnation: m.Incarnation, Factory: m.ID, Slug: m.Slug, Source: ts.Nodes.Self.ID, GitURL: gitURL, Branch: m.Branch, Stack: stack, At: ts.Nodes.now()}
	cp, err := Capture(m, manifest, ts.limits())
	if err != nil {
		return Checkpoint{}, err
	}
	if err := cp.unchanged(m.Dir); err != nil {
		return Checkpoint{}, err
	}
	if err := cp.Validate(ts.limits()); err != nil {
		return Checkpoint{}, err
	}
	return cp, Retain(ts.StateDir, cp)
}

func (ts *Transfers) settled(ctx context.Context, operation, key string) (host.Movable, error) {
	for {
		var m host.Movable
		answer, err := ts.call(ctx, "checkpoint", "", host.CheckpointRequest{Key: key, Operation: operation}, &m)
		switch {
		case err != nil:
			return m, err
		case answer.T != "done":
			return m, errors.New(answer.Error)
		case m.Waiting == "":
			return m, nil
		}
		select {
		case <-ctx.Done():
			return m, fmt.Errorf("%s: %w", m.Waiting, ctx.Err())
		case <-time.After(ts.poll()):
		}
	}
}

func (cp Checkpoint) unchanged(dir string) error {
	captured, _ := cp.File(RootFactory + "/state.json")
	now, err := os.ReadFile(filepath.Join(dir, "state.json"))
	if err != nil || string(now) != string(captured) {
		return errors.New("state.json changed while the checkpoint was taken; try again once the supervisor is idle")
	}
	return nil
}

type Staged struct {
	Incarnation string `json:"incarnation"`
	Checkpoint  string `json:"checkpoint"`
	Files       int    `json:"files"`
}

func (ts *Transfers) Accept(ctx context.Context, cp Checkpoint) (Staged, error) {
	limits := ts.limits()
	m := cp.Manifest
	if err := cp.Validate(limits); err != nil {
		return Staged{}, err
	}
	if err := ts.verifiedSource(ctx, m.Source); err != nil {
		return Staged{}, err
	}
	if ts.Holds != nil && ts.Holds(m.Incarnation) {
		return Staged{}, fmt.Errorf("factory %s already runs on this node", m.Factory)
	}
	if err := ts.sameIdentity(m); err != nil {
		return Staged{}, err
	}
	if err := ts.room(); err != nil {
		return Staged{}, err
	}
	if err := ts.write(cp); err != nil {
		return Staged{}, err
	}
	digest := m.Digest()
	t := Transfer{Operation: m.Operation, Role: RoleTarget, Incarnation: m.Incarnation, Factory: m.Factory, Source: m.Source, Target: ts.Nodes.Self.ID, Checkpoint: digest}
	if _, err := ts.record(t, PhaseStaged); err != nil {
		return Staged{}, err
	}
	return Staged{Incarnation: m.Incarnation, Checkpoint: digest, Files: len(cp.Files)}, nil
}

func (ts *Transfers) verifiedSource(ctx context.Context, source string) error {
	n := ts.Nodes
	m, member := n.Dir.Member(source)
	if !member || source == n.Self.ID {
		return errors.New("the checkpoint comes from a node that is not in this node list")
	}
	inv, err := n.Tailscale.Inventory(ctx)
	if err != nil {
		return err
	}
	if !n.stillTheSamePeer(inv, m) {
		return fmt.Errorf("tailscale status no longer shows %s as this owner's machine", m.Name)
	}
	return nil
}

func (ts *Transfers) sameIdentity(m Manifest) error {
	data, err := os.ReadFile(filepath.Join(StagedDir(ts.StateDir, m.Incarnation), "manifest.json"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	var staged Manifest
	if err != nil || json.Unmarshal(data, &staged) != nil {
		return fmt.Errorf("this node cannot read its earlier staged checkpoint of %s", m.Incarnation)
	}
	if staged.Factory != m.Factory || staged.GitURL != m.GitURL {
		return fmt.Errorf("incarnation %s is staged here as %s from %s, not %s from %s", m.Incarnation, staged.Factory, staged.GitURL, m.Factory, m.GitURL)
	}
	return nil
}

func (ts *Transfers) write(cp Checkpoint) error {
	final := StagedDir(ts.StateDir, cp.Manifest.Incarnation)
	if err := os.MkdirAll(filepath.Dir(final), 0o700); err != nil {
		return err
	}
	partial, err := os.MkdirTemp(filepath.Dir(final), "."+cp.Manifest.Incarnation+"-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(partial)
	for _, f := range cp.Files {
		if err := writeNew(filepath.Join(partial, "files", filepath.FromSlash(f.Path)), f.Data); err != nil {
			return err
		}
	}
	manifest, _ := json.Marshal(cp.Manifest)
	if err := writeNew(filepath.Join(partial, "manifest.json"), manifest); err != nil {
		return err
	}
	if err := os.RemoveAll(final); err != nil {
		return err
	}
	return os.Rename(partial, final)
}

func writeNew(p string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func (ts *Transfers) routes(route, bounded func(string, func(http.ResponseWriter, *http.Request, string))) {
	route(PreflightRoute, nativeOnly(ts.preflightRoute))
	bounded(StageRoute, nativeOnly(ts.stageRoute))
}

func StageBody(limits Limits) int64 { return limits.Total*4/3 + maxBody + int64(limits.Files)*512 }

func (ts *Transfers) preflightRoute(w http.ResponseWriter, r *http.Request, _ string) {
	var body PreflightRequest
	if err := decode(r, &body); err != nil || uuid.Validate(body.Operation) != nil || uuid.Validate(body.Incarnation) != nil {
		fail(w, http.StatusBadRequest, "send {operation and incarnation as UUIDs, source, factory, git_url, branch, config, protocol}")
		return
	}
	if err := ts.verifiedSource(r.Context(), body.Source); err != nil {
		fail(w, http.StatusForbidden, err.Error())
		return
	}
	reply(w, http.StatusOK, ts.Preflight(r.Context(), body))
}

func (ts *Transfers) stageRoute(w http.ResponseWriter, r *http.Request, _ string) {
	var cp Checkpoint
	if err := decode(r, &cp); err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	staged, err := ts.Accept(r.Context(), cp)
	if err != nil {
		fail(w, http.StatusConflict, err.Error())
		return
	}
	reply(w, http.StatusOK, staged)
}
