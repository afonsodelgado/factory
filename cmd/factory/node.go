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
	nodeUsage   = "usage: factory node run [--owner <tailscale login>]"
	ownerEnv    = "FACTORY_NODE_OWNER"
	readTimeout = 10 * time.Second
)

var errNoNode = errors.New("no factory node runs on this machine. Start one in the foreground with factory node run, or install it as a service with factory node install")

func runNode(args []string) int {
	if len(args) == 0 || args[0] != "run" {
		fmt.Fprintln(os.Stderr, nodeUsage)
		return 2
	}
	flags := flag.NewFlagSet("node run", flag.ContinueOnError)
	owner := flags.String("owner", "", "the Tailscale login that owns this node, such as you@example.com")
	if err := flags.Parse(args[1:]); err != nil || flags.NArg() > 0 {
		fmt.Fprintln(os.Stderr, nodeUsage)
		return 2
	}
	cfg, err := state.LoadConfig()
	if err == nil {
		err = runNodeService(cfg, cmp.Or(*owner, os.Getenv(ownerEnv), cfg.Node.Owner))
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "factory node:", err)
		return 1
	}
	return 0
}

func runNodeService(cfg *state.Config, owner string) error {
	if owner == "" {
		return errors.New("this node does not know its owner's Tailscale login. Give it with factory node run --owner you@example.com, " + ownerEnv + ", or node.owner in " + cfg.ConfigFile())
	}
	release, err := node.Lock(cfg.NodeLockPath(), cfg.LockPath(), hostLockPath(cfg), phone.ServeLockPath(cfg.StateDir))
	if err != nil {
		return err
	}
	defer release()
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer cancel()
	n, err := startNode(ctx, cfg, owner)
	if err != nil {
		return err
	}
	defer n.close()
	fmt.Printf("factory node: %s on %s and 127.0.0.1:%d\n", owner, cfg.NodeSocket(), cfg.NodePort())
	return n.wait()
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

func startNode(ctx context.Context, cfg *state.Config, owner string) (*runningNode, error) {
	if err := os.MkdirAll(cfg.SessionsDir(), 0o700); err != nil {
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
	n := &runningNode{closers: []io.Closer{pages}, stopped: make(chan error, 1)}
	queue := node.NewQueue()
	pages.SetActions(host.ReviewActions(queue.Sink()))
	runtime := host.New(host.Runtime{
		Config: cfg, Loader: &host.Loader{Config: cfg, Registry: registry, TM: &tm, Now: time.Now}, TM: tm, Session: cfg.Hub.Session,
		Pages: pages, Engine: panes, Typist: panes, Log: host.LoadEventLog(host.EventLogPath(cfg.StateDir)), Push: &host.Pusher{StateDir: cfg.StateDir, Now: time.Now},
	})
	go runtime.Push.Run(ctx)
	go func() { n.stopped <- runtime.Run(ctx, queue.Events()) }()
	api := &node.API{Commands: node.Commands{Runtime: queue}, Version: version, Node: id, Boot: uuid.NewString()}
	go api.Poll(ctx)
	gate := node.Gate{Owner: owner, Hosts: nodeHosts(cfg), Origin: cfg.Phone.Origin(), OpaquePath: phone.GrantPrefix}
	unixListener, err := node.ListenUnix(cfg.NodeSocket())
	if err != nil {
		n.close()
		return nil, err
	}
	n.serve(unixListener, api.Handler(gate.Principal))
	tcp, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(cfg.NodePort())))
	if err != nil {
		n.close()
		return nil, fmt.Errorf("port %d is taken; free it or pick another with node.port in %s: %w", cfg.NodePort(), cfg.ConfigFile(), err)
	}
	mux := http.NewServeMux()
	api.Register(mux, gate.Principal, node.NetworkKinds...)
	mux.Handle("/", phoneApp(ctx, cfg, queue, pages, n))
	n.serve(tcp, gate.Wrap(mux))
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
	if cfg.Phone.Hostname != "" {
		hosts = append(hosts, cfg.Phone.Hostname)
	}
	return hosts
}

func phoneApp(ctx context.Context, cfg *state.Config, owner phone.Owner, pages *review.Server, n *runningNode) http.Handler {
	settings := cfg.Phone
	if !settings.Enabled {
		return http.NotFoundHandler()
	}
	store := phone.Open(cfg.StateDir)
	pusher := &phone.Pusher{Store: store, Origin: settings.Origin(), Client: phone.PublicClient()}
	go pusher.Run(ctx)
	server := &phone.Server{Store: store, Owner: owner, Review: pages.MobileHandler(), Push: pusher, VAPIDKey: settings.VAPIDPublicKey, Shell: phone.Shell(bundle.Files), Host: settings.Hostname, Version: version}
	go server.Poll(ctx)
	if control, err := host.Listen(phone.ControlPath(cfg.StateDir), phone.Control{Config: cfg, Server: server, Version: version, Stop: func() {}}.Handle); err == nil {
		os.Chmod(phone.ControlPath(cfg.StateDir), 0o600)
		n.closers = append(n.closers, control)
	}
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
	return review.Listen(cfg.Review.Port, token, overlay)
}

func localNode(cfg *state.Config) (*node.Client, error) {
	if !node.Answers(cfg.NodeSocket()) {
		return nil, errNoNode
	}
	return node.Unix(cfg.NodeSocket()), nil
}
