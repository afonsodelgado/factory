package main

import (
	"bufio"
	"context"
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

	"github.com/afonsodelgado/factory/bundle"
	"github.com/afonsodelgado/factory/internal/catalog"
	"github.com/afonsodelgado/factory/internal/factory"
	"github.com/afonsodelgado/factory/internal/host"
	"github.com/afonsodelgado/factory/internal/hub"
	"github.com/afonsodelgado/factory/internal/install"
	"github.com/afonsodelgado/factory/internal/state"
	"github.com/afonsodelgado/factory/internal/tmux"
	"github.com/google/uuid"
)

var passedEnv = []string{"FACTORY_CONFIG_DIR", "FACTORY_STATE_DIR", "FACTORY_TMUX_SOCKET", "CLAUDE_CONFIG_DIR"}

func runOpen(args []string) int {
	flags := flag.NewFlagSet("open", flag.ContinueOnError)
	repo := flags.String("repo", "", "repo to create the factory in")
	prompt := flags.String("prompt", "", "what the factory should do; written to brief.md")
	pipeline := flags.String("pipeline", "", "the pipeline of stages to run; default_pipeline when omitted")
	stages := flags.String("stages", "", "the exact stages to run, comma-separated; instead of --pipeline")
	agents := agentFlags{}
	flags.Var(agents, "agent", "role=model:effort or role=tool/model:effort for one agent of this factory; repeatable")
	supervisorFlag := flags.String("supervisor", "", "tool/model:effort the supervisor runs on, such as codex/gpt-6.1-sol:high")
	if err := flags.Parse(reorder(flags, args)); err != nil {
		return 2
	}
	if *pipeline != "" && *stages != "" {
		fmt.Fprintln(os.Stderr, "factory open: give --pipeline or --stages, not both")
		return 2
	}
	id, rest := "", flags.Args()
	if len(rest) > 0 {
		id, rest = rest[0], rest[1:]
	}
	if *prompt == "" {
		*prompt = strings.Join(rest, " ")
	}
	supervisor := state.ParseSetting(*supervisorFlag)
	if err := state.ValidateSupervisor("--supervisor", supervisor); err != nil {
		fmt.Fprintln(os.Stderr, "factory open:", err)
		return 2
	}
	if err := catalogRefusal(agents, supervisor); err != nil {
		fmt.Fprintln(os.Stderr, "factory open:", err)
		return 2
	}
	request := hub.OpenRequest{Name: factory.Slugify(id), Repo: *repo, Prompt: *prompt, Pipeline: *pipeline, Stages: splitStages(*stages), Agents: agents, Supervisor: supervisor}
	if err := openMain(request); err != nil {
		fmt.Fprintln(os.Stderr, "factory open:", err)
		return 1
	}
	return 0
}

func catalogRefusal(agents map[string]state.AgentSetting, supervisor state.AgentSetting) error {
	catalogs := catalog.Local(state.StateDir())
	if err := catalog.Refusal(catalogs.ValidateAgents("--agent", agents)); err != nil {
		return err
	}
	return catalog.Refusal(catalogs.Validate("--supervisor", "supervisor", supervisor))
}

type agentFlags map[string]state.AgentSetting

func (a agentFlags) String() string { return "" }

func (a agentFlags) Set(flag string) error {
	role, setting, err := state.ParseAgentFlag(flag)
	if err != nil {
		return err
	}
	a[role] = setting
	return nil
}

func splitStages(list string) []string {
	var stages []string
	for _, stage := range strings.Split(list, ",") {
		if stage = strings.TrimSpace(stage); stage != "" {
			stages = append(stages, stage)
		}
	}
	return stages
}

func reorder(flags *flag.FlagSet, args []string) []string {
	var named, positional []string
	for i := 0; i < len(args); i++ {
		switch {
		case takesValue(flags, args[i]) && i+1 < len(args):
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

func takesValue(flags *flag.FlagSet, arg string) bool {
	name, isFlag := strings.CutPrefix(arg, "-")
	name = strings.TrimPrefix(name, "-")
	if !isFlag || strings.Contains(name, "=") {
		return false
	}
	f := flags.Lookup(name)
	if f == nil {
		return false
	}
	boolean, ok := f.Value.(interface{ IsBoolFlag() bool })
	return !ok || !boolean.IsBoolFlag()
}

func openMain(r hub.OpenRequest) error {
	cfg, err := state.LoadConfig()
	if err != nil {
		return err
	}
	runtime, err := localNode(cfg)
	if err != nil {
		return err
	}
	name, named := r.Name, r.Repo != ""
	target := "."
	if named {
		target = state.ExpandHome(r.Repo)
	}
	setUp, notice, err := setUpRepo(target, named)
	if err != nil {
		if named {
			return err
		}
		fmt.Fprintf(os.Stderr, "factory: could not set up %s: %v\n", install.HomeRelative(setUp), err)
	}
	if cfg, err = state.LoadConfig(); err != nil {
		return err
	}
	here, listed := "", false
	if setUp != "" {
		here, listed = configuredRepo(cfg.Repos, setUp)
	}
	if named && !listed {
		return fmt.Errorf("could not add %s to the hub's repos", r.Repo)
	}
	repo := here
	if !named {
		repo = repoOf(cfg, name, here)
	}
	ctx := context.Background()
	if err := runtime.Request(ctx, "repo", "", hub.RepoRequest{Repo: here}, nil); err != nil {
		return err
	}
	show := hub.ShowRequest{Notice: notice}
	if !named {
		show.Here = here
	}
	if name != "" {
		open := hub.OpenRequest{Name: name, Repo: repo, Prompt: r.Prompt, Pipeline: r.Pipeline, Stages: r.Stages, Agents: r.Agents, Supervisor: r.Supervisor}
		if err := runtime.Request(ctx, "open", uuid.NewString(), open, &show.Select); err != nil {
			return err
		}
	}
	tm, err := factoryTmux(cfg)
	if err != nil {
		return err
	}
	bin, err := os.Executable()
	if err != nil {
		return err
	}
	if err := ensureHub(cfg, tm, bin); err != nil {
		return err
	}
	reply, err := request(cfg.SocketPath(), "show", show)
	if err != nil {
		return err
	}
	if reply.T == "error" {
		return errors.New(reply.Error)
	}
	if notice != "" {
		fmt.Println(notice)
	}
	return attach(cfg, tm)
}

func factoryTmux(cfg *state.Config) (tmux.Client, error) {
	conf, err := bundle.Files.ReadFile("tmux.conf")
	if err != nil {
		return tmux.Client{}, err
	}
	if err := state.WriteFileAtomic(cfg.TmuxConfPath(), conf); err != nil {
		return tmux.Client{}, err
	}
	return tmux.Client{Socket: cfg.Tmux.Socket, ConfigFile: cfg.TmuxConfPath()}, nil
}

func insideFactoryServer(tm tmux.Client) bool {
	client, _, _ := strings.Cut(os.Getenv("TMUX"), ",")
	if client == "" {
		return false
	}
	socket, err := tm.Run("display-message", "-p", "#{socket_path}")
	return err == nil && socket == client
}

func passedEnvironment() []string {
	var env []string
	for _, name := range passedEnv {
		if value := os.Getenv(name); value != "" {
			env = append(env, name+"="+value)
		}
	}
	return env
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

func repoOf(cfg *state.Config, name, here string) string {
	id := factory.Slugify(name)
	for _, repo := range cfg.Repos {
		if info, err := os.Stat(filepath.Join(factory.WorktreesDir(repo), id)); id != "" && err == nil && info.IsDir() {
			return repo
		}
	}
	return here
}

func cwdRepo(cfg *state.Config) string {
	cwd, _ := os.Getwd()
	for _, repo := range cfg.Repos {
		if cwd == repo || strings.HasPrefix(cwd, repo+"/") {
			return repo
		}
	}
	return ""
}

func hubOpen(cfg *state.Config) bool { return fileLocked(cfg.HubViewLock()) }

func fileLocked(path string) bool {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
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
	if hubOpen(cfg) {
		return nil
	}
	return startHub(cfg, tm, bin)
}

func startHub(cfg *state.Config, tm tmux.Client, bin string) error {
	hubCommand := shellQuote(bin) + " hub; exec " + loginShell()
	if !tm.HasSession(cfg.Hub.Session) {
		args := []string{"new-session", "-d", "-s", cfg.Hub.Session, "-n", hub.HubWindow, "-x", "200", "-y", "50"}
		for _, kv := range passedEnvironment() {
			args = append(args, "-e", kv)
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

func request(socket, kind string, payload any) (hub.Message, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return hub.Message{}, err
	}
	return send(socket, hub.Message{T: kind, Pane: "cli", Data: data})
}

func send(socket string, m hub.Message) (hub.Message, error) {
	var conn net.Conn
	var err error
	deadline := time.Now().Add(15 * time.Second)
	for {
		conn, err = net.Dial("unix", socket)
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			return hub.Message{}, fmt.Errorf("the hub did not start: %w", err)
		}
		time.Sleep(200 * time.Millisecond)
	}
	defer conn.Close()
	if err := json.NewEncoder(conn).Encode(m); err != nil {
		return hub.Message{}, err
	}
	conn.SetReadDeadline(time.Now().Add(20 * time.Second))
	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 64*1024), host.MaxMessage)
	for scanner.Scan() {
		var reply hub.Message
		if json.Unmarshal(scanner.Bytes(), &reply) == nil && (reply.T == "done" || reply.T == "error" || reply.T == "uncertain") {
			return reply, nil
		}
	}
	return hub.Message{}, errors.New("the hub did not answer")
}

func attach(cfg *state.Config, tm tmux.Client) error {
	target := hubPane(cfg, tm)
	if insideFactoryServer(tm) {
		if _, err := tm.Run("switch-client", "-t", target); err == nil {
			return nil
		}
	}
	path, err := exec.LookPath("tmux")
	if err != nil {
		return err
	}
	argv := append([]string{"tmux"}, tm.Command("attach-session", "-t", target).Args[1:]...)
	var env []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "TMUX=") {
			env = append(env, kv)
		}
	}
	return syscall.Exec(path, argv, env)
}

func hubPane(cfg *state.Config, tm tmux.Client) string {
	panes, _ := tm.Panes()
	for _, p := range panes {
		if p.UI == "selector" {
			return p.ID
		}
	}
	return "=" + cfg.Hub.Session
}
