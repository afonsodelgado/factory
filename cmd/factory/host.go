package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"

	"github.com/afonsodelgado/factory/internal/host"
	"github.com/afonsodelgado/factory/internal/state"
	"github.com/afonsodelgado/factory/internal/tmux"
)

const hostWindow = "host"

func runHost(args []string) int {
	if len(args) == 1 && args[0] == "request" {
		json.NewEncoder(os.Stdout).Encode(answerRequest(os.Stdin))
		return 0
	}
	if len(args) == 2 && args[0] == "attach" {
		err := attachView(args[1])
		fmt.Fprintln(os.Stderr, "factory host attach:", err)
		return 1
	}
	if len(args) == 0 || args[0] != "run" {
		fmt.Fprintln(os.Stderr, "usage: factory host run | factory host request | factory host attach <view>")
		return 2
	}
	cfg, err := state.LoadConfig()
	if err == nil {
		err = os.MkdirAll(cfg.StateDir, 0o700)
	}
	if err == nil && len(args) == 2 && args[1] == "--worker" {
		err = serveHost(cfg)
	} else if err == nil {
		err = startHost(cfg, tmux.Client{Socket: cfg.Tmux.Socket})
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "factory host:", err)
		return 1
	}
	return 0
}

func hostLockPath(cfg *state.Config) string { return filepath.Join(cfg.StateDir, "host.lock") }

func attachView(id string) error {
	if _, err := uuid.Parse(id); err != nil {
		return errors.New("a view id is a UUID from factory host request")
	}
	cfg, err := state.LoadConfig()
	if err != nil {
		return err
	}
	cmd := tmux.Client{Socket: cfg.Tmux.Socket}.Command("attach-session", "-t", "="+host.ViewSession(id))
	path, err := exec.LookPath(cmd.Path)
	if err != nil {
		return err
	}
	env := slices.DeleteFunc(os.Environ(), func(kv string) bool { return strings.HasPrefix(kv, "TMUX=") })
	return syscall.Exec(path, cmd.Args, env)
}

func startHost(cfg *state.Config, tm tmux.Client) error {
	if hubRunning(cfg) {
		if !fileLocked(hostLockPath(cfg)) {
			return errors.New("the interactive factory hub is open on this machine and holds its runtime, so it cannot serve a laptop. Close it, then run factory host run")
		}
		return nil
	}
	bin, err := os.Executable()
	if err != nil {
		return err
	}
	command := shellQuote(bin) + " host run --worker"
	if tm.HasSession(cfg.Hub.Session) {
		_, err = tm.NewWindow(tmux.Window{Session: cfg.Hub.Session, Name: hostWindow, Env: passedEnvironment(), Command: command})
	} else {
		args := []string{"new-session", "-d", "-s", cfg.Hub.Session, "-n", hostWindow}
		for _, kv := range passedEnvironment() {
			args = append(args, "-e", kv)
		}
		_, err = tm.Run(append(args, command)...)
	}
	if err != nil {
		return err
	}
	for range 100 {
		if hubRunning(cfg) {
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return errors.New("the host did not start; read the " + hostWindow + " window in tmux session " + cfg.Hub.Session)
}

func serveHost(cfg *state.Config) error {
	unlockHost, err := lockHub(hostLockPath(cfg))
	if err != nil {
		return err
	}
	defer unlockHost()
	unlock, err := lockHub(cfg.LockPath())
	if err != nil {
		return err
	}
	defer unlock()
	registry, err := state.LoadRegistry(cfg.RegistryPath())
	if err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer cancel()
	events := make(chan host.Event, 64)
	server, err := host.Listen(cfg.SocketPath(), func(e host.Event) {
		select {
		case events <- e:
		case <-ctx.Done():
		}
	})
	if err != nil {
		return err
	}
	defer server.Close()
	pages, err := startReview(cfg)
	if err != nil {
		return err
	}
	defer pages.Close()
	pages.SetActions(host.ReviewActions(events))
	bin, err := os.Executable()
	if err != nil {
		return err
	}
	openers := filepath.Join(cfg.StateDir, "openers")
	if err := host.WriteOpeners(openers, bin); err != nil {
		return err
	}
	tm := tmux.Client{Socket: cfg.Tmux.Socket}
	panes := &host.Panes{TM: tm, Session: cfg.Hub.Session, Openers: openers}
	loader := &host.Loader{Config: cfg, Registry: registry, TM: &tm, Now: time.Now}
	pusher := &host.Pusher{StateDir: cfg.StateDir, Now: time.Now}
	go pusher.Run(ctx)
	journal := host.LoadEventLog(host.EventLogPath(cfg.StateDir))
	runtime := host.New(host.Runtime{Config: cfg, Loader: loader, Engine: panes, TM: tm, Session: cfg.Hub.Session, Typist: panes, Pages: pages, Log: journal, Push: pusher})
	return runtime.Run(ctx, events)
}
