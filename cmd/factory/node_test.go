package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/afonsodelgado/factory/internal/factory"
	"github.com/afonsodelgado/factory/internal/host"
	"github.com/afonsodelgado/factory/internal/node"
	"github.com/afonsodelgado/factory/internal/state"
)

type standIn struct {
	root, bin, server, tmux string
	env                     []string
}

const (
	claudeVersion = "#!/bin/sh\n[ \"${1:-}\" = --version ] && { echo '2.1.400 (Claude Code)'; exit 0; }\n"
	runnableGH    = "#!/bin/sh\ncase \"$*\" in\n--version) echo 'gh version 2.45.0' ;;\n'auth status') echo 'Logged in to github.com account stand-in' ;;\n'extension list') printf 'gh stack\\tgithub/gh-stack\\tv0.4.0\\n' ;;\n*) exit 1 ;;\nesac\n"
)

func newStandIn(t *testing.T) standIn {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		if os.Getenv("CI") == "" {
			t.Skip("tmux not installed")
		}
		t.Fatal(err)
	}
	root, err := os.MkdirTemp("/tmp", "frc-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	s := standIn{root: root, bin: filepath.Join(root, "bin"), server: filepath.Join(root, "server"), tmux: "frc-" + uuid.NewString()}
	if out, err := exec.Command("go", "build", "-o", filepath.Join(s.bin, "factory"), ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	for name, script := range map[string]string{
		"claude":    claudeVersion + "exec sleep 600\n",
		"gh":        runnableGH,
		"tailscale": "#!/bin/sh\necho 'stand-in tailscale: not running' >&2\nexit 1\n",
		"shell":     "#!/bin/sh\n[ \"${1:-}\" = -l ] && shift\nexec /bin/sh \"$@\"\n",
	} {
		if err := os.WriteFile(filepath.Join(s.bin, name), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	client := filepath.Join(root, "client")
	s.env = append(os.Environ(),
		"PATH="+s.bin+string(os.PathListSeparator)+os.Getenv("PATH"),
		"SHELL="+filepath.Join(s.bin, "shell"),
		"ZDOTDIR=",
		"ENV=",
		"CLAUDE_ENV_FILE=",
		"HOME="+filepath.Join(client, "home"),
		"FACTORY_CONFIG_DIR="+filepath.Join(client, "config"),
		"FACTORY_STATE_DIR="+filepath.Join(client, "state"),
		"CLAUDE_CONFIG_DIR="+filepath.Join(client, "claude"),
		"FACTORY_TMUX_SOCKET=frc-client-unused",
		"TMUX=",
	)
	server := exec.Command("tmux", "-L", s.tmux, "-f", "/dev/null", "new-session", "-d", "-s", "scratch", "exec sleep 600")
	server.Env = s.env
	if out, err := server.CombinedOutput(); err != nil {
		t.Fatalf("tmux: %v\n%s", err, out)
	}
	t.Cleanup(func() { exec.Command("tmux", "-L", s.tmux, "kill-server").Run() })
	writeFile(t, filepath.Join(s.server, "config", "config.yml"), fmt.Sprintf("node:\n  port: %d\n  owner: owner@example.com\n", freePort(t)))
	s.serverNode(t)
	return s
}

func (s standIn) onServer() []string {
	return []string{"HOME=" + filepath.Join(s.server, "home"), "FACTORY_CONFIG_DIR=" + filepath.Join(s.server, "config"), "FACTORY_STATE_DIR=" + filepath.Join(s.server, "state"),
		"CLAUDE_CONFIG_DIR=" + filepath.Join(s.server, "claude"), "FACTORY_TMUX_SOCKET=" + s.tmux}
}

func (s standIn) serverNode(t *testing.T) {
	t.Helper()
	writeFile(t, filepath.Join(s.server, "claude", ".credentials.json"), "{}")
	process := exec.Command(filepath.Join(s.bin, "factory"), "node", "run")
	process.Env = append(slices.Clone(s.env), s.onServer()...)
	var log strings.Builder
	process.Stdout, process.Stderr = &log, &log
	if err := process.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- process.Wait() }()
	t.Cleanup(func() {
		process.Process.Signal(os.Interrupt)
		<-exited
	})
	nodeSocket := filepath.Join(s.server, "state", "node.sock")
	waitFor(t, "the server's node to answer on "+nodeSocket, func() bool {
		select {
		case err := <-exited:
			t.Fatalf("the server's node exited: %v\n%s", err, log.String())
		default:
		}
		return node.Answers(nodeSocket)
	})
}

func (s standIn) serverTmux(t *testing.T, args ...string) string {
	t.Helper()
	out, err := exec.Command("tmux", append([]string{"-L", s.tmux}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("tmux %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func (s standIn) factory(t *testing.T, dir string, extra []string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command(filepath.Join(s.bin, "factory"), args...)
	cmd.Dir, cmd.Env = dir, append(s.env, extra...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func (s standIn) startFactory(t *testing.T, runtime *node.Client, id string) string {
	t.Helper()
	url := "https://git.example/acme/app.git"
	repo := gitRepo(t, s.root, url)
	writeFile(t, filepath.Join(s.server, "home", ".gitconfig"), fmt.Sprintf("[url %q]\n\tinsteadOf = %s\n", filepath.Join(s.root, "origin.git"), url))
	ctx := context.Background()
	if err := runtime.Request(ctx, "repo", "", host.RepoRequest{Repo: repo}, nil); err != nil {
		t.Fatal(err)
	}
	if err := runtime.Request(ctx, "open", uuid.NewString(), host.OpenRequest{Name: id, Repo: repo, Prompt: "Export contacts."}, nil); err != nil {
		t.Fatalf("creating %s: %v", id, err)
	}
	return repo
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readOr(path string) string {
	data, _ := os.ReadFile(path)
	return string(data)
}

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

func gitRepo(t *testing.T, root, url string) string {
	t.Helper()
	bare, src := filepath.Join(root, "origin.git"), filepath.Join(root, "src")
	for _, args := range [][]string{
		{"init", "--bare", "-b", "main", bare},
		{"init", "-b", "main", src},
		{"-C", src, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "--allow-empty", "-m", "first"},
		{"-C", src, "remote", "add", "origin", url},
		{"-C", src, "push", bare, "main"},
	} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	return src
}

func waitFor(t *testing.T, what string, done func() bool) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); !done(); time.Sleep(100 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}

func TestASecondNodeRefusesAndTheFirstKeepsItsSocket(t *testing.T) {
	s := newStandIn(t)
	out, err := s.factory(t, s.root, s.onServer(), "node", "run")
	if err == nil || !strings.Contains(out, "already runs") {
		t.Fatalf("a second node exited with %v:\n%s\nwant a refusal naming the running node", err, out)
	}
	if !node.Answers(filepath.Join(s.server, "state", "node.sock")) {
		t.Fatal("the refused node unlinked or broke the running node's socket")
	}
}

func TestClosingTheHubLeavesTheNodeRunningStages(t *testing.T) {
	s := newStandIn(t)
	args := []string{"new-window", "-d", "-t", "factory:", "-n", "viewer", "-P", "-F", "#{window_id}"}
	for _, kv := range append(s.onServer(), "PATH="+s.bin+string(os.PathListSeparator)+os.Getenv("PATH"), "SHELL="+filepath.Join(s.bin, "shell")) {
		args = append(args, "-e", kv)
	}
	viewer := s.serverTmux(t, append(args, filepath.Join(s.bin, "factory")+" hub; exec sleep 600")...)
	hubLock := (&state.Config{StateDir: filepath.Join(s.server, "state")}).HubViewLock()
	waitFor(t, "the hub to open against the node", func() bool { return fileLocked(hubLock) })
	s.serverTmux(t, "kill-window", "-t", viewer)
	waitFor(t, "the hub to close", func() bool { return !fileLocked(hubLock) })

	runtime := node.Unix(filepath.Join(s.server, "state", "node.sock"))
	repo := s.startFactory(t, runtime, "csv")
	writeFile(t, filepath.Join(factory.WorktreesDir(repo), "csv", ".claude", "factory", "csv", "state.json"), `{"id": "csv", "stage": "implement"}`)
	if err := runtime.Request(context.Background(), "stage", uuid.NewString(), host.StageRequest{Action: "start", Role: "engineer", Text: "Implement the plan.", Factory: "csv"}, nil); err != nil {
		t.Fatalf("starting a stage with the hub closed: %v", err)
	}
	windows := func() string { return s.serverTmux(t, "list-windows", "-a", "-F", "#{window_name}") }
	waitFor(t, "the engineer's stage window", func() bool { return strings.Contains(windows(), host.StageParkName("csv", "engineer")) })
}

func TestAttachingFromAnotherNodeShowsThePreparedPaneAndClosingItLeavesTheAgentRunning(t *testing.T) {
	s := newStandIn(t)
	s.serverTmux(t, "set-option", "-g", "base-index", "1")
	runtime := node.Unix(filepath.Join(s.server, "state", "node.sock"))
	s.startFactory(t, runtime, "csv")
	var csv host.LocalFactory
	waitFor(t, "csv's pane", func() bool {
		var local host.Local
		runtime.Request(context.Background(), "local", "", nil, &local)
		i := slices.IndexFunc(local.Factories, func(f host.LocalFactory) bool { return f.ID == "csv" && f.Pane != "" })
		if i >= 0 {
			csv = local.Factories[i]
		}
		return i >= 0
	})
	pane := s.serverTmux(t, "display-message", "-p", "-t", "=factory:"+host.ParkName("csv"), "#{pane_id}")

	ctx := context.Background()
	if _, err := node.AttachCommand(ctx, runtime, "homelab", host.AttachRequest{Key: csv.Key, Token: "%999"}); err == nil {
		t.Fatal("the node prepared a view for a pane token it does not have")
	}
	words, err := node.AttachCommand(ctx, runtime, "homelab", host.AttachRequest{Key: csv.Key, Token: csv.Pane})
	if err != nil {
		t.Fatal(err)
	}
	view := words[len(words)-1]
	env := []string{"env"}
	for _, kv := range append(s.onServer(), "TMUX=") {
		env = append(env, factory.ShellQuote(kv))
	}
	proxy := "frc-proxy-" + uuid.NewString()
	start := exec.Command("tmux", "-L", proxy, "-f", "/dev/null", "new-session", "-d", "-x", "100", "-y", "30", "exec "+strings.Join(env, " ")+" "+filepath.Join(s.bin, "factory")+" host attach "+view)
	start.Env = s.env
	if out, err := start.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	t.Cleanup(func() { exec.Command("tmux", "-L", proxy, "kill-server").Run() })
	session := host.ViewSession(view)
	waitFor(t, "the proxy to attach", func() bool {
		return strings.Contains(s.serverTmux(t, "list-clients", "-F", "#{session_name}"), session)
	})
	if shown := s.serverTmux(t, "display-message", "-p", "-t", "="+session+":", "#{pane_id}"); shown != pane {
		t.Fatalf("the view shows %s; want csv's own pane %s", shown, pane)
	}

	exec.Command("tmux", "-L", proxy, "kill-server").Run()
	waitFor(t, "the view to close", func() bool {
		return !strings.Contains(s.serverTmux(t, "list-sessions", "-F", "#{session_name}"), session)
	})
	if alive := s.serverTmux(t, "display-message", "-p", "-t", pane, "#{pane_dead}"); alive != "0" {
		t.Fatalf("closing the proxy left csv's pane %q; want the agent still running", alive)
	}
}
