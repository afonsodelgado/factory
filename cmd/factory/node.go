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
	"slices"
	"strconv"
	"syscall"
	"time"

	"github.com/google/uuid"

	"github.com/afonsodelgado/factory/bundle"
	"github.com/afonsodelgado/factory/internal/host"
	"github.com/afonsodelgado/factory/internal/hub"
	"github.com/afonsodelgado/factory/internal/node"
	"github.com/afonsodelgado/factory/internal/phone"
	"github.com/afonsodelgado/factory/internal/remote"
	"github.com/afonsodelgado/factory/internal/review"
	"github.com/afonsodelgado/factory/internal/state"
	"github.com/afonsodelgado/factory/internal/tmux"
)

const (
	nodeUsage   = "usage: factory node run [--owner <tailscale login>] | install | uninstall | status"
	ownerEnv    = "FACTORY_NODE_OWNER"
	readTimeout = 10 * time.Second
)

var errNoNode = errors.New("no factory node runs on this machine. Start one in the foreground with factory node run, or install it as a service with factory node install")

func runNode(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, nodeUsage)
		return 2
	}
	if args[0] != "run" {
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
	if owner != "" && cfg.NodeHostname() != "" {
		return owner, nil
	}
	self, err := ts.Status(ctx)
	switch {
	case err != nil && owner == "":
		return "", fmt.Errorf("this node does not know its owner's Tailscale login, and Tailscale cannot say: %w. Then run factory node run again, or give the login with --owner you@example.com, %s or node.owner in %s", err, ownerEnv, cfg.ConfigFile())
	case err != nil:
		return owner, nil
	case self.Tagged && owner == "":
		return "", errors.New("this machine is a tagged Tailscale device, so no user owns it and Serve passes no login to check. Run " + node.UntagFix + " and sign in as yourself, then factory node run again")
	}
	learned := state.Node{Hostname: self.DNSName}
	if owner == "" {
		learned.Owner = self.Login
	}
	err = state.UpdateMachine(cfg.ConfigDir, func(m *state.Machine) error {
		m.Node.Owner = cmp.Or(m.Node.Owner, learned.Owner)
		m.Node.Hostname = cmp.Or(m.NodeHostname(), learned.Hostname)
		return nil
	})
	if err != nil {
		return "", err
	}
	cfg.Node.Owner = cmp.Or(cfg.Node.Owner, learned.Owner)
	cfg.Node.Hostname = cmp.Or(cfg.NodeHostname(), learned.Hostname)
	return cmp.Or(owner, learned.Owner), nil
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
		Jobs: &node.Jobs{Queue: queue, Ctx: ctx}, Browsers: browsers, GitHub: &host.GitHub{Run: host.RunGH},
	})
	go runtime.Push.Run(ctx)
	go func() { n.stopped <- runtime.Run(ctx, queue.Events()) }()
	api := &node.API{Commands: node.Commands{Runtime: queue}, Version: version, Node: id, Boot: uuid.NewString()}
	go api.Poll(ctx)
	app := phoneApp(ctx, cfg, api, pages, vapidKey)
	exposure := &node.Exposure{Tailscale: ts}
	go exposure.Watch(ctx)
	gate := node.Gate{Owner: owner, Hosts: nodeHosts(cfg), Origin: cfg.NodeOrigin(), OpaquePath: phone.GrantPrefix, RefusalPage: phone.RefusalPage, Exposure: exposure}
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
