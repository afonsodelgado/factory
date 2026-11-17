package node

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os/exec"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	tailscaleWait   = 5 * time.Second
	exposureEvery   = 10 * time.Second
	unconfirmed     = "the node has not yet confirmed that Funnel is off, so it answers nothing over the network"
	UntagFix        = "sudo tailscale up --advertise-tags= --force-reauth"
	HTTPSFix        = "turn on HTTPS Certificates at https://login.tailscale.com/admin/dns"
	OperatorCommand = "sudo tailscale set --operator=$USER"
)

var (
	ErrNoTailscale = errors.New("Tailscale is not installed; install it from https://tailscale.com/download and log in")
	ErrLoggedOut   = errors.New("Tailscale is not logged in or not running; run tailscale up")
	errUnnamed     = errors.New("tailscale status names no user login or MagicDNS name for this machine; check tailscale status and turn on MagicDNS in the admin console")
)

type Runner func(ctx context.Context, name string, args ...string) ([]byte, error)

func System(ctx context.Context, name string, args ...string) ([]byte, error) {
	out, err := exec.CommandContext(ctx, name, args...).Output()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return out, fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(exit.Stderr)))
	}
	return out, err
}

type Tailscale struct{ Run Runner }

type Self struct {
	ID          string
	Login       string
	DNSName     string
	Tailnet     string
	OS          string
	Tagged      bool
	CertDomains []string
}

func (s Self) Short() string {
	name, _, _ := strings.Cut(s.DNSName, ".")
	return name
}

func (s Self) HTTPS() bool { return slices.Contains(s.CertDomains, s.DNSName) }

type device struct {
	ID           string
	DNSName      string
	OS           string
	UserID       int64
	Tags         []string
	Online       bool
	TailscaleIPs []string
	LastSeen     time.Time
}

type status struct {
	BackendState   string
	Self           device
	Peer           map[string]device
	User           map[string]struct{ LoginName string }
	CurrentTailnet struct{ MagicDNSSuffix string }
	CertDomains    []string
}

func (s status) login(d device) string { return s.User[strconv.FormatInt(d.UserID, 10)].LoginName }

func (t Tailscale) call(ctx context.Context, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, tailscaleWait)
	defer cancel()
	out, err := t.Run(ctx, "tailscale", args...)
	if errors.Is(err, exec.ErrNotFound) {
		return nil, ErrNoTailscale
	}
	return out, err
}

func (t Tailscale) Status(ctx context.Context) (Self, error) {
	inv, err := t.Inventory(ctx)
	return inv.Self, err
}

func (t Tailscale) Inventory(ctx context.Context) (Inventory, error) {
	out, err := t.call(ctx, "status", "--json")
	if errors.Is(err, ErrNoTailscale) {
		return Inventory{}, err
	}
	var s status
	if err == nil {
		err = json.Unmarshal(out, &s)
	}
	if err != nil || s.BackendState != "Running" {
		return Inventory{}, ErrLoggedOut
	}
	self := Self{
		ID:          s.Self.ID,
		Login:       s.login(s.Self),
		DNSName:     strings.TrimSuffix(s.Self.DNSName, "."),
		Tailnet:     strings.TrimSuffix(s.CurrentTailnet.MagicDNSSuffix, "."),
		OS:          s.Self.OS,
		Tagged:      len(s.Self.Tags) > 0,
		CertDomains: s.CertDomains,
	}
	if !self.Tagged && (self.Login == "" || self.DNSName == "") {
		return Inventory{}, errUnnamed
	}
	inv := Inventory{Self: self}
	for _, key := range slices.Sorted(maps.Keys(s.Peer)) {
		d := s.Peer[key]
		inv.Peers = append(inv.Peers, Peer{ID: d.ID, Name: strings.ToLower(strings.TrimSuffix(d.DNSName, ".")), OS: d.OS, Login: s.login(d), Tags: d.Tags, Online: d.Online, LastSeen: d.LastSeen, IPs: d.TailscaleIPs})
	}
	return inv, nil
}

type ServeConfig struct {
	TCP map[string]struct {
		HTTPS        bool
		TCPForward   string
		TerminateTLS string
	}
	Web map[string]struct {
		Handlers map[string]struct{ Proxy, Text, Path string }
	}
	AllowFunnel map[string]bool
	Foreground  map[string]ServeConfig
}

func (t Tailscale) Serve(ctx context.Context) (ServeConfig, error) {
	out, err := t.call(ctx, "serve", "status", "--json")
	var sc ServeConfig
	if err == nil {
		err = json.Unmarshal(out, &sc)
	}
	if err != nil {
		return ServeConfig{}, fmt.Errorf("could not read tailscale serve status: %w", err)
	}
	return sc, nil
}

func Target(port int) string { return "http://127.0.0.1:" + strconv.Itoa(port) }

const HomeHTTPS = 443

func ServeArgs(https, port int) []string {
	return []string{"serve", "--bg", "--https=" + strconv.Itoa(https), Target(port)}
}

func HTTPSOrigin(host string, https int) string {
	if https == HomeHTTPS {
		return "https://" + host
	}
	return "https://" + host + ":" + strconv.Itoa(https)
}

func UnserveArgs(https int) []string {
	return []string{"serve", "--https=" + strconv.Itoa(https), "off"}
}

type Mapping int

const (
	Unmapped Mapping = iota
	Ours
	Foreign
)

func (sc ServeConfig) Mapping(host string, https int, target string) (Mapping, string) {
	port := strconv.Itoa(https)
	web, served := sc.Web[host+":"+port]
	forward := sc.TCP[port].TCPForward
	switch {
	case forward != "":
		return Foreign, "port " + port + " forwards raw TCP to " + forward
	case !served || len(web.Handlers) == 0:
		return Unmapped, ""
	}
	root := web.Handlers["/"]
	isOurs := len(web.Handlers) == 1 && strings.TrimSuffix(root.Proxy, "/") == target
	if isOurs {
		return Ours, ""
	}
	var uses []string
	for _, path := range slices.Sorted(maps.Keys(web.Handlers)) {
		h := web.Handlers[path]
		uses = append(uses, HTTPSOrigin(host, https)+path+" → "+cmp.Or(h.Proxy, h.Path, "text"))
	}
	return Foreign, strings.Join(uses, ", ")
}

type Funnel struct {
	Where string
	Fix   string
}

func (sc ServeConfig) Funnels() []Funnel {
	var funnels []Funnel
	for _, hostPort := range enabled(sc.AllowFunnel) {
		funnels = append(funnels, sc.funnelFixes(hostPort)...)
	}
	for _, session := range slices.Sorted(maps.Keys(sc.Foreground)) {
		for _, hostPort := range enabled(sc.Foreground[session].AllowFunnel) {
			funnels = append(funnels, Funnel{Where: hostPort + " (a foreground session)", Fix: "stop the tailscale funnel command that runs in the foreground for " + hostPort})
		}
	}
	return funnels
}

func enabled(allow map[string]bool) []string {
	var on []string
	for hostPort, isOn := range allow {
		if isOn {
			on = append(on, hostPort)
		}
	}
	slices.Sort(on)
	return on
}

func (sc ServeConfig) funnelFixes(hostPort string) []Funnel {
	port := hostPort[strings.LastIndex(hostPort, ":")+1:]
	tcp := sc.TCP[port]
	switch {
	case tcp.TCPForward != "" && tcp.TerminateTLS != "":
		return []Funnel{{Where: hostPort + " (TLS-terminated TCP)", Fix: "tailscale funnel --tls-terminated-tcp=" + port + " off"}}
	case tcp.TCPForward != "":
		return []Funnel{{Where: hostPort + " (TCP)", Fix: "tailscale funnel --tcp=" + port + " off"}}
	}
	paths := slices.Sorted(maps.Keys(sc.Web[hostPort].Handlers))
	if len(paths) == 0 {
		paths = []string{"/"}
	}
	funnels := make([]Funnel, len(paths))
	for i, path := range paths {
		fix := "tailscale funnel --https=" + port + " off"
		if path != "/" {
			fix = "tailscale funnel --https=" + port + " --set-path=" + path + " off"
		}
		funnels[i] = Funnel{Where: "https://" + hostPort + path, Fix: fix}
	}
	return funnels
}

func PrivilegeNote() string {
	if runtime.GOOS != "linux" {
		return ""
	}
	return " Tailscale Serve and Funnel changes need sudo here unless you are the Tailscale operator: " + OperatorCommand + "."
}

type Exposure struct {
	Tailscale Tailscale

	mu      sync.Mutex
	checked bool
	problem string
	fixes   []string
	safe    chan struct{}
}

var neverSafe = func() chan struct{} {
	c := make(chan struct{})
	close(c)
	return c
}()

func (e *Exposure) Unsafe() <-chan struct{} {
	if e == nil {
		return neverSafe
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.safe == nil {
		return neverSafe
	}
	return e.safe
}

func (e *Exposure) Check(ctx context.Context) {
	sc, err := e.Tailscale.Serve(ctx)
	problem, fixes := "", []string(nil)
	switch funnels := sc.Funnels(); {
	case err != nil:
		problem = "the node cannot tell whether Funnel is off, so it answers nothing over the network: " + err.Error()
	case len(funnels) > 0:
		problem = "Funnel puts this machine on the public internet, so the node answers nothing over the network until every Funnel entry is off"
		for _, f := range funnels {
			fixes = append(fixes, f.Fix)
		}
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.checked, e.problem, e.fixes = true, problem, fixes
	switch {
	case problem != "" && e.safe != nil:
		close(e.safe)
		e.safe = nil
	case problem == "" && e.safe == nil:
		e.safe = make(chan struct{})
	}
}

func (e *Exposure) Watch(ctx context.Context) {
	ticker := time.NewTicker(exposureEvery)
	defer ticker.Stop()
	for {
		e.Check(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (e *Exposure) Refusal() (string, []string) {
	if e == nil {
		return unconfirmed, nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.checked {
		return unconfirmed, nil
	}
	return e.problem, slices.Clone(e.fixes)
}
