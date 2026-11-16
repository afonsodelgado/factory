package phone

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"mime"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/afonsodelgado/factory/internal/host"
)

const (
	maxBody    = 64 << 10
	heartbeat  = 15 * time.Second
	pollEvery  = time.Second
	retryAfter = "2"
)

type Snapshot struct {
	Revision   int64               `json:"revision"`
	ServerTime time.Time           `json:"server_time"`
	Factories  []host.LocalFactory `json:"factories"`
}

type Server struct {
	Store    Store
	Owner    Owner
	Host     string
	Version  string
	Shell    http.Handler
	Review   http.Handler
	Push     *Pusher
	VAPIDKey string
	Now      func() time.Time

	grants    grants
	mu        sync.Mutex
	current   *Snapshot
	factories []byte
	streams   map[*stream]bool
}

type stream struct {
	device    string
	snapshots bool
	next      chan []byte
	closed    chan struct{}
	once      sync.Once
}

func (s *stream) close() { s.once.Do(func() { close(s.closed) }) }

func (s *stream) offer(event []byte) {
	for {
		select {
		case s.next <- event:
			return
		default:
		}
		select {
		case <-s.next:
		default:
		}
	}
}

func (s *Server) now() time.Time {
	if s.Now == nil {
		return time.Now()
	}
	return s.Now()
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/me", s.authed(s.me))
	mux.HandleFunc("GET /api/snapshot", s.authed(s.snapshot))
	mux.HandleFunc("GET /api/events", s.authed(s.events))
	mux.HandleFunc("GET /api/operations/{operation}", s.authed(s.operation))
	mux.HandleFunc("POST /api/factories/{key}/actions", s.authed(s.act))
	mux.HandleFunc("POST /api/factories/{key}/preview", s.authed(s.preview))
	mux.HandleFunc("POST /api/push/subscription", s.authed(s.subscribePush))
	mux.HandleFunc("DELETE /api/push/subscription", s.authed(s.unsubscribePush))
	mux.HandleFunc("GET /api/links/{key}", s.authed(s.link))
	mux.HandleFunc("POST /api/pair/status", s.pairStatus)
	mux.HandleFunc("POST /api/pair/exchange", s.pairExchange)
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) { fail(w, http.StatusNotFound, "no such route") })
	mux.HandleFunc("/r/", s.authed(s.openReview))
	mux.HandleFunc(grantPrefix+"{token}/{rest...}", s.sandboxed)
	mux.Handle("/", s.shell())
	return s.guard(mux)
}

func (s *Server) shell() http.Handler {
	if s.Shell != nil {
		return s.Shell
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "The phone app is not part of this build. Run make build, then factory phone start.", http.StatusServiceUnavailable)
	})
}

func (s *Server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != s.Host {
			fail(w, http.StatusMisdirectedRequest, "unknown host")
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") {
			w.Header().Set("Cache-Control", "no-store")
		}
		if mutates(r) {
			if problem, status := s.mutationProblem(r); problem != "" && !strings.HasPrefix(r.URL.Path, grantPrefix) {
				fail(w, status, problem)
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, maxBody)
		}
		next.ServeHTTP(w, r)
	})
}

func mutates(r *http.Request) bool {
	return r.Method != http.MethodGet && r.Method != http.MethodHead
}

func (s *Server) mutationProblem(r *http.Request) (string, int) {
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	origins := r.Header.Values("Origin")
	switch {
	case len(origins) != 1 || origins[0] != "https://"+s.Host:
		return "this request must come from the factory app's own origin", http.StatusForbidden
	case err != nil || media != "application/json":
		return "send JSON", http.StatusUnsupportedMediaType
	}
	return "", 0
}

func credential(r *http.Request) (string, bool) {
	cookies := r.CookiesNamed(CookieName)
	if len(cookies) != 1 || r.Header.Get("Authorization") != "" || r.URL.Query().Has("token") {
		return "", false
	}
	return cookies[0].Value, true
}

type authedHandler func(http.ResponseWriter, *http.Request, Device)

func (s *Server) authed(next authedHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		secret, ok := credential(r)
		device, paired := s.Store.Authenticate(secret)
		if !ok || !paired {
			http.SetCookie(w, &http.Cookie{Name: CookieName, Path: "/", MaxAge: -1, Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode})
			if opensPage(r.Method, r.URL.Path) {
				page(w, http.StatusUnauthorized, unpairedPage, DatabaseName)
				return
			}
			fail(w, http.StatusUnauthorized, "This phone is not paired.")
			return
		}
		next(w, r, device)
	}
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

type me struct {
	Device     Device    `json:"device"`
	Version    string    `json:"version"`
	ServerTime time.Time `json:"server_time"`
	Push       pushState `json:"push"`
}

type pushState struct {
	Key        string    `json:"vapid_public_key"`
	Subscribed bool      `json:"subscribed"`
	Endpoint   string    `json:"endpoint,omitempty"`
	LastSent   time.Time `json:"last_sent,omitzero"`
	Problem    string    `json:"problem,omitempty"`
}

func (s *Server) pushStateOf(device string) pushState {
	sub, subscribed := s.Store.SubscriptionOf(device)
	return pushState{Key: s.VAPIDKey, Subscribed: subscribed, Endpoint: sub.Endpoint, LastSent: sub.LastSent, Problem: sub.Problem}
}

func (s *Server) me(w http.ResponseWriter, r *http.Request, device Device) {
	reply(w, http.StatusOK, me{Device: device, Version: s.Version, ServerTime: s.now(), Push: s.pushStateOf(device.ID)})
}

type subscriptionBody struct {
	Endpoint       string `json:"endpoint"`
	ExpirationTime *int64 `json:"expirationTime"`
	Keys           struct {
		P256dh string `json:"p256dh"`
		Auth   string `json:"auth"`
	} `json:"keys"`
}

func (s *Server) subscribePush(w http.ResponseWriter, r *http.Request, device Device) {
	var body subscriptionBody
	if err := decode(r, &body); err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	sub := Subscription{Device: device.ID, Endpoint: body.Endpoint, P256dh: body.Keys.P256dh, Auth: body.Keys.Auth, Since: s.now()}
	if err := s.Store.Subscribe(sub); err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	reply(w, http.StatusOK, s.pushStateOf(device.ID))
}

func (s *Server) unsubscribePush(w http.ResponseWriter, r *http.Request, device Device) {
	if err := s.Store.Unsubscribe(device.ID); err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	reply(w, http.StatusOK, s.pushStateOf(device.ID))
}

func (s *Server) link(w http.ResponseWriter, r *http.Request, _ Device) {
	link, ok := s.Store.Link(r.PathValue("key"))
	if !ok {
		fail(w, http.StatusNotFound, "This notification's item is no longer known.")
		return
	}
	reply(w, http.StatusOK, link)
}

func (s *Server) snapshot(w http.ResponseWriter, r *http.Request, _ Device) {
	snap, ok := s.Latest()
	if !ok {
		unavailable(w)
		return
	}
	snap.ServerTime = s.now()
	reply(w, http.StatusOK, snap)
}

func (s *Server) Latest() (Snapshot, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.current == nil {
		return Snapshot{}, false
	}
	return *s.current, true
}

func (s *Server) Poll(ctx context.Context) {
	ticker := time.NewTicker(pollEvery)
	defer ticker.Stop()
	for {
		s.refresh(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Server) refresh(ctx context.Context) {
	var local host.Local
	reply, err := request(ctx, s.Owner, "local", "", nil, &local)
	if err != nil || reply.T != "done" {
		s.lose()
		return
	}
	if s.publish(local.Factories) && s.Push != nil {
		queued, err := s.Store.Reconcile(local.Factories, s.connected, s.now())
		if err == nil && queued {
			s.Push.Wake()
		}
	}
}

func (s *Server) publish(local []host.LocalFactory) bool {
	factories, _ := json.Marshal(local)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.current != nil && bytes.Equal(factories, s.factories) {
		return false
	}
	revision := s.now().UnixMilli()
	if s.current != nil && revision <= s.current.Revision {
		revision = s.current.Revision + 1
	}
	s.factories = factories
	s.current = &Snapshot{Revision: revision, ServerTime: s.now(), Factories: local}
	s.broadcast(event("snapshot", s.current))
	return true
}

func (s *Server) connected(device string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for st := range s.streams {
		if st.snapshots && st.device == device {
			return true
		}
	}
	return false
}

func (s *Server) lose() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.current == nil {
		return
	}
	s.current, s.factories = nil, nil
	s.broadcast(event("unavailable", map[string]any{"retry": true}))
}

func (s *Server) broadcast(e []byte) {
	for st := range s.streams {
		st.offer(e)
	}
}

func event(name string, value any) []byte {
	data, _ := json.Marshal(value)
	return fmt.Appendf(nil, "event: %s\ndata: %s\n\n", name, data)
}

func (s *Server) Revoke(device string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for st := range s.streams {
		if st.device == device {
			st.close()
		}
	}
}

func (s *Server) watch(device string) *stream {
	st := &stream{device: device, next: make(chan []byte, 1), closed: make(chan struct{})}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.streams == nil {
		s.streams = map[*stream]bool{}
	}
	s.streams[st] = true
	return st
}

func (s *Server) subscribe(device string) *stream {
	st := s.watch(device)
	s.mu.Lock()
	defer s.mu.Unlock()
	st.snapshots = true
	if s.current != nil {
		st.offer(event("snapshot", s.current))
	} else {
		st.offer(event("unavailable", map[string]any{"retry": true}))
	}
	return st
}

func (s *Server) unsubscribe(st *stream) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.streams, st)
}

func (s *Server) events(w http.ResponseWriter, r *http.Request, device Device) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		fail(w, http.StatusInternalServerError, "streaming is not supported")
		return
	}
	st := s.subscribe(device.ID)
	defer s.unsubscribe(st)
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	io.WriteString(w, "retry: 3000\n\n")
	flusher.Flush()
	beat := time.NewTicker(heartbeat)
	defer beat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-st.closed:
			return
		case <-beat.C:
			io.WriteString(w, ": ping\n\n")
		case e := <-st.next:
			if !s.Store.Paired(device.ID) {
				return
			}
			w.Write(e)
		}
		flusher.Flush()
	}
}

func (s *Server) operation(w http.ResponseWriter, r *http.Request, device Device) {
	op := r.PathValue("operation")
	if uuid.Validate(op) != nil {
		fail(w, http.StatusNotFound, "no such operation")
		return
	}
	var receipt host.Receipt
	answer, err := request(r.Context(), s.Owner, "receipt", "", host.ReceiptRequest{Device: device.ID, Operation: op}, &receipt)
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

type actionBody struct {
	Operation   string    `json:"operation"`
	Incarnation string    `json:"incarnation"`
	Action      string    `json:"action"`
	Item        string    `json:"item,omitempty"`
	Since       time.Time `json:"since,omitzero"`
	Answers     []string  `json:"answers,omitempty"`
	Text        string    `json:"text,omitempty"`
	Option      string    `json:"option,omitempty"`
	Fingerprint string    `json:"fingerprint,omitempty"`
}

func (s *Server) preview(w http.ResponseWriter, r *http.Request, device Device) {
	var body actionBody
	if err := decode(r, &body); err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	action := host.ActionRequest{Device: device.ID, Factory: r.PathValue("key"), Incarnation: body.Incarnation, Action: body.Action, Item: body.Item, Since: body.Since, Answers: body.Answers, Text: body.Text}
	var preview host.Preview
	answer, err := request(r.Context(), s.Owner, "preview", "", action, &preview)
	switch {
	case err != nil:
		unavailable(w)
	case answer.T != "done":
		fail(w, http.StatusConflict, answer.Error)
	default:
		reply(w, http.StatusOK, preview)
	}
}

const reusedOperation = "operation id already belongs to a different request"

func (s *Server) act(w http.ResponseWriter, r *http.Request, device Device) {
	var body actionBody
	if err := decode(r, &body); err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	if uuid.Validate(body.Operation) != nil {
		fail(w, http.StatusBadRequest, "operation must be a UUID")
		return
	}
	action := host.ActionRequest{
		Operation: body.Operation, Device: device.ID, Factory: r.PathValue("key"), Incarnation: body.Incarnation, Action: body.Action,
		Item: body.Item, Since: body.Since, Answers: body.Answers, Text: body.Text, Option: body.Option, Fingerprint: body.Fingerprint,
	}
	var receipt host.Receipt
	answer, err := request(r.Context(), s.Owner, "act", body.Operation, action, &receipt)
	switch {
	case err != nil:
		unavailable(w)
	case answer.T == "uncertain":
		reply(w, http.StatusConflict, map[string]any{"error": answer.Error, "operation": body.Operation, "status": host.StatusUnknown})
	case answer.T == "error" && answer.Error == reusedOperation:
		fail(w, http.StatusConflict, answer.Error)
	case answer.T == "error":
		fail(w, http.StatusBadRequest, answer.Error)
	case receipt.Status == host.StatusStale:
		reply(w, http.StatusConflict, receipt)
	default:
		reply(w, http.StatusOK, receipt)
	}
}

type pairBody struct {
	Code string `json:"code"`
	Name string `json:"name,omitempty"`
}

func (s *Server) pairStatus(w http.ResponseWriter, r *http.Request) {
	var body pairBody
	if err := decode(r, &body); err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	expires, err := s.Store.CodeExpiry(body.Code, s.now())
	if err != nil {
		pairFailure(w, err)
		return
	}
	reply(w, http.StatusOK, map[string]any{"expires_at": expires})
}

func (s *Server) pairExchange(w http.ResponseWriter, r *http.Request) {
	var body pairBody
	if err := decode(r, &body); err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	device, secret, err := s.Store.Exchange(body.Code, body.Name, s.now())
	if err != nil {
		pairFailure(w, err)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: CookieName, Value: secret, Path: "/", MaxAge: int(CredentialLife.Seconds()), Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode})
	reply(w, http.StatusOK, map[string]any{"device": device})
}

func pairFailure(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrCodeSpent):
		fail(w, http.StatusGone, err.Error())
	case errors.Is(err, ErrTooManyCodes):
		w.Header().Set("Retry-After", "300")
		fail(w, http.StatusTooManyRequests, err.Error())
	case strings.Contains(err.Error(), "device name"):
		fail(w, http.StatusBadRequest, err.Error())
	default:
		fail(w, http.StatusInternalServerError, err.Error())
	}
}

var helpPage = template.Must(template.New("help").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>factory</title>
<style>:root{color-scheme:dark}body{margin:0;padding:24px 16px;background:#16161e;color:#c0caf5;font:15px/1.5 ui-monospace,Menlo,monospace}a{color:#7aa2f7}</style>
</head><body><h1>Open factory over HTTPS</h1>
<p>The app works only at <a href="{{.}}">{{.}}</a>.</p>
<p>If that address does not load, run <code>factory doctor</code> on the machine.</p>
</body></html>
`))

func HelpHandler(origin string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" || r.Method != http.MethodGet {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		helpPage.Execute(w, origin)
	})
}
