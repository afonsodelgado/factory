package nodetest

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/afonsodelgado/factory/internal/host"
	"github.com/afonsodelgado/factory/internal/node"
	"github.com/afonsodelgado/factory/internal/phone"
)

const (
	Suffix  = "tail5e2c.ts.net"
	Owner   = "alice@example.com"
	Version = "0.9.4"
)

type Tailnet struct {
	T          testing.TB
	ProbeWait  time.Duration
	ReadyWait  time.Duration
	PushClient *http.Client

	mu       sync.Mutex
	machines []*Machine
	strays   []string
	offset   time.Duration
	base     *http.Transport
	watched  map[string][]string
}

func (tn *Tailnet) Watch(path string) {
	tn.mu.Lock()
	defer tn.mu.Unlock()
	if tn.watched == nil {
		tn.watched = map[string][]string{}
	}
	tn.watched[path] = []string{}
}

func (tn *Tailnet) Bodies(path string) []string {
	tn.mu.Lock()
	defer tn.mu.Unlock()
	return slices.Clone(tn.watched[path])
}

func (tn *Tailnet) record(req *http.Request) {
	tn.mu.Lock()
	defer tn.mu.Unlock()
	if _, watched := tn.watched[req.URL.Path]; !watched || req.Body == nil {
		return
	}
	data, _ := io.ReadAll(req.Body)
	req.Body = io.NopCloser(bytes.NewReader(data))
	tn.watched[req.URL.Path] = append(tn.watched[req.URL.Path], string(data))
}

func New(t testing.TB) *Tailnet {
	tn := &Tailnet{T: t, ProbeWait: 300 * time.Millisecond, ReadyWait: 300 * time.Millisecond, base: &http.Transport{}}
	t.Cleanup(tn.base.CloseIdleConnections)
	return tn
}

func (tn *Tailnet) Now() time.Time {
	tn.mu.Lock()
	defer tn.mu.Unlock()
	return time.Now().Add(tn.offset)
}

func (tn *Tailnet) Advance(d time.Duration) {
	tn.mu.Lock()
	defer tn.mu.Unlock()
	tn.offset += d
}

func (tn *Tailnet) Strays() []string {
	tn.mu.Lock()
	defer tn.mu.Unlock()
	return slices.Clone(tn.strays)
}

type Machine struct {
	Name     string
	ID       string
	OS       string
	Login    string
	Tags     []string
	IP       string
	LastSeen time.Time

	net      *Tailnet
	online   bool
	funnel   bool
	hang     bool
	handler  http.Handler
	addr     string
	node     *Node
	served   map[string]string
	serveLog []string
}

func (tn *Tailnet) Machine(short string) *Machine {
	tn.mu.Lock()
	defer tn.mu.Unlock()
	m := &Machine{Name: short + "." + Suffix, ID: "n" + short + "CNTRL", OS: "linux", Login: Owner, IP: fmt.Sprintf("100.64.0.%d", len(tn.machines)+1), net: tn, online: true}
	tn.machines = append(tn.machines, m)
	return m
}

func (m *Machine) Short() string { return strings.Split(m.Name, ".")[0] }

func (m *Machine) SetOnline(on bool) {
	m.net.mu.Lock()
	defer m.net.mu.Unlock()
	m.online = on
}

func (m *Machine) Hang() {
	m.net.mu.Lock()
	defer m.net.mu.Unlock()
	m.hang = true
}

func (m *Machine) SetFunnel(on bool) {
	m.net.mu.Lock()
	m.funnel = on
	n := m.node
	m.net.mu.Unlock()
	if n != nil {
		n.Exposure.Check(context.Background())
	}
}

func (m *Machine) Serve(h http.Handler) {
	server := httptest.NewServer(h)
	m.net.T.Cleanup(server.Close)
	m.net.mu.Lock()
	defer m.net.mu.Unlock()
	m.addr = server.Listener.Addr().String()
}

func (m *Machine) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	switch strings.Join(append([]string{name}, args...), " ") {
	case "tailscale status --json":
		return m.net.status(m), nil
	case "tailscale serve status --json":
		return m.serveStatus(), nil
	}
	if len(args) == 4 && args[0] == "serve" && args[1] == "--bg" {
		return nil, m.serveChange(strings.TrimPrefix(args[2], "--https="), args[3])
	}
	if len(args) == 3 && args[0] == "serve" && args[2] == "off" {
		return nil, m.serveChange(strings.TrimPrefix(args[1], "--https="), "")
	}
	return nil, fmt.Errorf("the fake tailnet does not run %s %v", name, args)
}

func (m *Machine) ServeOn(port, target string) { m.serveChange(port, target) }

func (m *Machine) Served() map[string]string {
	m.net.mu.Lock()
	defer m.net.mu.Unlock()
	return maps.Clone(m.served)
}

func (m *Machine) ServeLog() []string {
	m.net.mu.Lock()
	defer m.net.mu.Unlock()
	return slices.Clone(m.serveLog)
}

func (m *Machine) serveChange(port, target string) error {
	m.net.mu.Lock()
	defer m.net.mu.Unlock()
	if m.served == nil {
		m.served = map[string]string{}
	}
	if target == "" {
		m.serveLog = append(m.serveLog, "serve --https="+port+" off")
		delete(m.served, port)
		return nil
	}
	m.serveLog = append(m.serveLog, "serve --bg --https="+port+" "+target)
	m.served[port] = target
	return nil
}

func (m *Machine) serveStatus() []byte {
	m.net.mu.Lock()
	defer m.net.mu.Unlock()
	type handler struct{ Proxy string }
	type web struct{ Handlers map[string]handler }
	sc := struct {
		TCP         map[string]map[string]bool
		Web         map[string]web
		AllowFunnel map[string]bool `json:",omitempty"`
	}{TCP: map[string]map[string]bool{}, Web: map[string]web{}}
	for port, target := range m.served {
		sc.TCP[port] = map[string]bool{"HTTPS": true}
		sc.Web[m.Name+":"+port] = web{Handlers: map[string]handler{"/": {Proxy: target}}}
	}
	if m.funnel {
		sc.TCP["443"] = map[string]bool{"HTTPS": true}
		sc.AllowFunnel = map[string]bool{m.Name + ":443": true}
	}
	data, _ := json.Marshal(sc)
	return data
}

type device struct {
	ID           string
	DNSName      string
	OS           string
	UserID       int64
	Tags         []string `json:",omitempty"`
	Online       bool
	TailscaleIPs []string
	LastSeen     time.Time
}

func (tn *Tailnet) status(self *Machine) []byte {
	tn.mu.Lock()
	defer tn.mu.Unlock()
	users := map[string]struct{ LoginName string }{}
	userID := func(m *Machine) int64 {
		login := m.Login
		if len(m.Tags) > 0 {
			login = "tagged-devices"
		}
		for id, u := range users {
			if u.LoginName == login {
				var n int64
				fmt.Sscan(id, &n)
				return n
			}
		}
		id := int64(len(users) + 1)
		users[fmt.Sprint(id)] = struct{ LoginName string }{login}
		return id
	}
	describe := func(m *Machine) device {
		return device{ID: m.ID, DNSName: m.Name + ".", OS: m.OS, UserID: userID(m), Tags: m.Tags, Online: m.online, TailscaleIPs: []string{m.IP}, LastSeen: m.LastSeen}
	}
	peers := map[string]device{}
	for _, m := range tn.machines {
		if m != self {
			peers["nodekey:"+m.ID] = describe(m)
		}
	}
	data, _ := json.Marshal(map[string]any{
		"BackendState":   "Running",
		"Self":           describe(self),
		"Peer":           peers,
		"User":           users,
		"CurrentTailnet": map[string]string{"MagicDNSSuffix": Suffix + "."},
		"CertDomains":    []string{self.Name},
	})
	return data
}

func (tn *Tailnet) Client(from *Machine) *http.Client {
	c := node.PeerHTTP()
	c.Transport = serve{net: tn, from: from}
	return c
}

type serve struct {
	net  *Tailnet
	from *Machine
}

func (s serve) RoundTrip(req *http.Request) (*http.Response, error) {
	tn := s.net
	tn.mu.Lock()
	i := slices.IndexFunc(tn.machines, func(m *Machine) bool { return m.Name == req.URL.Hostname() })
	if i < 0 || req.URL.Scheme != "https" {
		tn.strays = append(tn.strays, req.URL.String())
		tn.mu.Unlock()
		return nil, fmt.Errorf("the fake tailnet has no https://%s", req.URL.Hostname())
	}
	to := tn.machines[i]
	reachable, hang, addr := s.from.online && to.online, to.hang, to.addr
	tagged, login := len(s.from.Tags) > 0, s.from.Login
	tn.mu.Unlock()
	switch {
	case !reachable:
		return nil, fmt.Errorf("dial %s: %w", to.Name, syscall.EHOSTUNREACH)
	case hang:
		<-req.Context().Done()
		return nil, req.Context().Err()
	case addr == "":
		return nil, fmt.Errorf("dial %s:443: %w", to.Name, syscall.ECONNREFUSED)
	}
	out := req.Clone(req.Context())
	tn.record(out)
	for name := range out.Header {
		if strings.HasPrefix(http.CanonicalHeaderKey(name), "Tailscale-") {
			out.Header.Del(name)
		}
	}
	if !tagged {
		out.Header.Set(node.LoginHeader, login)
	}
	out.URL.Scheme, out.URL.Host, out.Host = "http", addr, to.Name
	return tn.base.RoundTrip(out)
}

type Runtime struct {
	mu        sync.Mutex
	factories []host.LocalFactory
	calls     []string
	behind    node.Caller
}

func (r *Runtime) Behind(c node.Caller) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.behind = c
}

func (r *Runtime) SetFactories(f ...host.LocalFactory) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.factories = f
}

func (r *Runtime) Calls() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.calls)
}

func (r *Runtime) Call(ctx context.Context, m host.Message) (host.Message, error) {
	r.mu.Lock()
	if behind := r.behind; behind != nil {
		r.mu.Unlock()
		return behind.Call(ctx, m)
	}
	defer r.mu.Unlock()
	var value any
	switch m.T {
	case "local":
		value = host.Local{Factories: r.factories}
	case "attach":
		var a host.AttachRequest
		json.Unmarshal(m.Data, &a)
		if a.Key == "" {
			return host.Message{T: "error", Error: "no factory"}, nil
		}
		r.calls = append(r.calls, m.T)
		value = "view-" + a.Key + "-" + a.Token
	default:
		r.calls = append(r.calls, m.T)
	}
	data, _ := json.Marshal(value)
	return host.Message{T: "done", Data: data}, nil
}

type Node struct {
	*Machine
	ID       string
	Dir      string
	Nodes    *node.Nodes
	API      *node.API
	Exposure *node.Exposure
	Runtime  *Runtime
	Local    *node.Client
	Phone    phone.Store
	Pusher   *phone.Pusher

	mu     sync.Mutex
	probes []node.Probe
	clones []string
	repos  map[string]string
}

func (n *Node) SetRepos(repos map[string]string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.repos = repos
}

func (n *Node) currentRepos() map[string]string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return maps.Clone(n.repos)
}

func (n *Node) Clones() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return slices.Clone(n.clones)
}

func (n *Node) clone(_ context.Context, url string) (string, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.clones = append(n.clones, url)
	return filepath.Join(n.Dir, "repos", filepath.Base(url)), nil
}

func Passing(name, detail string) node.Probe {
	return node.Probe{Name: name, Run: func(context.Context) node.Check {
		return node.Check{Name: name, Mark: node.MarkOK, Detail: detail}
	}}
}

func (n *Node) SetProbes(p ...node.Probe) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.probes = p
}

func (n *Node) currentProbes(node.Scope) []node.Probe {
	n.mu.Lock()
	defer n.mu.Unlock()
	return slices.Clone(n.probes)
}

func (m *Machine) Start() *Node {
	tn := m.net
	t := tn.T
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "fnt-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	n := &Node{Machine: m, ID: uuid.NewString(), Dir: dir, Runtime: &Runtime{}}
	n.probes = []node.Probe{Passing("tools", "tmux 3.4 · git 2.43 · gh 2.45")}
	directory, err := node.OpenDirectory(dir, n.ID, tn.Now)
	if err != nil {
		t.Fatal(err)
	}
	_, tailnet, _ := strings.Cut(m.Name, ".")
	self := node.Member{ID: n.ID, Peer: m.ID, Name: m.Name, Owner: m.Login, Tailnet: tailnet, OS: m.OS}
	if err := directory.Seed(self, "https://"+m.Name); err != nil {
		t.Fatal(err)
	}
	ts := node.Tailscale{Run: m.Run}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	n.API = &node.API{Version: Version, Node: n.ID, Boot: uuid.NewString(), Now: tn.Now}
	n.Nodes = &node.Nodes{
		Self: self, Version: Version, Dir: directory, Tailscale: ts, HTTP: tn.Client(m),
		Ready:     node.Readiness{Probes: n.currentProbes, Wait: tn.ReadyWait, Now: tn.Now},
		Factories: func() int { _, f, _ := n.API.Factories(); return len(f) }, ProbeWait: tn.ProbeWait, Now: tn.Now, Clone: n.clone, Repos: n.currentRepos,
	}
	n.API.Commands = node.Commands{Runtime: node.Activation{Caller: n.Runtime, Ready: n.Nodes.Runnable, Exists: n.API.Holds}}
	n.API.Nodes = n.Nodes
	n.Exposure = &node.Exposure{Tailscale: ts}
	n.Exposure.Check(ctx)
	gate := node.Gate{Owner: m.Login, Hosts: []string{m.Name}, Origin: "https://" + m.Name, Home: directory.Home, Exposure: n.Exposure}
	app := n.phone(t, directory)
	m.Serve(gate.Wrap(n.API.With(app, gate.Principal, node.NetworkKinds...)))
	socket := filepath.Join(dir, "node.sock")
	ln, err := node.ListenUnix(socket)
	if err != nil {
		t.Fatal(err)
	}
	local := &http.Server{Handler: n.API.With(app, gate.Principal)}
	go local.Serve(ln)
	t.Cleanup(func() { local.Close() })
	n.Local = node.Unix(socket)
	go n.API.Poll(ctx)
	tn.mu.Lock()
	m.node = n
	tn.mu.Unlock()
	n.waitForSnapshot()
	return n
}

func (n *Node) phone(t testing.TB, directory *node.Directory) http.Handler {
	n.Phone = phone.Open(n.Dir)
	if _, err := n.Phone.VAPIDKey(""); err != nil {
		t.Fatal(err)
	}
	n.Nodes.Phone = &n.Phone
	removed := func() bool { return directory.Roster().Removed }
	n.Pusher = &phone.Pusher{Store: n.Phone, Origin: "https://" + n.Name, Client: cmp.Or(n.net.PushClient, http.DefaultClient), Now: n.net.Now, Removed: removed}
	server := &phone.Server{Store: n.Phone, Factories: n.API.Factories, Connected: n.API.Connected, Push: n.Pusher, Node: n.ID, Changed: n.Nodes.Kick, Now: n.net.Now}
	return server.Handler()
}

func (n *Node) Notify(ctx context.Context) error {
	_, factories, _ := n.API.Factories()
	if _, err := n.Phone.Reconcile(n.ID, factories, func(string) bool { return false }, n.net.Now()); err != nil {
		return err
	}
	n.Pusher.Deliver(ctx)
	return nil
}

func (n *Node) waitForSnapshot() {
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := n.API.Latest(); ok {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	n.net.T.Fatalf("%s published no snapshot", n.Short())
}

func (n *Node) Roster() node.Roster { return n.Nodes.Dir.Roster() }

func (n *Node) Names() []string {
	var names []string
	for _, m := range n.Roster().Members {
		names = append(names, strings.Split(m.Name, ".")[0])
	}
	slices.Sort(names)
	return names
}

func (tn *Tailnet) Settle(nodes ...*Node) {
	for range 3 {
		for _, n := range nodes {
			n.Nodes.Sync(context.Background())
		}
	}
}

func (n *Node) Add(name string) (node.AddResult, error) {
	return n.AddWith(node.NodeRequest{Operation: uuid.NewString(), Name: name})
}

func (n *Node) AddWith(r node.NodeRequest) (node.AddResult, error) {
	var result node.AddResult
	err := n.Local.Do(context.Background(), http.MethodPost, "/api/nodes", r, &result)
	return result, err
}

func (n *Node) Join(name string) (node.AddResult, error) {
	var result node.AddResult
	err := n.Local.Do(context.Background(), http.MethodPost, "/api/nodes/join", node.NodeRequest{Operation: uuid.NewString(), Name: name}, &result)
	return result, err
}

func (n *Node) Remove(id string) error {
	return n.Local.Do(context.Background(), http.MethodPost, "/api/nodes/"+id+"/remove", node.RemoveRequest{Operation: uuid.NewString()}, nil)
}

func (tn *Tailnet) Request(from, to *Machine, method, path, origin string, body any, headers ...string) (int, http.Header, string) {
	tn.T.Helper()
	var payload io.Reader
	if body != nil {
		data, _ := json.Marshal(body)
		payload = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, "https://"+to.Name+path, payload)
	if err != nil {
		tn.T.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	resp, err := tn.Client(from).Do(req)
	if err != nil {
		tn.T.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header, string(data)
}
