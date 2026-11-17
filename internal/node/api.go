package node

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/afonsodelgado/factory/internal/host"
)

const (
	Protocol    = 1
	MinProtocol = 1
	maxBody     = 64 << 10
	commandWait = time.Minute
	heartbeat   = 15 * time.Second
	pollEvery   = time.Second
	runtimeWait = 10 * time.Second
	retryAfter  = "2"
)

var ErrUnavailable = errors.New("the factory node is not answering; check that factory node run is running")

type Principal func(*http.Request) (string, bool)

type Commands struct{ Runtime Caller }

type API struct {
	Commands
	Version string
	Node    string
	Boot    string
	Now     func() time.Time

	mu        sync.Mutex
	current   *Snapshot
	factories []byte
	streams   map[*stream]bool
}

func (a *API) now() time.Time {
	if a.Now == nil {
		return time.Now()
	}
	return a.Now()
}

func (a *API) With(app http.Handler, principal Principal, kinds ...string) http.Handler {
	mux := http.NewServeMux()
	a.Register(mux, principal, kinds...)
	mux.Handle("/", app)
	return mux
}

var NetworkKinds = []string{"create", "removal", "delete", "archive", "unarchive", "wake", "pause", "wake-all"}

func (a *API) Register(mux *http.ServeMux, principal Principal, kinds ...string) {
	route := func(pattern string, serve func(http.ResponseWriter, *http.Request, string)) {
		mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			caller, ok := principal(r)
			if !ok {
				fail(w, http.StatusUnauthorized, "this request has no owner identity")
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, maxBody)
			serve(w, r, caller)
		})
	}
	route("GET /api/me", a.me)
	route("GET /api/snapshot", a.snapshot)
	route("GET /api/events", a.events)
	route("GET /api/operations/{operation}", a.Operation)
	route("POST /api/factories/{key}/actions", a.Act)
	route("POST /api/factories/{key}/preview", a.Preview)
	mux.HandleFunc("POST /api/commands/{kind}", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := principal(r); !ok {
			fail(w, http.StatusUnauthorized, "this request has no owner identity")
			return
		}
		if len(kinds) > 0 && !slices.Contains(kinds, r.PathValue("kind")) {
			fail(w, http.StatusNotFound, "no such command on this listener")
			return
		}
		a.Command(w, r)
	})
}

func (a *API) me(w http.ResponseWriter, r *http.Request, caller string) {
	reply(w, http.StatusOK, Me{Caller: caller, Node: a.Node, Version: a.Version, ServerTime: a.now()})
}

func (a *API) snapshot(w http.ResponseWriter, r *http.Request, _ string) {
	snap, ok := a.Latest()
	if !ok {
		unavailable(w)
		return
	}
	snap.ServerTime = a.now()
	reply(w, http.StatusOK, snap)
}

func (a *API) Latest() (Snapshot, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.current == nil {
		return Snapshot{}, false
	}
	return *a.current, true
}

func (a *API) Poll(ctx context.Context) {
	ticker := time.NewTicker(pollEvery)
	defer ticker.Stop()
	for {
		a.refresh(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (a *API) refresh(ctx context.Context) {
	var local host.Local
	answer, err := a.call(ctx, "local", "", nil, &local)
	if err != nil || answer.T != "done" {
		a.lose()
		return
	}
	a.publish(local.Factories)
}

func (a *API) publish(local []host.LocalFactory) {
	factories, _ := json.Marshal(local)
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.current != nil && bytes.Equal(factories, a.factories) {
		return
	}
	revision := a.now().UnixMilli()
	if a.current != nil && revision <= a.current.Revision {
		revision = a.current.Revision + 1
	}
	a.factories = factories
	a.current = &Snapshot{Node: a.Node, Version: a.Version, Protocol: Protocol, MinProtocol: MinProtocol, Boot: a.Boot, Revision: revision, ServerTime: a.now(), Factories: local}
	a.broadcast(event("snapshot", a.current))
}

func (a *API) Factories() (int64, []host.LocalFactory, bool) {
	snap, ok := a.Latest()
	return snap.Revision, snap.Factories, ok
}

func (a *API) Connected(installation string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	for s := range a.streams {
		if installation != "" && s.installation == installation {
			return true
		}
	}
	return false
}

func (a *API) lose() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.current == nil {
		return
	}
	a.current, a.factories = nil, nil
	a.broadcast(event("unavailable", map[string]any{"retry": true}))
}

type stream struct {
	installation string
	next         chan []byte
}

func (s *stream) offer(e []byte) {
	for {
		select {
		case s.next <- e:
			return
		default:
		}
		select {
		case <-s.next:
		default:
		}
	}
}

func (a *API) broadcast(e []byte) {
	for s := range a.streams {
		s.offer(e)
	}
}

func event(name string, value any) []byte {
	data, _ := json.Marshal(value)
	return fmt.Appendf(nil, "event: %s\ndata: %s\n\n", name, data)
}

func (a *API) subscribe(installation string) *stream {
	s := &stream{installation: installation, next: make(chan []byte, 1)}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.streams == nil {
		a.streams = map[*stream]bool{}
	}
	a.streams[s] = true
	if a.current != nil {
		s.offer(event("snapshot", a.current))
	} else {
		s.offer(event("unavailable", map[string]any{"retry": true}))
	}
	return s
}

func (a *API) unsubscribe(s *stream) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.streams, s)
}

func (a *API) events(w http.ResponseWriter, r *http.Request, _ string) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		fail(w, http.StatusInternalServerError, "streaming is not supported")
		return
	}
	s := a.subscribe(r.URL.Query().Get("installation"))
	defer a.unsubscribe(s)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	io.WriteString(w, "retry: 3000\n\n")
	flusher.Flush()
	beat := time.NewTicker(heartbeat)
	defer beat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-beat.C:
			io.WriteString(w, ": ping\n\n")
		case e := <-s.next:
			w.Write(e)
		}
		flusher.Flush()
	}
}

func (c Commands) Operation(w http.ResponseWriter, r *http.Request, _ string) {
	op := r.PathValue("operation")
	if uuid.Validate(op) != nil {
		fail(w, http.StatusNotFound, "no such operation")
		return
	}
	var receipt host.Receipt
	answer, err := c.call(r.Context(), "receipt", "", host.ReceiptRequest{Operation: op}, &receipt)
	switch {
	case err != nil:
		unavailable(w)
	case answer.T == "error" && answer.Error == host.ErrNoOperation.Error():
		fail(w, http.StatusNotFound, answer.Error)
	case answer.T != "done":
		fail(w, http.StatusInternalServerError, answer.Error)
	default:
		reply(w, http.StatusOK, receipt)
	}
}

func (c Commands) Preview(w http.ResponseWriter, r *http.Request, caller string) {
	var body ActionBody
	if err := decode(r, &body); err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	var preview host.Preview
	answer, err := c.call(r.Context(), "preview", "", body.Request(caller, r.PathValue("key")), &preview)
	switch {
	case err != nil:
		unavailable(w)
	case answer.T != "done":
		fail(w, http.StatusConflict, answer.Error)
	default:
		reply(w, http.StatusOK, preview)
	}
}

func (c Commands) Act(w http.ResponseWriter, r *http.Request, caller string) {
	var body ActionBody
	if err := decode(r, &body); err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	if uuid.Validate(body.Operation) != nil {
		fail(w, http.StatusBadRequest, "operation must be a UUID")
		return
	}
	var receipt host.Receipt
	answer, err := c.call(r.Context(), "act", body.Operation, body.Request(caller, r.PathValue("key")), &receipt)
	switch {
	case err != nil:
		unavailable(w)
	case answer.T == "uncertain":
		reply(w, http.StatusConflict, map[string]any{"error": answer.Error, "operation": body.Operation, "status": host.StatusUnknown})
	case answer.T == "error" && answer.Error == host.ReusedOperation:
		fail(w, http.StatusConflict, answer.Error)
	case answer.T == "error":
		fail(w, http.StatusBadRequest, answer.Error)
	case receipt.Status == host.StatusStale:
		reply(w, http.StatusConflict, receipt)
	default:
		reply(w, http.StatusOK, receipt)
	}
}

func (c Commands) Command(w http.ResponseWriter, r *http.Request) {
	op := r.Header.Get(OperationHeader)
	if op != "" && uuid.Validate(op) != nil {
		fail(w, http.StatusBadRequest, OperationHeader+" must be a UUID")
		return
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, host.MaxMessage))
	if err != nil || !json.Valid(data) {
		fail(w, http.StatusBadRequest, "the request body is not JSON")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), commandWait)
	defer cancel()
	answer, err := c.Runtime.Call(ctx, host.Message{T: r.PathValue("kind"), Op: op, Data: data})
	switch {
	case err != nil:
		unavailable(w)
	case answer.T == "done":
		reply(w, http.StatusOK, answer)
	case answer.T == "uncertain":
		reply(w, http.StatusConflict, answer)
	default:
		reply(w, http.StatusUnprocessableEntity, answer)
	}
}

func (c Commands) call(ctx context.Context, kind, op string, payload, into any) (host.Message, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return host.Message{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, runtimeWait)
	defer cancel()
	answer, err := c.Runtime.Call(ctx, host.Message{T: kind, Op: op, Data: data})
	if err == nil && answer.T == "done" && into != nil {
		err = json.Unmarshal(answer.Data, into)
	}
	return answer, err
}

func decode(r *http.Request, into any) error {
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(into); err != nil {
		return fmt.Errorf("the request body is not the expected JSON: %w", err)
	}
	if d.More() {
		return errors.New("the request body holds more than one JSON value")
	}
	return nil
}

func fail(w http.ResponseWriter, status int, message string) {
	reply(w, status, map[string]any{"error": message})
}

func unavailable(w http.ResponseWriter) {
	w.Header().Set("Retry-After", retryAfter)
	reply(w, http.StatusServiceUnavailable, map[string]any{"error": ErrUnavailable.Error(), "retry": true})
}

func reply(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(value)
}
