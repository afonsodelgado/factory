package main

import (
	"cmp"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"

	"github.com/afonsodelgado/factory/bundle"
	"github.com/afonsodelgado/factory/internal/host"
	"github.com/afonsodelgado/factory/internal/hub"
	"github.com/afonsodelgado/factory/internal/install"
	"github.com/afonsodelgado/factory/internal/node"
	"github.com/afonsodelgado/factory/internal/phone"
	"github.com/afonsodelgado/factory/internal/remote"
	"github.com/afonsodelgado/factory/internal/review"
	"github.com/afonsodelgado/factory/internal/state"
	"github.com/afonsodelgado/factory/internal/tmux"
)

const (
	nodeUsage   = "usage: factory node run [--owner <tailscale login>] | install [--join | --no-join] | uninstall | status | list | add <machine> | remove <node>"
	ownerEnv    = "FACTORY_NODE_OWNER"
	readTimeout = 10 * time.Second
)

var errNoNode = errors.New("no factory node runs on this machine. Start one in the foreground with factory node run, or install it as a service with factory node install")

func runNode(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, nodeUsage)
		return 2
	}
	switch args[0] {
	case "list", "add", "remove":
		return runNodeList(args)
	case "run":
	default:
		return runNodeSetup(args)
	}
	flags := flag.NewFlagSet("node run", flag.ContinueOnError)
	owner := flags.String("owner", "", "the Tailscale login that owns this node, such as you@example.com")
	if err := flags.Parse(args[1:]); err != nil || flags.NArg() > 0 {
		fmt.Fprintln(os.Stderr, nodeUsage)
		return 2
	}
	cfg, err := state.LoadConfig()
	if err == nil {
		err = runNodeService(cfg, cmp.Or(*owner, os.Getenv(ownerEnv), cfg.Node.Owner), node.Tailscale{Run: node.System})
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "factory node:", err)
		return 1
	}
	return 0
}

func runNodeService(cfg *state.Config, owner string, ts node.Tailscale) error {
	release, err := node.Lock(cfg.NodeLockPath(), cfg.LockPath(), hostLockPath(cfg), phone.ServeLockPath(cfg.StateDir))
	if err != nil {
		return err
	}
	defer release()
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer cancel()
	if owner, err = learnIdentity(ctx, cfg, owner, ts); err != nil {
		return err
	}
	n, err := startNode(ctx, cfg, owner, ts)
	if err != nil {
		return err
	}
	defer n.close()
	fmt.Printf("factory node: %s on %s and 127.0.0.1:%d\n", owner, cfg.NodeSocket(), cfg.NodePort())
	return n.wait()
}

func learnIdentity(ctx context.Context, cfg *state.Config, owner string, ts node.Tailscale) (string, error) {
	hostname := cfg.NodeHostname()
	if owner == "" || hostname == "" {
		self, err := ts.Status(ctx)
		switch {
		case err != nil && owner == "":
			return "", fmt.Errorf("this node does not know its owner's Tailscale login, and Tailscale cannot say: %w. Then run factory node run again, or give the login with --owner you@example.com, %s or node.owner in %s", err, ownerEnv, cfg.ConfigFile())
		case err == nil && self.Tagged && owner == "":
			return "", errors.New("this machine is a tagged Tailscale device, so no user owns it and Serve passes no login to check. Run " + node.UntagFix + " and sign in as yourself, then factory node run again")
		case err == nil:
			hostname = cmp.Or(hostname, self.DNSName)
			owner = cmp.Or(owner, self.Login)
		}
	}
	err := state.UpdateMachine(cfg.ConfigDir, func(m *state.Machine) error {
		m.Node.Owner = cmp.Or(m.Node.Owner, owner)
		m.Node.Hostname = cmp.Or(m.NodeHostname(), hostname)
		return nil
	})
	if err != nil {
		return "", err
	}
	cfg.Node.Owner = cmp.Or(cfg.Node.Owner, owner)
	cfg.Node.Hostname = cmp.Or(cfg.NodeHostname(), hostname)
	return owner, nil
}

type runningNode struct {
	closers []io.Closer
	stopped chan error
}

func (n *runningNode) wait() error { return <-n.stopped }

func (n *runningNode) close() {
	for _, c := range slices.Backward(n.closers) {
		c.Close()
	}
}

func startNode(ctx context.Context, cfg *state.Config, owner string, ts node.Tailscale) (*runningNode, error) {
	if err := os.MkdirAll(cfg.SessionsDir(), 0o700); err != nil {
		return nil, err
	}
	vapidKey, err := node.Migrate(cfg)
	if err != nil {
		return nil, err
	}
	tm := tmux.Client{Socket: cfg.Tmux.Socket}
	if err := adoptSession(cfg, tm); err != nil {
		return nil, err
	}
	registry, err := state.LoadRegistry(cfg.RegistryPath())
	if err != nil {
		return nil, err
	}
	id, err := remote.ServerID(cfg.StateDir)
	if err != nil {
		return nil, err
	}
	bin, err := os.Executable()
	if err != nil {
		return nil, err
	}
	panes := &host.Panes{TM: tm, Session: cfg.Hub.Session, Openers: filepath.Join(cfg.StateDir, "openers")}
	if err := host.WriteOpeners(panes.Openers, bin); err != nil {
		return nil, err
	}
	pages, err := startReview(cfg)
	if err != nil {
		return nil, err
	}
	browsers := &node.Browsers{Chrome: cfg.Review.Chrome}
	n := &runningNode{closers: []io.Closer{pages, browsers}, stopped: make(chan error, 1)}
	reviewListener, err := node.ListenUnix(cfg.ReviewSocket())
	if err != nil {
		n.close()
		return nil, err
	}
	n.serve(reviewListener, pages.Handler())
	queue := node.NewQueue()
	pages.SetActions(host.ReviewActions(queue.Sink()))
	runtime := host.New(host.Runtime{
		Config: cfg, Loader: &host.Loader{Config: cfg, Registry: registry, TM: &tm, Now: time.Now}, TM: tm, Session: cfg.Hub.Session,
		Pages: pages, Engine: panes, Typist: panes, Log: host.LoadEventLog(host.EventLogPath(cfg.StateDir)), Push: &host.Pusher{StateDir: cfg.StateDir, Now: time.Now},
		Jobs: &node.Jobs{Queue: queue, Ctx: ctx}, Browsers: browsers, GitHub: &host.GitHub{Run: host.RunGH}, Machine: cfg.NodeName(),
	})
	go runtime.Push.Run(ctx)
	go func() { n.stopped <- runtime.Run(ctx, queue.Events()) }()
	nodes, err := nodeRoster(ctx, cfg, id, owner, ts)
	if err != nil {
		n.close()
		return nil, err
	}
	api := &node.API{Version: version, Node: id, Boot: uuid.NewString(), Nodes: nodes}
	api.Commands = node.Commands{Runtime: node.Activation{Caller: queue, Ready: nodes.Runnable, Exists: api.Holds}}
	nodes.Factories = func() int { _, factories, _ := api.Factories(); return len(factories) }
	nodes.Clone = cloneFor(cfg, queue)
	go api.Poll(ctx)
	go nodes.Replicate(ctx)
	app := phoneApp(ctx, cfg, api, pages, vapidKey)
	exposure := &node.Exposure{Tailscale: ts}
	go exposure.Watch(ctx)
	gate := node.Gate{Owner: owner, Hosts: nodeHosts(cfg), Origin: cfg.NodeOrigin(), Home: nodes.Dir.Home, OpaquePath: phone.GrantPrefix, RefusalPage: phone.RefusalPage, Exposure: exposure}
	unixListener, err := node.ListenUnix(cfg.NodeSocket())
	if err != nil {
		n.close()
		return nil, err
	}
	n.serve(unixListener, api.With(app, gate.Principal))
	tcp, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(cfg.NodePort())))
	if err != nil {
		n.close()
		return nil, fmt.Errorf("port %d is taken; free it or pick another with node.port in %s: %w", cfg.NodePort(), cfg.ConfigFile(), err)
	}
	n.serve(tcp, gate.Wrap(api.With(app, gate.Principal, node.NetworkKinds...)))
	return n, nil
}

func nodeRoster(ctx context.Context, cfg *state.Config, id, owner string, ts node.Tailscale) (*node.Nodes, error) {
	dir, err := node.OpenDirectory(cfg.StateDir, id, time.Now)
	if err != nil {
		return nil, err
	}
	self := node.Member{ID: id, Name: cfg.NodeHostname(), Owner: owner, OS: nodeOS()}
	_, self.Tailnet, _ = strings.Cut(self.Name, ".")
	if status, err := ts.Status(ctx); err == nil {
		self.Peer = status.ID
	}
	if self.Name != "" {
		if err := dir.Seed(self, cfg.NodeOrigin()); err != nil {
			return nil, err
		}
	}
	return &node.Nodes{Self: self, Version: version, Dir: dir, Tailscale: ts, HTTP: node.PeerHTTP(), Ready: node.Readiness{Probes: readinessProbes}, Repos: repoOrigins}, nil
}

func repoOrigins() map[string]string {
	cfg, err := state.LoadConfig()
	if err != nil {
		return nil
	}
	return originsOf(cfg.Repos)
}

func originsOf(repos []string) map[string]string {
	origins := map[string]string{}
	for _, repo := range repos {
		if url := originOf(repo); url != "" {
			origins[url] = repo
		}
	}
	return origins
}

func cloneFor(cfg *state.Config, queue node.Caller) func(context.Context, string) (string, error) {
	return func(ctx context.Context, url string) (string, error) {
		ctx, cancel := context.WithTimeout(ctx, cloneTimeout)
		defer cancel()
		clone, err := remote.EnsureClone(ctx, cfg.StateDir, url)
		if err != nil {
			return "", err
		}
		if _, err := install.Init(clone, cfg.StateDir); err != nil {
			return "", err
		}
		answer, err := queue.Call(ctx, host.Message{T: "repo", Data: mustJSON(host.RepoRequest{Repo: clone})})
		if err == nil && answer.T != "done" {
			err = errors.New(answer.Error)
		}
		return clone, err
	}
}

func nodeOS() string {
	if runtime.GOOS == "darwin" {
		return "macOS"
	}
	return runtime.GOOS
}

func readinessProbes(scope node.Scope) []node.Probe {
	o := doctorOptions()
	origins := originsOf(o.Repos)
	if scope.Repo != "" {
		origins = originsOf([]string{scope.Repo})
	}
	return install.Readiness(o, origins, scope)
}

func (n *runningNode) serve(ln net.Listener, handler http.Handler) {
	s := &http.Server{Handler: handler, ReadHeaderTimeout: readTimeout}
	n.closers = append(n.closers, s)
	go s.Serve(ln)
}

func nodeHosts(cfg *state.Config) []string {
	port := strconv.Itoa(cfg.NodePort())
	hosts := []string{"127.0.0.1:" + port, "localhost:" + port}
	if hostname := cfg.NodeHostname(); hostname != "" {
		hosts = append(hosts, hostname)
	}
	return hosts
}

func phoneApp(ctx context.Context, cfg *state.Config, api *node.API, pages *review.Server, vapidKey string) http.Handler {
	store := phone.Open(cfg.StateDir)
	pusher := &phone.Pusher{Store: store, Origin: cfg.NodeOrigin(), Client: phone.PublicClient()}
	go pusher.Run(ctx)
	server := &phone.Server{Store: store, Factories: api.Factories, Connected: api.Connected, Review: pages.MobileHandler(), Push: pusher, VAPIDKey: vapidKey, Shell: phone.Shell(bundle.Files)}
	go server.Poll(ctx)
	return server.Handler()
}

func adoptSession(cfg *state.Config, tm tmux.Client) error {
	if tm.HasSession(cfg.Hub.Session) {
		return nil
	}
	args := []string{"new-session", "-d", "-s", cfg.Hub.Session, "-n", hub.HubWindow, "-x", "200", "-y", "50"}
	for _, kv := range passedEnvironment() {
		args = append(args, "-e", kv)
	}
	_, err := tm.Run(append(args, loginShell())...)
	return err
}

func hostLockPath(cfg *state.Config) string { return filepath.Join(cfg.StateDir, "host.lock") }

func startReview(cfg *state.Config) (*review.Server, error) {
	token, err := review.Token(cfg.StateDir)
	if err != nil {
		return nil, err
	}
	overlay, err := bundle.Files.ReadFile("review/overlay.js")
	if err != nil {
		return nil, err
	}
	return review.New(token, overlay)
}

func localNode(cfg *state.Config) (*node.Client, error) {
	if !node.Answers(cfg.NodeSocket()) {
		return nil, errNoNode
	}
	return node.Unix(cfg.NodeSocket()), nil
}

func runNodeList(args []string) int {
	cfg, err := state.LoadConfig()
	var c *node.Client
	if err == nil {
		c, err = localNode(cfg)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "factory node:", err)
		return 1
	}
	ctx := context.Background()
	switch {
	case args[0] == "list" && len(args) == 1:
		err = listNodes(ctx, c, os.Stdout)
	case args[0] == "add" && len(args) == 2:
		err = addNode(ctx, c, args[1], os.Stdout)
	case args[0] == "remove" && len(args) == 2:
		err = removeNode(ctx, c, args[1], os.Stdout)
	default:
		fmt.Fprintln(os.Stderr, nodeUsage)
		return 2
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "factory node:", err)
		return 1
	}
	return 0
}

func listNodes(ctx context.Context, c *node.Client, out io.Writer) error {
	var roster node.Roster
	if err := c.Do(ctx, http.MethodGet, "/api/nodes", nil, &roster); err != nil {
		return err
	}
	printRoster(out, roster)
	return nil
}

func printRoster(out io.Writer, r node.Roster) {
	if r.Removed {
		fmt.Fprintln(out, "This node was removed from its node list, so it lists only itself. Another node's hub can add it again.")
	}
	for _, m := range r.Members {
		detail := m.OS
		if m.Self {
			detail = "this machine · " + m.OS
		}
		if m.Pending > 0 {
			detail += fmt.Sprintf(" · %s not confirmed by it yet", count(m.Pending, "change"))
		}
		fmt.Fprintf(out, "  %-14s %-34s %s\n", shortName(m.Name), m.Name, detail)
	}
	for _, m := range r.Leaving {
		fmt.Fprintf(out, "  %-14s %-34s removed; it learns that when it answers\n", shortName(m.Name), m.Name)
	}
	if r.Home != "" {
		fmt.Fprintf(out, "\nphone app  %s\n", r.Home)
	}
}

func shortName(name string) string {
	short, _, _ := strings.Cut(name, ".")
	return short
}

func addNode(ctx context.Context, c *node.Client, name string, out io.Writer) error {
	var result node.AddResult
	err := c.Do(ctx, http.MethodPost, "/api/nodes", node.NodeRequest{Operation: uuid.NewString(), Name: name}, &result)
	if target := result.Report.Target.Short; target != "" {
		fmt.Fprintf(out, "factory node add · %s\n\n", target)
	}
	printChecks(out, result.Report.Checks)
	if err != nil {
		return fmt.Errorf("%w. Nothing was changed", err)
	}
	fmt.Fprintf(out, "\n%s is in the node list; every node, hub and phone lists it.\n", shortName(result.Member.Name))
	if problems := result.Report.Problems; problems > 0 {
		fmt.Fprintf(out, "%s. %s is a node now, but factories won't start there until they pass.\n", count(problems, "problem"), shortName(result.Member.Name))
	}
	return nil
}

func printChecks(out io.Writer, checks []node.Check) {
	marks := map[string]install.Mark{node.MarkOK: install.Pass, node.MarkWarn: install.Warn, node.MarkFail: install.Fail, node.MarkSkip: install.Skip}
	for _, c := range checks {
		fix := c.Fix
		if fix != "" && c.On != "" {
			fix = "on " + c.On + "  " + fix
		}
		install.WriteCheck(out, install.Check{Name: c.Name, Mark: marks[c.Mark], Detail: c.Detail, Lines: c.Lines, Fix: fix})
	}
}

func removeNode(ctx context.Context, c *node.Client, name string, out io.Writer) error {
	var roster node.Roster
	if err := c.Do(ctx, http.MethodGet, "/api/nodes", nil, &roster); err != nil {
		return err
	}
	i := slices.IndexFunc(roster.Members, func(m node.RosterMember) bool { return m.ID == name || m.Name == name || shortName(m.Name) == name })
	if i < 0 {
		return fmt.Errorf("no node %s in the list; factory node list shows them", name)
	}
	m := roster.Members[i]
	if err := c.Do(ctx, http.MethodPost, "/api/nodes/"+m.ID+"/remove", node.RemoveRequest{Operation: uuid.NewString()}, &roster); err != nil {
		return err
	}
	fmt.Fprintf(out, "removed  %s from the node list. Its factories keep running there; it stops notifying your phone once it learns this.\n", shortName(m.Name))
	return nil
}
