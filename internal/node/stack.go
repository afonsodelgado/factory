package node

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/afonsodelgado/factory/internal/host"
	"github.com/afonsodelgado/factory/internal/phone"
	"github.com/afonsodelgado/factory/internal/review"
	"github.com/afonsodelgado/factory/internal/state"
)

const (
	ticketLife   = 2 * time.Minute
	proxyTimeout = 10 * time.Second
	stacksDir    = "stacks"
)

type Stacks struct {
	Tailscale Tailscale
	Gate      Gate
	Host      string
	Origin    string
	Review    *review.Server
	StateDir  string

	mu      sync.Mutex
	live    map[string]*liveStack
	tickets map[string]ticket
	bridged http.Handler
}

type stackRecord struct {
	host.StackLease
	Run    string           `json:"run"`
	Host   string           `json:"host"`
	HTTPS  int              `json:"https,omitempty"`
	Proxy  int              `json:"proxy,omitempty"`
	Mapped bool             `json:"mapped,omitempty"`
	Stack  *process         `json:"stack,omitempty"`
	Status host.StackStatus `json:"status"`
}

type liveStack struct {
	record  stackRecord
	recipe  Recipe
	cancel  context.CancelFunc
	process *stackProcess
	server  *http.Server
	settled bool
	browser string
}

func (s *Stacks) loopback() bool { return s.Host == "" }

func (s *Stacks) origin(r stackRecord) string {
	if s.loopback() {
		return "http://127.0.0.1:" + strconv.Itoa(r.HTTPS)
	}
	return HTTPSOrigin(s.Host, r.HTTPS)
}

type ticket struct {
	factory string
	expires time.Time
}

func (s *Stacks) Start(ctx context.Context, lease host.StackLease, profile state.Profile) host.StackStatus {
	s.Stop(lease.Factory)
	ctx, cancel := context.WithCancel(ctx)
	run := &liveStack{record: stackRecord{StackLease: lease, Run: rand.Text(), Host: s.Host}, cancel: cancel, browser: rand.Text()}
	run.record.Status = host.StackStatus{Factory: lease.Factory, State: host.StackStarting, At: time.Now()}
	s.mu.Lock()
	if s.live == nil {
		s.live = map[string]*liveStack{}
	}
	s.live[lease.Factory] = run
	s.mu.Unlock()
	return s.settle(run, s.bringUp(ctx, run, profile))
}

func (s *Stacks) bringUp(ctx context.Context, run *liveStack, profile state.Profile) error {
	recipe, err := RecipeOf(profile, run.record.Worktree)
	if err != nil {
		return failed(host.FailRecipe, err)
	}
	if err := s.reserve(ctx, run); err != nil {
		return err
	}
	ln, err := s.listen(run)
	if err != nil {
		return failed(host.FailPorts, fmt.Errorf("the review stack proxy could not listen on a loopback port: %w", err))
	}
	origin := s.origin(run.record)
	if run.recipe, err = recipe.Bind(origin); err != nil {
		ln.Close()
		return err
	}
	run.server = &http.Server{Handler: s.proxy(run), ReadHeaderTimeout: proxyTimeout}
	go run.server.Serve(ln)
	if _, err := bringUp(ctx, run.recipe, func(p *stackProcess) {
		run.process = p
		s.update(run, func(r *stackRecord) { r.Stack = p.proc })
	}); err != nil {
		return err
	}
	if err := s.trySignIn(ctx, run); err != nil {
		return err
	}
	if s.loopback() {
		return nil
	}
	s.update(run, func(r *stackRecord) { r.Mapped = true })
	if _, err := s.Tailscale.call(ctx, ServeArgs(run.record.HTTPS, run.record.Proxy)...); err != nil {
		return failed(host.FailServe, fmt.Errorf("tailscale serve did not publish %s: %w.%s", origin, err, PrivilegeNote()))
	}
	return nil
}

func (s *Stacks) trySignIn(ctx context.Context, run *liveStack) error {
	cookies, err := signIn(ctx, run.recipe)
	if err != nil {
		return failed(host.FailSignIn, err)
	}
	upstream, _ := upstreamOf(run.recipe.URL)
	public, _ := url.Parse(s.origin(run.record))
	if err := holdable(cookies, upstream.Hostname(), public.Host); err != nil {
		return failed(host.FailPhoneSignIn, err)
	}
	return nil
}

func (s *Stacks) reserve(ctx context.Context, run *liveStack) error {
	if s.loopback() {
		return nil
	}
	sc, err := s.Tailscale.Serve(ctx)
	if err != nil {
		return failed(host.FailServe, err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	https, err := pickHTTPS(sc, s.reservedLocked())
	if err != nil {
		return err
	}
	run.record.HTTPS = https
	return s.journal(run.record)
}

func (s *Stacks) reservedLocked() []int {
	var ports []int
	for _, run := range s.live {
		ports = append(ports, run.record.HTTPS)
	}
	return ports
}

func (s *Stacks) listen(run *liveStack) (net.Listener, error) {
	ln, err := s.listener()
	if err != nil {
		return nil, err
	}
	port := ln.Addr().(*net.TCPAddr).Port
	s.update(run, func(r *stackRecord) {
		r.Proxy = port
		if s.loopback() {
			r.HTTPS = port
		}
	})
	return ln, nil
}

func (s *Stacks) listener() (net.Listener, error) {
	if !s.loopback() {
		return net.Listen("tcp", "127.0.0.1:0")
	}
	s.mu.Lock()
	reserved := s.reservedLocked()
	s.mu.Unlock()
	var err error
	for port := FirstAppHTTPS; port <= LastAppHTTPS; port++ {
		if slices.Contains(reserved, port) {
			continue
		}
		var ln net.Listener
		if ln, err = net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port))); err == nil {
			return ln, nil
		}
	}
	return nil, fmt.Errorf("every port from %d to %d on 127.0.0.1 is taken: %w", FirstAppHTTPS, LastAppHTTPS, err)
}

func (s *Stacks) settle(run *liveStack, err error) host.StackStatus {
	status := s.outcome(run, err)
	if err != nil {
		s.release(run)
	}
	s.mu.Lock()
	current := s.live[run.record.Factory] == run
	if current {
		run.settled = true
		run.record.Status = status
		s.journal(run.record)
	}
	s.mu.Unlock()
	if !current {
		s.drop(run)
	}
	return status
}

func (s *Stacks) outcome(run *liveStack, err error) host.StackStatus {
	status := host.StackStatus{Factory: run.record.Factory, State: host.StackReady, At: time.Now()}
	if err != nil {
		status.State, status.Failure, status.Reason = host.StackFailed, failureOf(err), err.Error()
		return status
	}
	app, _ := url.Parse(run.recipe.URL)
	status.Origin = s.origin(run.record)
	status.URL = status.Origin + app.RequestURI()
	status.Href = "/w/" + url.PathEscape(run.record.Route)
	return status
}

func failureOf(err error) string {
	var named host.BrowserFailure
	if errors.As(err, &named) {
		return named.Name
	}
	return host.FailStack
}

func (s *Stacks) Stop(key string) {
	s.mu.Lock()
	run := s.live[key]
	delete(s.live, key)
	settled := run != nil && run.settled
	s.mu.Unlock()
	if run == nil {
		return
	}
	run.cancel()
	if settled {
		s.drop(run)
	}
}

func (s *Stacks) drop(run *liveStack) {
	if s.release(run) {
		os.Remove(s.recordPath(run.record.Run))
	}
}

func (s *Stacks) release(run *liveStack) bool {
	unmapped := s.unmap(run.record)
	if run.server != nil {
		run.server.Close()
		run.server = nil
	}
	run.process.stop()
	run.process = nil
	s.update(run, func(r *stackRecord) {
		r.Stack = nil
		if unmapped {
			r.Mapped, r.HTTPS, r.Proxy = false, 0, 0
		}
	})
	return unmapped
}

func (s *Stacks) unmap(r stackRecord) bool {
	if !r.Mapped {
		return true
	}
	ctx := context.Background()
	sc, err := s.Tailscale.Serve(ctx)
	if err != nil {
		return false
	}
	if mapping, _ := sc.Mapping(r.Host, r.HTTPS, Target(r.Proxy)); mapping != Ours {
		return true
	}
	_, err = s.Tailscale.call(ctx, UnserveArgs(r.HTTPS)...)
	return err == nil
}

func (s *Stacks) update(run *liveStack, change func(*stackRecord)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	change(&run.record)
	s.journal(run.record)
}

func (s *Stacks) Status(key string) (host.StackStatus, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	run, ok := s.live[key]
	if !ok {
		return host.StackStatus{}, false
	}
	return run.record.Status, true
}

func (s *Stacks) Keys() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Collect(maps.Keys(s.live))
}

func (s *Stacks) Recover() []host.StackLease {
	paths, _ := filepath.Glob(filepath.Join(s.StateDir, stacksDir, "*.json"))
	var stopped []host.StackLease
	for _, path := range paths {
		var r stackRecord
		if data, err := os.ReadFile(path); err != nil || json.Unmarshal(data, &r) != nil {
			os.Remove(path)
			continue
		}
		if r.Status.State != host.StackFailed {
			stopped = append(stopped, r.StackLease)
			r.Status = host.StackStatus{Factory: r.Factory, State: host.StackFailed, At: time.Now(), Failure: host.FailRestart, Reason: "the node restarted, so the review stack stopped and its tailnet port closed; run factory review stack again"}
		}
		if r.Stack != nil && r.Stack.ours() {
			killGroup(r.Stack.PID)
		}
		run := &liveStack{record: r, cancel: func() {}, settled: true}
		s.release(run)
		s.mu.Lock()
		if s.live == nil {
			s.live = map[string]*liveStack{}
		}
		s.live[r.Factory] = run
		s.mu.Unlock()
	}
	return stopped
}

func (s *Stacks) Close() error {
	s.mu.Lock()
	runs := slices.Collect(maps.Values(s.live))
	s.mu.Unlock()
	for _, run := range runs {
		run.cancel()
		s.mu.Lock()
		settled := run.settled
		s.mu.Unlock()
		if settled {
			s.release(run)
		}
	}
	return nil
}

func (s *Stacks) recordPath(run string) string {
	return filepath.Join(s.StateDir, stacksDir, run+".json")
}

func (s *Stacks) journal(r stackRecord) error {
	if s.StateDir == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Join(s.StateDir, stacksDir), 0o700); err != nil {
		return err
	}
	data, _ := json.Marshal(r)
	path := s.recordPath(r.Run)
	if err := os.WriteFile(path+".tmp", data, 0o600); err != nil {
		return err
	}
	return os.Rename(path+".tmp", path)
}

func (s *Stacks) Open(w http.ResponseWriter, r *http.Request) {
	run, status, found := s.byRoute(r.PathValue("route"))
	switch {
	case !found:
		phone.Notice(w, http.StatusNotFound, "No review stack runs for this factory", "The supervisor starts it with factory review stack, and the factory's Pages tab links to it once it answers.")
	case status.State == host.StackFailed:
		phone.Notice(w, http.StatusConflict, "The review stack failed at "+status.Failure, status.Reason)
	case status.State != host.StackReady:
		phone.Notice(w, http.StatusConflict, "The review stack is still starting", "Open it again in a moment.")
	case run.recipe.SignIn == state.NotNeeded && !s.loopback():
		http.Redirect(w, r, status.URL, http.StatusSeeOther)
	default:
		w.Header().Set("Referrer-Policy", "no-referrer")
		http.Redirect(w, r, status.Origin+review.AppPrefix+"/sign-in?ticket="+s.mint(run.record.Factory), http.StatusSeeOther)
	}
}

func (s *Stacks) byRoute(route string) (*liveStack, host.StackStatus, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, run := range s.live {
		if run.record.Route == route {
			return run, run.record.Status, true
		}
	}
	return nil, host.StackStatus{}, false
}

func (s *Stacks) mint(key string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.tickets == nil {
		s.tickets = map[string]ticket{}
	}
	now := time.Now()
	maps.DeleteFunc(s.tickets, func(_ string, t ticket) bool { return !t.expires.After(now) })
	token := rand.Text()
	s.tickets[token] = ticket{factory: key, expires: now.Add(ticketLife)}
	return token
}

func (s *Stacks) redeem(token, key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tickets[token]
	delete(s.tickets, token)
	return ok && t.factory == key && t.expires.After(time.Now())
}
