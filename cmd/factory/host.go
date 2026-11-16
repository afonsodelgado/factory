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
	"github.com/afonsodelgado/factory/internal/phone"
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
		fmt.Fprintln(os.Stderr, "usage: factory host run [--local] | factory host request | factory host attach <view>")
		return 2
	}
	cfg, err := state.LoadConfig()
	if err == nil {
		err = os.MkdirAll(cfg.StateDir, 0o700)
	}
	local := slices.Contains(args[1:], "--local")
	if err == nil && slices.Contains(args[1:], "--worker") {
		err = serveHost(cfg, local)
	} else if err == nil {
		err = startHost(cfg, tmux.Client{Socket: cfg.Tmux.Socket}, local)
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

func startHost(cfg *state.Config, tm tmux.Client, local bool) error {
	if hubRunning(cfg) {
		if phone.FallbackOwns(cfg) && !local {
			return errors.New("factory serve runs this machine's factories while the hub is closed, so they cannot serve a laptop. Run factory phone stop, then factory host run")
		}
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
	if local {
		command += " --local"
	}
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

func serveHost(cfg *state.Config, local bool) error {
	unlockHost, err := lockHub(hostLockPath(cfg))
	if err != nil {
		return err
	}
	defer unlockHost()
	mode := phone.ModeRemote
	if local {
		mode = phone.ModeLocal
	}
	release, err := phone.Claim(cfg, mode, version)
	if err != nil {
		return err
	}
	defer release()
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
	tm := tmux.Client{Socket: cfg.Tmux.Socket}
	loader := &host.Loader{Config: cfg, Registry: registry, TM: &tm, Now: time.Now}
	runtime := host.Runtime{Config: cfg, Loader: loader, TM: tm, Session: cfg.Hub.Session, Pages: pages, Fallback: local}
	panes := &host.Panes{TM: tm, Session: cfg.Hub.Session}
	if !local {
		bin, err := os.Executable()
		if err != nil {
			return err
		}
		panes.Openers = filepath.Join(cfg.StateDir, "openers")
		if err := host.WriteOpeners(panes.Openers, bin); err != nil {
			return err
		}
		runtime.Push = &host.Pusher{StateDir: cfg.StateDir, Now: time.Now}
		go runtime.Push.Run(ctx)
		runtime.Log = host.LoadEventLog(host.EventLogPath(cfg.StateDir))
	}
	runtime.Engine, runtime.Typist = panes, panes
	return host.New(runtime).Run(ctx, events)
}
