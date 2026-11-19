package phone

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/afonsodelgado/factory/internal/host"
)

const (
	maxBody    = 64 << 10
	pollEvery  = time.Second
	testWait   = 8 * time.Second
	retryAfter = "2"
)

var ErrUnavailable = errors.New("the factory node is restarting; try again in a moment")

type Server struct {
	Store     Store
	Factories func() (revision int64, factories []host.LocalFactory, ok bool)
	Connected func(installation string) bool
	Shell     http.Handler
	Review    http.Handler
	Push      *Pusher
	Node      string
	Changed   func()
	Now       func() time.Time
}

func (s *Server) now() time.Time {
	if s.Now == nil {
		return time.Now()
	}
	return s.Now()
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/push/subscription", s.pushState)
	mux.HandleFunc("POST /api/push/subscription", s.subscribePush)
	mux.HandleFunc("DELETE /api/push/subscription", s.unsubscribePush)
	mux.HandleFunc("POST /api/push/test", s.testPush)
	mux.HandleFunc("GET /api/links/{key}", s.link)
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) { fail(w, http.StatusNotFound, "no such route") })
	current := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { serveReview(s.Review, w, r) })
	(&Reviews{Review: current, Now: s.Now}).Register(mux)
	mux.Handle("/", s.shell())
	return mux
}

func (s *Server) shell() http.Handler {
	if s.Shell != nil {
		return s.Shell
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "The phone app is not part of this build. Run make build, then restart factory node run.", http.StatusServiceUnavailable)
	})
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

func decode(w http.ResponseWriter, r *http.Request, into any) error {
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody))
	d.DisallowUnknownFields()
	if err := d.Decode(into); err != nil {
		return fmt.Errorf("the request body is not the expected JSON: %w", err)
	}
	if d.More() {
		return errors.New("the request body holds more than one JSON value")
	}
	return nil
}

type PushState struct {
	Key          string    `json:"vapid_public_key"`
	Fingerprint  string    `json:"fingerprint,omitempty"`
	Health       string    `json:"health"`
	HealthDetail string    `json:"health_detail,omitempty"`
	Subscribed   bool      `json:"subscribed"`
	Endpoint     string    `json:"endpoint,omitempty"`
	LastSent     time.Time `json:"last_sent,omitzero"`
	Problem      string    `json:"problem,omitempty"`
}

const (
	HealthReady    = "ready"
	HealthConflict = "conflict"
	HealthNoKey    = "no key"
)

func (k KeyState) Health() (string, string) {
	switch {
	case k.Public == "":
		return HealthNoKey, "This node has no push key yet."
	case k.Conflict != "":
		return HealthConflict, "This node kept its own push key " + k.Fingerprint + " when it joined, because a phone was subscribed to it, so it cannot notify a phone subscribed with the shared key " + k.Conflict + "."
	}
	return HealthReady, ""
}

func (s *Server) stateOf(sub Subscription, subscribed bool) PushState {
	keys, _ := s.Store.KeyState()
	health, detail := keys.Health()
	return PushState{Key: keys.Public, Fingerprint: keys.Fingerprint, Health: health, HealthDetail: detail, Subscribed: subscribed, Endpoint: sub.Endpoint, LastSent: sub.LastSent, Problem: sub.Problem}
}

func (s *Server) changed() {
	if s.Changed != nil {
		s.Changed()
	}
}

func (s *Server) pushState(w http.ResponseWriter, r *http.Request) {
	reply(w, http.StatusOK, s.stateOf(s.Store.SubscriptionOf(r.URL.Query().Get("installation"))))
}

type subscriptionBody struct {
	Endpoint       string `json:"endpoint"`
	ExpirationTime *int64 `json:"expirationTime"`
	Keys           struct {
		P256dh string `json:"p256dh"`
		Auth   string `json:"auth"`
	} `json:"keys"`
	Installation string `json:"installation"`
	Replaces     string `json:"replaces,omitempty"`
}

func (s *Server) subscribePush(w http.ResponseWriter, r *http.Request) {
	var body subscriptionBody
	if err := decode(w, r, &body); err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	if s.Push.halted() {
		fail(w, http.StatusConflict, ErrRemoved.Error())
		return
	}
	sub, err := s.Store.Subscribe(Subscription{Endpoint: body.Endpoint, P256dh: body.Keys.P256dh, Auth: body.Keys.Auth, Installation: body.Installation, Since: s.now()}, body.Replaces)
	if err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	s.changed()
	reply(w, http.StatusOK, s.stateOf(sub, true))
}

func (s *Server) unsubscribePush(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Endpoint string `json:"endpoint"`
	}
	if err := decode(w, r, &body); err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.Store.Unsubscribe(body.Endpoint, s.now()); err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.changed()
	reply(w, http.StatusOK, s.stateOf(Subscription{}, false))
}

func (s *Server) testPush(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), testWait)
	defer cancel()
	results, err := s.Push.Test(ctx)
	if err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	reply(w, http.StatusOK, results)
}

func (s *Server) link(w http.ResponseWriter, r *http.Request) {
	link, ok := s.Store.Link(r.PathValue("key"))
	if !ok {
		fail(w, http.StatusNotFound, "This notification's item is no longer known.")
		return
	}
	reply(w, http.StatusOK, link)
}

func (s *Server) Poll(ctx context.Context) {
	ticker := time.NewTicker(pollEvery)
	defer ticker.Stop()
	var seen int64
	for {
		if revision, factories, ok := s.Factories(); ok && revision != seen {
			seen = revision
			s.reconcile(factories)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Server) reconcile(factories []host.LocalFactory) {
	if s.Push.halted() {
		return
	}
	queued, err := s.Store.Reconcile(s.Node, factories, s.Connected, s.now())
	if err == nil && queued {
		s.Push.Wake()
	}
}
