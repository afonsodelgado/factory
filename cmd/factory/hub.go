package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strings"
	"syscall"

	tea "charm.land/bubbletea/v2"

	"github.com/afonsodelgado/factory/internal/hub"
	"github.com/afonsodelgado/factory/internal/node"
	"github.com/afonsodelgado/factory/internal/state"
	"github.com/afonsodelgado/factory/internal/ui/theme"
)

func runHub(args []string) int {
	if err := hubMain(); err != nil {
		fmt.Fprintln(os.Stderr, "factory hub:", err)
		return 1
	}
	return 0
}

func hubMain() error {
	cfg, err := state.LoadConfig()
	if err != nil {
		return err
	}
	runtime, err := localNode(cfg)
	if err != nil {
		return err
	}
	self := os.Getenv("TMUX_PANE")
	if self == "" {
		return errors.New("not inside tmux; start the hub with factory open")
	}
	unlock, err := lockHub(cfg.HubViewLock())
	if err != nil {
		return err
	}
	defer unlock()

	tm, err := factoryTmux(cfg)
	if err != nil {
		return err
	}
	tm.Run("source-file", cfg.TmuxConfPath())
	bin, err := os.Executable()
	if err != nil {
		return err
	}
	engine := &hub.Engine{TM: tm, Session: cfg.Hub.Session, Env: passedEnvironment(), ViewCommand: func(kind string) string {
		return shellQuote(bin) + " view " + kind + " --socket " + shellQuote(cfg.SocketPath())
	}}
	width, height, err := engine.Measure(self)
	if err != nil {
		return err
	}
	layout, err := engine.Build(self, hub.Shape{Width: width, Height: height})
	if err != nil {
		return err
	}
	events := make(chan hub.Event, 64)
	server, err := hub.Listen(cfg.SocketPath(), func(e hub.Event) { events <- e })
	if err != nil {
		return err
	}
	defer server.Close()
	restore := engine.BindReturn(cfg.Hub.ReturnKey, layout)
	defer restore()
	icons := theme.IconsFor(cfg.Icons)
	defer engine.SetHeader(hub.StoppedHeader(icons))
	watch, err := hub.NewWatcher()
	if err != nil {
		return err
	}
	defer watch.Close()

	model := hub.NewModel(hub.Model{
		Config: cfg,
		Engine: engine,
		Layout: layout,
		Server: server,
		Node:   runtime,
		Events: events,
		Watch:  watch,
		Open:   openURL,
		Roster: func(ctx context.Context) (node.Roster, error) {
			var roster node.Roster
			err := runtime.Do(ctx, http.MethodGet, "/api/nodes", nil, &roster)
			return roster, err
		},
		Bell:   os.Stderr,
		Notify: notify,
		Icons:  icons,
		DialogCommand: func(kind string) string {
			return shellQuote(bin) + " dialog " + kind + " --socket " + shellQuote(cfg.SocketPath())
		},
	})
	program := tea.NewProgram(model)
	hangup := make(chan os.Signal, 1)
	signal.Notify(hangup, syscall.SIGHUP)
	defer signal.Stop(hangup)
	go func() {
		<-hangup
		program.Quit()
	}()
	if _, err := program.Run(); err != nil {
		return err
	}
	tm.Run("detach-client", "-s", "="+cfg.Hub.Session)
	return nil
}

func lockHub(path string) (func(), error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, errors.New("another factory hub is already running")
	}
	return func() { f.Close() }, nil
}

func runView(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: factory view <panel|footer|slot|frame-top|frame-bottom|frame-left|frame-right|frame-split> [--socket path]")
		return 2
	}
	flags := flag.NewFlagSet("view", flag.ContinueOnError)
	socket := flags.String("socket", state.StateDir()+"/hub.sock", "hub socket")
	if err := flags.Parse(args[1:]); err != nil {
		return 2
	}
	viewer := &hub.Viewer{Kind: args[0], Socket: *socket}
	if _, err := tea.NewProgram(viewer).Run(); err != nil {
		fmt.Fprintln(os.Stderr, "factory view:", err)
		return 1
	}
	return 0
}

func openURL(url string) {
	opener := "xdg-open"
	if runtime.GOOS == "darwin" {
		opener = "open"
	}
	exec.Command(opener, url).Run()
}

func notify(text string) {
	if runtime.GOOS != "darwin" {
		exec.Command("notify-send", "factory", text).Run()
		return
	}
	script := fmt.Sprintf("display notification %q with title %q", text, "factory")
	exec.Command("osascript", "-e", script).Run()
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
