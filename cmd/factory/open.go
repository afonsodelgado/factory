package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/afonsodelgado/factory/internal/factory"
	"github.com/afonsodelgado/factory/internal/hub"
	"github.com/afonsodelgado/factory/internal/state"
	"github.com/afonsodelgado/factory/internal/tmux"
)

var passedEnv = []string{"FACTORY_CONFIG_DIR", "FACTORY_STATE_DIR", "FACTORY_TMUX_SOCKET", "CLAUDE_CONFIG_DIR"}

func runOpen(args []string) int {
	flags := flag.NewFlagSet("open", flag.ContinueOnError)
	repo := flags.String("repo", "", "repo to create the factory in")
	prompt := flags.String("prompt", "", "what the factory should do; written to brief.md")
	if err := flags.Parse(reorder(args)); err != nil {
		return 2
	}
	id, rest := "", flags.Args()
	if len(rest) > 0 {
		id, rest = rest[0], rest[1:]
	}
	if *prompt == "" {
		*prompt = strings.Join(rest, " ")
	}
	if err := openMain(hub.OpenRequest{Name: id, Repo: *repo, Prompt: *prompt}); err != nil {
		fmt.Fprintln(os.Stderr, "factory open:", err)
		return 1
	}
	return 0
}

func reorder(args []string) []string {
	var named, positional []string
	for i := 0; i < len(args); i++ {
		switch {
		case (args[i] == "--repo" || args[i] == "--prompt") && i+1 < len(args):
			named = append(named, args[i], args[i+1])
			i++
		case strings.HasPrefix(args[i], "-"):
			named = append(named, args[i])
		default:
			positional = append(positional, args[i])
		}
	}
	return append(named, positional...)
}

func openMain(r hub.OpenRequest) error {
	name, repo := r.Name, r.Repo
	cfg, err := state.LoadConfig()
	if err != nil {
		return err
	}
	if repo != "" {
		configured, ok := configuredRepo(cfg.Repos, repo)
		if !ok {
			return fmt.Errorf("%s is not a configured repo; add it to repos in the config (configured: %s)", repo, strings.Join(cfg.Repos, ", "))
		}
		repo = configured
	} else {
		repo = repoOf(cfg, name)
	}
	tm := tmux.Client{Socket: cfg.Tmux.Socket}
	bin, err := os.Executable()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(cfg.StateDir, 0o755); err != nil {
		return err
	}
	if err := ensureHub(cfg, tm, bin); err != nil {
		return err
	}
	if name != "" {
		if err := request(cfg.SocketPath(), hub.OpenRequest{Name: name, Repo: repo, Prompt: r.Prompt}); err != nil {
			return err
		}
	}
	return attach(cfg, tm)
}

func configuredRepo(repos []string, path string) (string, bool) {
	abs, err := filepath.Abs(state.ExpandHome(path))
	if err != nil {
		return "", false
	}
	wanted, statErr := os.Stat(abs)
	for _, repo := range repos {
		if filepath.Clean(repo) == abs {
			return repo, true
		}
		if info, err := os.Stat(repo); statErr == nil && err == nil && os.SameFile(info, wanted) {
			return repo, true
		}
	}
	return "", false
}

func repoOf(cfg *state.Config, name string) string {
	id := factory.Slugify(name)
	for _, repo := range cfg.Repos {
		if info, err := os.Stat(filepath.Join(factory.WorktreesDir(repo), id)); id != "" && err == nil && info.IsDir() {
			return repo
		}
	}
	cwd, _ := os.Getwd()
	for _, repo := range cfg.Repos {
		if cwd == repo || strings.HasPrefix(cwd, repo+"/") {
			return repo
		}
	}
	return ""
}

func hubRunning(cfg *state.Config) bool {
	f, err := os.OpenFile(cfg.LockPath(), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return false
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return true
	}
	syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return false
}

func ensureHub(cfg *state.Config, tm tmux.Client, bin string) error {
	if hubRunning(cfg) {
		return nil
	}
	hubCommand := shellQuote(bin) + " hub; exec " + loginShell()
	if !tm.HasSession(cfg.Hub.Session) {
		args := []string{"new-session", "-d", "-s", cfg.Hub.Session, "-n", hub.HubWindow, "-x", "200", "-y", "50"}
		for _, name := range passedEnv {
			if value := os.Getenv(name); value != "" {
				args = append(args, "-e", name+"="+value)
			}
		}
		_, err := tm.Run(append(args, hubCommand)...)
		return err
	}
	panes, err := tm.Panes()
	if err != nil {
		return err
	}
	for _, p := range panes {
		selector := p.Session == cfg.Hub.Session && (p.UI == "selector" || (p.WindowName == hub.HubWindow && p.Slug == "" && p.UI == ""))
		if selector && factory.IsShell(p.Command) {
			if err := tm.SendKeys(p.ID, "-l", shellQuote(bin)+" hub"); err != nil {
				return err
			}
			return tm.SendKeys(p.ID, "Enter")
		}
	}
	_, err = tm.Run("new-window", "-t", cfg.Hub.Session+":", "-n", hub.HubWindow, hubCommand)
	return err
}

func loginShell() string {
	if shell := os.Getenv("SHELL"); shell != "" {
		return shell + " -l"
	}
	return "/bin/sh -l"
}

func request(socket string, r hub.OpenRequest) error {
	data, err := json.Marshal(r)
	if err != nil {
		return err
	}
	var conn net.Conn
	deadline := time.Now().Add(15 * time.Second)
	for {
		conn, err = net.Dial("unix", socket)
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("the hub did not start: %w", err)
		}
		time.Sleep(200 * time.Millisecond)
	}
	defer conn.Close()
	if err := json.NewEncoder(conn).Encode(hub.Message{T: "open", Pane: "cli", Data: data}); err != nil {
		return err
	}
	conn.SetReadDeadline(time.Now().Add(20 * time.Second))
	scanner := bufio.NewScanner(conn)
	for scanner.Scan() {
		var m hub.Message
		if json.Unmarshal(scanner.Bytes(), &m) != nil {
			continue
		}
		switch m.T {
		case "done":
			return nil
		case "error":
			return errors.New(m.Error)
		}
	}
	return errors.New("the hub did not answer")
}

func attach(cfg *state.Config, tm tmux.Client) error {
	if os.Getenv("TMUX") != "" {
		if _, err := tm.Run("switch-client", "-t", "="+cfg.Hub.Session); err == nil {
			return nil
		}
	}
	path, err := exec.LookPath("tmux")
	if err != nil {
		return err
	}
	argv := append([]string{"tmux"}, tm.Command("attach-session", "-t", "="+cfg.Hub.Session).Args[1:]...)
	var env []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "TMUX=") {
			env = append(env, kv)
		}
	}
	return syscall.Exec(path, argv, env)
}
