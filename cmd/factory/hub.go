package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/afonsodelgado/factory/bundle"
	"github.com/afonsodelgado/factory/internal/hub"
	"github.com/afonsodelgado/factory/internal/phone"
	"github.com/afonsodelgado/factory/internal/remote"
	"github.com/afonsodelgado/factory/internal/review"
	"github.com/afonsodelgado/factory/internal/state"
	"github.com/afonsodelgado/factory/internal/tmux"
	"github.com/afonsodelgado/factory/internal/ui/theme"
)

func runHub(args []string) int {
	if err := hubMain(nil, true); err != nil {
		fmt.Fprintln(os.Stderr, "factory hub:", err)
		return 1
	}
	return 0
}

func hubMain(startup []hub.Event, detachOnQuit bool) error {
	cfg, err := state.LoadConfig()
	if err != nil {
		return err
	}
	self := os.Getenv("TMUX_PANE")
	if self == "" {
		return errors.New("not inside tmux; start the hub with factory open")
	}
	if err := os.MkdirAll(cfg.StateDir, 0o755); err != nil {
		return err
	}
	release, err := phone.Claim(cfg, phone.ModeHub, version)
	if err != nil {
		return err
	}
	defer release()

	tm := tmux.Client{Socket: cfg.Tmux.Socket}
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
	defer func() { engine.Release(layout) }()
	if err != nil {
		return err
	}
	registry, err := state.LoadRegistry(cfg.RegistryPath())
	if err != nil {
		return err
	}
	events := make(chan hub.Event, 64)
	for _, e := range startup {
		events <- e
	}
	server, err := hub.Listen(cfg.SocketPath(), func(e hub.Event) { events <- e })
	if err != nil {
		return err
	}
	defer server.Close()
	pages, phoneProblem, err := startReview(cfg)
	if err != nil {
		return err
	}
	defer pages.Close()
	pages.SetActions(hub.ReviewActions(events))
	var chrome *review.Browser
	if cfg.Review.FactoryChrome() {
		chrome = pages.NewBrowser(filepath.Join(cfg.StateDir, "chrome"), cfg.Review.Chrome, false)
		defer chrome.Close()
	}
	restore := engine.BindReturn(cfg.Hub.ReturnKey, layout)
	defer restore()
	icons := theme.IconsFor(cfg.Icons)
	defer engine.SetHeader(hub.StoppedHeader(icons))
	if err := os.MkdirAll(cfg.SessionsDir(), 0o755); err != nil {
		return err
	}
	watch, err := hub.NewWatcher()
	if err != nil {
		return err
	}
	defer watch.Close()
	tunnels := &remote.Tunnels{StateDir: cfg.StateDir}
	defer tunnels.Close()

	model := hub.NewModel(hub.Model{
		Config:  cfg,
		Engine:  engine,
		Layout:  layout,
		Server:  server,
		Loader:  &hub.Loader{Config: cfg, Registry: registry, TM: &tm, Now: time.Now},
		Events:  events,
		Watch:   watch,
		GitHub:  &hub.GitHub{Run: hub.RunGH},
		Typist:  engine,
		Open:    reviewOpener(pages, chrome),
		Review:  pages,
		Chrome:  chrome,
		Tunnels: tunnels,
		Bell:    os.Stderr,
		Notify:  notify,
		Icons:   icons,
		Problem: phoneProblem,
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
	final, err := program.Run()
	if m, ok := final.(*hub.Model); ok {
		layout = m.Layout
	}
	if err == nil && detachOnQuit {
		tm.Run("detach-client", "-s", "="+cfg.Hub.Session)
	}
	return err
}

func reviewOpener(pages *review.Server, chrome *review.Browser) func(string) {
	return func(url string) {
		if chrome != nil && strings.HasPrefix(url, pages.Origin()+"/") && chrome.OpenPage(url) == nil {
			return
		}
		openURL(url)
	}
}

func startReview(cfg *state.Config) (pages *review.Server, phoneProblem string, err error) {
	token, err := review.Token(cfg.StateDir)
	if err != nil {
		return nil, "", err
	}
	overlay, err := bundle.Files.ReadFile("review/overlay.js")
	if err != nil {
		return nil, "", err
	}
	pages, err = review.Listen(cfg.Review.Port, token, overlay)
	if err != nil {
		return nil, "", err
	}
	if err := pages.ServeMobile(review.MobileSocket(cfg.StateDir)); err != nil {
		return pages, "The phone cannot open review pages: " + err.Error() + ". A shorter FACTORY_STATE_DIR fixes a socket path that is too long.", nil
	}
	return pages, "", nil
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
