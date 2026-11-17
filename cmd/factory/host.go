package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"syscall"

	"github.com/google/uuid"

	"github.com/afonsodelgado/factory/internal/host"
	"github.com/afonsodelgado/factory/internal/state"
	"github.com/afonsodelgado/factory/internal/tmux"
)

func runHost(args []string) int {
	if len(args) == 2 && args[0] == "attach" {
		err := attachView(args[1])
		fmt.Fprintln(os.Stderr, "factory host attach:", err)
		return 1
	}
	if len(args) > 0 && args[0] == "run" {
		fmt.Fprintln(os.Stderr, "factory host: host run is gone; the node runs this machine's factories. Start it with factory node run")
		return 1
	}
	fmt.Fprintln(os.Stderr, "usage: factory host attach <view>")
	return 2
}

func attachView(id string) error {
	if _, err := uuid.Parse(id); err != nil {
		return errors.New("a view id is a UUID from the node's attach preparation")
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
