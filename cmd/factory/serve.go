package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/afonsodelgado/factory/internal/host"
	"github.com/afonsodelgado/factory/internal/phone"
	"github.com/afonsodelgado/factory/internal/review"
	"github.com/afonsodelgado/factory/internal/state"
	"github.com/afonsodelgado/factory/internal/tmux"
)

var tailnetRange = netip.MustParsePrefix("100.64.0.0/10")

func runServe(args []string) int {
	if len(args) > 0 {
		fmt.Fprintln(os.Stderr, "usage: factory serve")
		return 2
	}
	cfg, err := state.LoadConfig()
	if err == nil {
		err = serve(cfg)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "factory serve:", err)
		return 1
	}
	return 0
}

func serve(cfg *state.Config) error {
	settings := cfg.Phone
	if !settings.Enabled {
		return errors.New("Phone is off; turn it on with factory phone enable")
	}
	unlock, err := phone.Lock(phone.ServeLockPath(cfg.StateDir))
	if errors.Is(err, phone.ErrLocked) {
		return phone.ErrServing
	}
	if err != nil {
		return err
	}
	defer unlock()
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer cancel()
	server := &phone.Server{Store: phone.Open(cfg.StateDir), Owner: phone.SocketOwner{Socket: cfg.SocketPath()}, Review: phone.ReviewProxy(review.MobileSocket(cfg.StateDir)), Host: settings.Hostname, Version: version}
	listener, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(settings.ListenPort())))
	if err != nil {
		return fmt.Errorf("port %d is taken; free it or pick another with factory phone configure --port: %w", settings.ListenPort(), err)
	}
	backend := &http.Server{Handler: server.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go backend.Serve(listener)
	defer backend.Close()
	if help, err := helpListener(settings); err != nil {
		fmt.Fprintln(os.Stderr, "factory serve: no plain HTTP help page on the tailnet address:", err)
	} else {
		helper := &http.Server{Handler: phone.HelpHandler(settings.Origin()), ReadHeaderTimeout: 10 * time.Second}
		go helper.Serve(help)
		defer helper.Close()
	}
	control, err := host.Listen(phone.ControlPath(cfg.StateDir), phone.Control{Config: cfg, Server: server, Version: version, Stop: cancel}.Handle)
	if err != nil {
		return err
	}
	defer control.Close()
	if err := os.Chmod(phone.ControlPath(cfg.StateDir), 0o600); err != nil {
		return err
	}
	go server.Poll(ctx)
	tm := tmux.Client{Socket: cfg.Tmux.Socket}
	owned := make(chan struct{})
	go func() {
		defer close(owned)
		phone.KeepOwner(ctx, cfg, func() error { return startHost(cfg, tm, true) }, reportOnce())
	}()
	fmt.Printf("factory serve: %s on 127.0.0.1:%d\n", settings.Origin(), settings.ListenPort())
	<-ctx.Done()
	<-owned
	return phone.Release(cfg)
}

func reportOnce() func(error) {
	last := ""
	return func(err error) {
		if err.Error() != last {
			last = err.Error()
			fmt.Fprintln(os.Stderr, "factory serve: starting the factories without the hub:", err)
		}
	}
}

func helpListener(settings state.Phone) (net.Listener, error) {
	ip, err := tailscaleIP()
	if err != nil {
		return nil, err
	}
	return net.Listen("tcp", net.JoinHostPort(ip.String(), strconv.Itoa(settings.ListenPort())))
}

func tailscaleIP() (netip.Addr, error) {
	out, err := exec.Command("tailscale", "ip", "-4").Output()
	if err != nil {
		return netip.Addr{}, fmt.Errorf("tailscale ip -4: %w", err)
	}
	first, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	ip, err := netip.ParseAddr(first)
	if err != nil || !tailnetRange.Contains(ip) {
		return netip.Addr{}, fmt.Errorf("tailscale ip -4 printed %q, not a tailnet address", first)
	}
	return ip, nil
}
