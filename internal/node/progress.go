package node

import (
	"cmp"
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

	"github.com/afonsodelgado/factory/internal/host"
)

const (
	StateMoving            = "moving"
	StateCheckpointWaiting = "checkpoint_waiting"
	StatePreflightFailed   = "preflight_failed"
	StateRefused           = "refused"
	StateCancelled         = "cancelled"
	StateStaged            = "staged"
	StateReleased          = "released"
	StateRecoveryNeeded    = "recovery_needed"
	StateInterrupted       = "interrupted"
	StateActive            = "active"
	StateQueued            = "queued"
)

var ErrNoTransfer = errors.New("no such move on this node")

type MoveRequest struct {
	Operation string `json:"operation"`
	Factory   string `json:"factory"`
	Target    string `json:"target"`
}

type MoveAllRequest struct {
	Operation string `json:"operation"`
	Target    string `json:"target"`
}

type Progress struct {
	Transfer
	State    string   `json:"state"`
	Running  bool     `json:"running"`
	Recovery []string `json:"recovery,omitempty"`
}

type Batch struct {
	Schema     int         `json:"schema"`
	Operation  string      `json:"operation"`
	Target     string      `json:"target"`
	TargetName string      `json:"target_name"`
	Items      []BatchItem `json:"items"`
	Cancelled  bool        `json:"cancelled,omitempty"`
	At         time.Time   `json:"at"`
}

type BatchItem struct {
	Key       string `json:"key"`
	Factory   string `json:"factory"`
	Operation string `json:"operation,omitempty"`
	Refusal   string `json:"refusal,omitempty"`
}

type BatchProgress struct {
	Operation  string        `json:"operation"`
	Target     string        `json:"target"`
	TargetName string        `json:"target_name"`
	Running    bool          `json:"running"`
	Cancelled  bool          `json:"cancelled,omitempty"`
	Items      []ItemOutcome `json:"items"`
}

type ItemOutcome struct {
	Key       string    `json:"key"`
	Factory   string    `json:"factory"`
	Operation string    `json:"operation,omitempty"`
	State     string    `json:"state"`
	Reason    string    `json:"reason,omitempty"`
	Progress  *Progress `json:"progress,omitempty"`
}

func ProgressOf(t Transfer, running bool) Progress {
	p := Progress{Transfer: t, Running: running}
	switch {
	case t.Phase == PhaseActive:
		p.State = StateActive
	case t.Cancelled:
		p.State = StateCancelled
	case t.Refusal != "" && len(t.Checks) > 0:
		p.State = StatePreflightFailed
	case t.Refusal != "":
		p.State = StateRefused
	case t.Phase == PhaseReleased && running:
		p.State = StateReleased
	case t.Phase == PhaseReleased:
		p.State, p.Recovery = StateRecoveryNeeded, recoverySteps(t)
	case t.Role == RoleTarget && t.Phase == PhaseStaged && !t.Forced:
		p.State = StateStaged
	case !running && t.Role == RoleTarget:
		p.State, p.Recovery = StateInterrupted, []string{fmt.Sprintf("The takeover of %s stopped before %s claimed it. Run factory move %s --to %s --force again to finish it.", t.Factory, t.TargetName, t.Factory, t.TargetName)}
	case !running:
		p.State, p.Recovery = StateInterrupted, []string{fmt.Sprintf("The move stopped at %s before %s was released, so it still runs on %s. Run factory move %s --to %s again to finish it.", t.Phase, t.Factory, t.SourceName, t.Factory, t.TargetName)}
	case t.Waiting != "":
		p.State = StateCheckpointWaiting
	case t.Phase == PhaseStaged:
		p.State = StateStaged
	default:
		p.State = StateMoving
	}
	return p
}

func recoverySteps(t Transfer) []string {
	if t.Role == RoleTarget {
		return []string{
			fmt.Sprintf("%s claimed %s but did not start it: %s", t.TargetName, t.Factory, cmp.Or(t.Failure, "the node stopped before it finished")),
			fmt.Sprintf("Run factory move %s --to %s --force on %s to finish starting it from the same checkpoint.", t.Factory, t.TargetName, t.TargetName),
		}
	}
	return []string{
		fmt.Sprintf("%s is stopped on %s and %s has not confirmed it runs it: %s", t.Factory, t.SourceName, t.TargetName, cmp.Or(t.Failure, "the node stopped before it heard back")),
		fmt.Sprintf("When %s answers, run factory move %s --to %s again; it finishes this move and never runs %s twice.", t.TargetName, t.Factory, t.TargetName, t.Factory),
		fmt.Sprintf("To run it on %s again instead, run factory move %s --to %s --force; it restarts from the checkpoint, and anything %s did after it may diverge.", t.SourceName, t.Factory, t.SourceName, t.TargetName),
	}
}

func Journals(stateDir string) []Transfer {
	paths, _ := filepath.Glob(filepath.Join(stateDir, "transfers", "*.json"))
	var all []Transfer
	for _, p := range paths {
		if t, found, err := LoadTransfer(stateDir, strings.TrimSuffix(filepath.Base(p), ".json")); found && err == nil {
			all = append(all, t)
		}
	}
	slices.SortFunc(all, func(a, b Transfer) int { return a.At.Compare(b.At) })
	return all
}

func (ts *Transfers) base() context.Context {
	if ts.Ctx == nil {
		return context.Background()
	}
	return ts.Ctx
}

func (ts *Transfers) track(op string, parent context.Context) (context.Context, func(), bool) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	if ts.running == nil {
		ts.running = map[string]context.CancelFunc{}
	}
	if _, busy := ts.running[op]; busy {
		return nil, nil, false
	}
	ctx, cancel := context.WithCancel(parent)
	ts.running[op] = cancel
	return ctx, func() {
		ts.mu.Lock()
		delete(ts.running, op)
		ts.mu.Unlock()
		cancel()
	}, true
}

func (ts *Transfers) drive(op string, work func(context.Context)) {
	ctx, done, fresh := ts.track(op, ts.base())
	if !fresh {
		return
	}
	go func() {
		defer done()
		work(ctx)
	}()
}

func (ts *Transfers) Running(op string) bool {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	_, running := ts.running[op]
	return running
}

func (ts *Transfers) Progress(op string) (Progress, error) {
	t, found, err := LoadTransfer(ts.StateDir, op)
	running := ts.Running(op)
	switch {
	case err != nil:
		return Progress{}, err
	case !found && !running:
		return Progress{}, ErrNoTransfer
	case !found:
		return Progress{Transfer: Transfer{Operation: op}, State: StateMoving, Running: true}, nil
	}
	return ProgressOf(t, running), nil
}

func (ts *Transfers) List() []Progress {
	var all []Progress
	for _, t := range Journals(ts.StateDir) {
		all = append(all, ProgressOf(t, ts.Running(t.Operation)))
	}
	return all
}

func (ts *Transfers) Start(ctx context.Context, r MoveRequest) (Progress, error) {
	t, found, err := LoadTransfer(ts.StateDir, r.Operation)
	switch {
	case err != nil:
		return Progress{}, err
	case found && t.Role != RoleSource:
		return Progress{}, fmt.Errorf("operation %s is a move onto this node, not away from it", r.Operation)
	case found:
		r.Factory, r.Target = t.Key, t.Target
	}
	if earlier, ok := ts.unfinished(r.Factory); !found && ok {
		sameTarget := ts.names(earlier.Target, r.Target)
		if earlier.reached(PhaseReleased) && !sameTarget {
			return ProgressOf(earlier, false), fmt.Errorf("%s is stopped here mid-move to %s (operation %s); finish it with factory move %s --to %s, or run it here again with --to %s --force", earlier.Factory, earlier.TargetName, earlier.Operation, earlier.Factory, earlier.TargetName, ts.Nodes.Self.short())
		}
		if sameTarget {
			r.Operation, r.Factory, r.Target = earlier.Operation, earlier.Key, earlier.Target
		} else {
			ts.refused(earlier, fmt.Errorf("replaced by move %s to %s", r.Operation, r.Target))
		}
	}
	if !found {
		r.Factory = ts.resolve(ctx, r.Factory)
	}
	ts.drive(r.Operation, func(ctx context.Context) { ts.Move(ctx, r.Operation, r.Factory, r.Target) })
	return ts.Progress(r.Operation)
}

func (ts *Transfers) unfinished(factoryID string) (Transfer, bool) {
	var newest Transfer
	found := false
	for _, t := range Journals(ts.StateDir) {
		named := t.Key == factoryID || t.Factory == factoryID
		open := t.Role == RoleSource && t.Refusal == "" && t.Phase != PhaseActive && !ts.Running(t.Operation)
		if named && open {
			newest, found = t, true
		}
	}
	return newest, found
}

func (ts *Transfers) names(id, target string) bool {
	m, member := ts.Nodes.Dir.Member(id)
	return target == id || (member && (target == m.Name || target == m.short()))
}

func (ts *Transfers) resolve(ctx context.Context, factoryID string) string {
	factories, _ := ts.local(ctx)
	if i := slices.IndexFunc(factories, func(f host.LocalFactory) bool { return f.Key == factoryID || f.ID == factoryID }); i >= 0 {
		return factories[i].Key
	}
	return factoryID
}

func (ts *Transfers) Cancel(op string) (Progress, error) {
	return ts.cancel(op, false)
}

func (ts *Transfers) cancel(op string, queued bool) (Progress, error) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	t, found, err := LoadTransfer(ts.StateDir, op)
	stop, running := ts.running[op]
	switch {
	case err != nil:
		return Progress{}, err
	case !found && !running && !queued:
		return Progress{}, ErrNoTransfer
	case found && t.Role != RoleSource:
		return ProgressOf(t, running), errors.New("only the node a move starts from can cancel it")
	case found && t.reached(PhaseReleased):
		return ProgressOf(t, running), fmt.Errorf("%s is already released to %s, so the move can only finish or be recovered, not cancelled", t.Factory, t.TargetName)
	case found && t.Refusal != "":
		return ProgressOf(t, running), nil
	case !found:
		t = Transfer{Operation: op, Role: RoleSource, Source: ts.Nodes.Self.ID, SourceName: ts.Nodes.Self.short()}
	}
	t.Cancelled, t.Refusal, t.Schema, t.At = true, errCancelled.Error(), TransferSchema, ts.Nodes.now()
	if err := host.SavePrivate(TransferPath(ts.StateDir, op), t); err != nil {
		return Progress{}, err
	}
	if running {
		stop()
	}
	return ProgressOf(t, false), nil
}

func BatchPath(stateDir, operation string) string {
	return filepath.Join(stateDir, "transfers", "batches", operation+".json")
}

func loadBatch(stateDir, operation string) (Batch, bool, error) {
	var b Batch
	data, err := os.ReadFile(BatchPath(stateDir, operation))
	if errors.Is(err, fs.ErrNotExist) {
		return b, false, nil
	}
	if err == nil {
		err = json.Unmarshal(data, &b)
	}
	return b, err == nil, err
}

func (ts *Transfers) saveBatch(b Batch) error {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	b.Schema, b.At = TransferSchema, ts.Nodes.now()
	return host.SavePrivate(BatchPath(ts.StateDir, b.Operation), b)
}

func (ts *Transfers) StartAll(ctx context.Context, r MoveAllRequest) (BatchProgress, error) {
	b, found, err := loadBatch(ts.StateDir, r.Operation)
	if err != nil {
		return BatchProgress{}, err
	}
	if !found {
		if b, err = ts.snapshotMovable(ctx, r); err != nil {
			return BatchProgress{}, err
		}
	}
	ts.drive(b.Operation, func(ctx context.Context) { ts.moveAll(ctx, b) })
	return ts.BatchProgress(b.Operation)
}

func (ts *Transfers) snapshotMovable(ctx context.Context, r MoveAllRequest) (Batch, error) {
	peer, err := ts.destination(ctx, r.Target)
	if err != nil {
		return Batch{}, err
	}
	factories, err := ts.local(ctx)
	if err != nil {
		return Batch{}, err
	}
	b := Batch{Operation: r.Operation, Target: peer.ID, TargetName: peer.short(), Items: []BatchItem{}}
	for _, f := range factories {
		item := BatchItem{Key: f.Key, Factory: f.ID, Operation: uuid.NewString()}
		if _, err := ts.movable(ctx, f.Key); err != nil {
			item.Operation, item.Refusal = "", err.Error()
		}
		b.Items = append(b.Items, item)
	}
	return b, ts.saveBatch(b)
}

func (ts *Transfers) moveAll(ctx context.Context, b Batch) {
	for _, item := range b.Items {
		current, _, _ := loadBatch(ts.StateDir, b.Operation)
		if item.Refusal != "" || current.Cancelled {
			continue
		}
		itemCtx, done, fresh := ts.track(item.Operation, ctx)
		if !fresh {
			continue
		}
		ts.Move(itemCtx, item.Operation, item.Key, b.Target)
		done()
		if ctx.Err() != nil {
			return
		}
	}
}

func (ts *Transfers) BatchProgress(op string) (BatchProgress, error) {
	b, found, err := loadBatch(ts.StateDir, op)
	switch {
	case err != nil:
		return BatchProgress{}, err
	case !found:
		return BatchProgress{}, ErrNoTransfer
	}
	p := BatchProgress{Operation: op, Target: b.Target, TargetName: b.TargetName, Running: ts.Running(op), Cancelled: b.Cancelled, Items: []ItemOutcome{}}
	for _, item := range b.Items {
		p.Items = append(p.Items, ts.outcome(item, b.Cancelled))
	}
	return p, nil
}

func (ts *Transfers) outcome(item BatchItem, cancelled bool) ItemOutcome {
	o := ItemOutcome{Key: item.Key, Factory: item.Factory, Operation: item.Operation}
	if item.Refusal != "" {
		o.State, o.Reason = StateRefused, item.Refusal
		return o
	}
	progress, err := ts.Progress(item.Operation)
	switch {
	case err != nil && cancelled:
		o.State = StateCancelled
	case err != nil:
		o.State = StateQueued
	default:
		o.State, o.Reason, o.Progress = progress.State, cmp.Or(progress.Refusal, progress.Failure), &progress
	}
	return o
}

func (ts *Transfers) CancelAll(op string) (BatchProgress, error) {
	ts.mu.Lock()
	b, found, err := loadBatch(ts.StateDir, op)
	if err == nil && found {
		b.Cancelled, b.At = true, ts.Nodes.now()
		err = host.SavePrivate(BatchPath(ts.StateDir, op), b)
	}
	ts.mu.Unlock()
	switch {
	case err != nil:
		return BatchProgress{}, err
	case !found:
		return BatchProgress{}, ErrNoTransfer
	}
	for _, item := range b.Items {
		if item.Operation != "" {
			ts.cancel(item.Operation, true)
		}
	}
	return ts.BatchProgress(op)
}

func (ts *Transfers) Preview(factoryID string) (Unconfirmed, error) {
	cp, _, ok := ts.retained(factoryID)
	if !ok {
		return Unconfirmed{}, fmt.Errorf("this node has no checkpoint of %s, so it cannot take it over; a forced takeover restarts a factory only from a checkpoint a move left on this node", factoryID)
	}
	return ts.unconfirmed(cp.Manifest), nil
}

func (ts *Transfers) routes(route, bounded func(string, func(http.ResponseWriter, *http.Request, string))) {
	home := ts.Nodes.homeOrNative
	route(PreflightRoute, nativeOnly(ts.preflightRoute))
	bounded(StageRoute, nativeOnly(ts.stageRoute))
	route(ActivateRoute, nativeOnly(ts.activateRoute))
	route(OperationRoute, nativeOnly(ts.operationRoute))
	route(TakeoverRoute, nativeOnly(ts.takeoverHere))
	route("GET /api/transfers", func(w http.ResponseWriter, r *http.Request, _ string) { reply(w, http.StatusOK, ts.List()) })
	route("GET /api/transfers/{operation}", ts.progressRoute)
	route("POST /api/transfers", home(ts.startRoute))
	route("POST /api/transfers/{operation}/cancel", home(ts.cancelRoute))
	route("POST /api/transfers/all", home(ts.startAllRoute))
	route("GET /api/transfers/all/{operation}", ts.batchRoute)
	route("POST /api/transfers/all/{operation}/cancel", home(ts.cancelAllRoute))
	route("POST /api/transfers/takeover", home(ts.takeoverRoute))
}

func (ts *Transfers) answer(w http.ResponseWriter, status int, value any, err error) {
	switch {
	case errors.Is(err, ErrNoTransfer):
		fail(w, http.StatusNotFound, err.Error())
	case err != nil:
		reply(w, http.StatusConflict, map[string]any{"error": err.Error(), "progress": value})
	default:
		reply(w, status, value)
	}
}

func (ts *Transfers) progressRoute(w http.ResponseWriter, r *http.Request, _ string) {
	p, err := ts.Progress(r.PathValue("operation"))
	ts.answer(w, http.StatusOK, p, err)
}

func (ts *Transfers) startRoute(w http.ResponseWriter, r *http.Request, _ string) {
	var body MoveRequest
	if err := decode(r, &body); err != nil || uuid.Validate(body.Operation) != nil || body.Factory == "" || body.Target == "" {
		fail(w, http.StatusBadRequest, "send {operation as a UUID, factory, target}")
		return
	}
	p, err := ts.Start(r.Context(), body)
	ts.answer(w, http.StatusAccepted, p, err)
}

func (ts *Transfers) cancelRoute(w http.ResponseWriter, r *http.Request, _ string) {
	p, err := ts.Cancel(r.PathValue("operation"))
	ts.answer(w, http.StatusOK, p, err)
}

func (ts *Transfers) startAllRoute(w http.ResponseWriter, r *http.Request, _ string) {
	var body MoveAllRequest
	if err := decode(r, &body); err != nil || uuid.Validate(body.Operation) != nil || body.Target == "" {
		fail(w, http.StatusBadRequest, "send {operation as a UUID, target}")
		return
	}
	p, err := ts.StartAll(r.Context(), body)
	ts.answer(w, http.StatusAccepted, p, err)
}

func (ts *Transfers) batchRoute(w http.ResponseWriter, r *http.Request, _ string) {
	p, err := ts.BatchProgress(r.PathValue("operation"))
	ts.answer(w, http.StatusOK, p, err)
}

func (ts *Transfers) cancelAllRoute(w http.ResponseWriter, r *http.Request, _ string) {
	p, err := ts.CancelAll(r.PathValue("operation"))
	ts.answer(w, http.StatusOK, p, err)
}

func (ts *Transfers) takeoverRoute(w http.ResponseWriter, r *http.Request, _ string) {
	var body TakeoverRequest
	if err := decode(r, &body); err != nil || uuid.Validate(body.Operation) != nil || body.Factory == "" {
		fail(w, http.StatusBadRequest, "send {operation as a UUID, factory, target, confirm}")
		return
	}
	if body.Target == "" || ts.names(ts.Nodes.Self.ID, body.Target) {
		ts.takeover(w, r.Context(), body)
		return
	}
	peer, err := ts.destination(r.Context(), body.Target)
	if err != nil {
		fail(w, http.StatusConflict, err.Error())
		return
	}
	var answer json.RawMessage
	err = Remote(peer.Name, ts.Nodes.HTTP).Do(r.Context(), http.MethodPost, routePath(TakeoverRoute), body, &answer)
	var refusal StatusError
	switch {
	case errors.As(err, &refusal):
		reply(w, refusal.Status, answer)
	case err != nil:
		fail(w, http.StatusBadGateway, fmt.Sprintf("%s did not answer: %v", peer.short(), err))
	default:
		reply(w, http.StatusAccepted, answer)
	}
}

func (ts *Transfers) takeoverHere(w http.ResponseWriter, r *http.Request, _ string) {
	var body TakeoverRequest
	if err := decode(r, &body); err != nil || uuid.Validate(body.Operation) != nil || body.Factory == "" {
		fail(w, http.StatusBadRequest, "send {operation as a UUID, factory, confirm}")
		return
	}
	ts.takeover(w, r.Context(), body)
}

func (ts *Transfers) takeover(w http.ResponseWriter, _ context.Context, body TakeoverRequest) {
	if _, found, _ := LoadTransfer(ts.StateDir, body.Operation); !found && !body.Confirm {
		warning, err := ts.Preview(body.Factory)
		if err != nil {
			fail(w, http.StatusConflict, err.Error())
			return
		}
		reply(w, http.StatusPreconditionRequired, map[string]any{"error": warning.Warning, "warning": warning})
		return
	}
	ts.drive(body.Operation, func(ctx context.Context) { ts.Takeover(ctx, body) })
	p, err := ts.Progress(body.Operation)
	ts.answer(w, http.StatusAccepted, p, err)
}
