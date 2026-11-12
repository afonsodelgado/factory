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
	"github.com/afonsodelgado/factory/internal/remote"
	"github.com/afonsodelgado/factory/internal/state"
)

type standIn struct {
	root, bin, server, tmux string
	env                     []string
}

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
	standInSSH, _ := filepath.Abs("../../internal/remote/testdata/ssh")
	if err := os.Symlink(standInSSH, filepath.Join(s.bin, "ssh")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.bin, "claude"), []byte("#!/bin/sh\nexec sleep 600\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.bin, "shell"), []byte("#!/bin/sh\n[ \"${1:-}\" = -l ] && shift\nexec /bin/sh \"$@\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	client := filepath.Join(root, "client")
	s.env = append(os.Environ(),
		"PATH="+s.bin+string(os.PathListSeparator)+os.Getenv("PATH"),
		"SHELL="+filepath.Join(s.bin, "shell"),
		"ZDOTDIR=",
		"ENV=",
		"HOME="+filepath.Join(client, "home"),
		"FACTORY_CONFIG_DIR="+filepath.Join(client, "config"),
		"FACTORY_STATE_DIR="+filepath.Join(client, "state"),
		"CLAUDE_CONFIG_DIR="+filepath.Join(client, "claude"),
		"FACTORY_TMUX_SOCKET=frc-client-unused",
		"TMUX=",
		"FACTORY_SSH_STANDIN_ROOT="+s.server,
		"FACTORY_SSH_STANDIN_BIN="+filepath.Join(s.bin, "factory"),
		"FACTORY_SSH_STANDIN_TMUX="+s.tmux,
	)
	server := exec.Command("tmux", "-L", s.tmux, "-f", "/dev/null", "new-session", "-d", "-s", "scratch", "exec sleep 600")
	server.Env = s.env
	if out, err := server.CombinedOutput(); err != nil {
		t.Fatalf("tmux: %v\n%s", err, out)
	}
	t.Cleanup(func() { exec.Command("tmux", "-L", s.tmux, "kill-server").Run() })
	writeFile(t, filepath.Join(s.server, "config", "config.yml"), fmt.Sprintf("review:\n  port: %d\n", freePort(t)))
	writeFile(t, filepath.Join(client, "config", "config.yml"), "remotes:\n  homelab:\n    host: homelab\n")
	return s
}

func (s standIn) serveGit(t *testing.T, url, path string) {
	t.Helper()
	writeFile(t, filepath.Join(s.server, "home", ".gitconfig"), readOr(filepath.Join(s.server, "home", ".gitconfig"))+fmt.Sprintf("[url %q]\n\tinsteadOf = %s\n", path, url))
}

func (s standIn) factory(t *testing.T, dir string, extra []string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command(filepath.Join(s.bin, "factory"), args...)
	cmd.Dir, cmd.Env = dir, append(s.env, extra...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func (s standIn) serverRegistry(t *testing.T) []state.Entry {
	t.Helper()
	registry, err := state.LoadRegistry(filepath.Join(s.server, "state", "registry.json"))
	if err != nil {
		t.Fatal(err)
	}
	return registry.Factories
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

func gitState(t *testing.T, repo string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", repo, "status", "--porcelain", "--ignored", "-b").CombinedOutput()
	if err != nil {
		t.Fatal(err)
	}
	head, _ := exec.Command("git", "-C", repo, "rev-parse", "HEAD").Output()
	return string(out) + string(head)
}

func TestNewFactoriesFollowTheDefaultAndExistingOnesStayWhereTheyRun(t *testing.T) {
	stateDir := t.TempDir()
	registry, _ := state.LoadRegistry(filepath.Join(stateDir, "registry.json"))
	registry.Upsert(state.Entry{Repo: "/repo", Slug: "legacy"})
	if err := registry.Save(); err != nil {
		t.Fatal(err)
	}
	remote.UpdateCache(stateDir, "homelab", func(c *remote.Cache) error {
		c.Inventory = []remote.Listing{{ID: "csv", Incarnation: "i-1"}}
		return nil
	})
	cfg := &state.Config{StateDir: stateDir, Machine: state.Machine{Remotes: map[string]state.Remote{"homelab": {Host: "homelab"}, "attic": {Host: "attic"}}, DefaultRemote: "homelab"}}
	cases := []struct {
		name, id, remote string
		local            bool
		want             string
		refused          bool
	}{
		{name: "a new factory goes to the default remote", id: "new", want: "homelab"},
		{name: "--local overrides the default for a new factory", id: "new", local: true, want: ""},
		{name: "a legacy record without a location stays local despite the default", id: "legacy", want: ""},
		{name: "a known remote factory routes to its remote without a selector", id: "csv", want: "homelab"},
		{name: "--remote and --local together are refused", id: "new", remote: "homelab", local: true, refused: true},
		{name: "--remote without an id is refused", remote: "homelab", refused: true},
		{name: "--remote cannot move a local factory", id: "legacy", remote: "homelab", refused: true},
		{name: "--local cannot shadow a remote factory's id", id: "csv", local: true, refused: true},
		{name: "another remote cannot reuse a known id", id: "csv", remote: "attic", refused: true},
		{name: "an unknown remote is refused", id: "new", remote: "nowhere", refused: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := remote.Place(cfg, c.id, c.remote, c.local)
			if c.refused != (err != nil) || (!c.refused && got != c.want) {
				t.Errorf("got %q, %v; want %q, refused %v", got, err, c.want, c.refused)
			}
		})
	}
}

func TestARemoteRequestFailsAtOnceWhileAnInteractiveHubHoldsTheServerRuntime(t *testing.T) {
	cfg := &state.Config{StateDir: t.TempDir()}
	unlock, err := lockHub(cfg.LockPath())
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	for _, op := range []string{"", uuid.NewString()} {
		_, status, err := forward(cfg, "open", op, nil)
		if status != remote.Failed || err == nil || !strings.Contains(err.Error(), "factory host run") {
			t.Fatalf("a request with op %q got %q, %v; want a failure naming factory host run, since the hub would create panes without the server environment", op, status, err)
		}
	}
}

func TestRemoteDoctorSkipsWhatNeedsAWorkingLink(t *testing.T) {
	s := newStandIn(t)
	out, err := s.factory(t, s.root, []string{"FACTORY_SSH_STANDIN_DOWN=1"}, "doctor", "--remote", "homelab")
	if err == nil || !strings.Contains(out, "✗ ssh") || !strings.Contains(out, "– claude") || !strings.Contains(out, "skipped") {
		t.Fatalf("an unreachable remote reported:\n%s\nwant ssh failing and the server checks skipped, not failed", out)
	}
	out, _ = s.factory(t, s.root, []string{"FACTORY_SSH_STANDIN_NO_FACTORY=1"}, "doctor", "--remote", "homelab")
	if !strings.Contains(out, "✓ ssh") || !strings.Contains(out, "✗ factory") || !strings.Contains(out, "command not found") {
		t.Fatalf("a server without factory on the ssh PATH reported:\n%s\nwant ssh fine and factory missing, not an unreachable host", out)
	}
	remote.UpdateCache(filepath.Join(s.root, "client", "state"), "homelab", func(c *remote.Cache) error {
		c.Server = "the-server-this-name-used-to-reach"
		return nil
	})
	out, _ = s.factory(t, s.root, nil, "doctor", "--remote", "homelab")
	if !strings.Contains(out, "✓ ssh") || !strings.Contains(out, "✗ factory") || !strings.Contains(out, "not the server") {
		t.Fatalf("a replaced server reported:\n%s\nwant the identity mismatch, so actions never reach the wrong machine", out)
	}
}

func TestRemoteCreationRetryDoesNotDuplicateFactory(t *testing.T) {
	s := newStandIn(t)
	url := "https://git.example/acme/app.git"
	src := gitRepo(t, s.root, url)
	s.serveGit(t, url, filepath.Join(s.root, "origin.git"))
	before := gitState(t, src)
	pwned := filepath.Join(s.root, "pwned")
	prompt := "Add CSV export $(touch " + pwned + ")"
	writeFile(t, filepath.Join(s.root, "client", "config", "rules.md"), "Write no comments.")

	out, err := s.factory(t, src, []string{"FACTORY_SSH_STANDIN_DROP=create"}, "open", "--remote", "homelab", "csv", "--prompt", prompt)
	if err == nil || !strings.Contains(out, "outcome unknown") {
		t.Fatalf("a lost acknowledgement reported %v:\n%s\nwant outcome unknown, never a claim that nothing happened", err, out)
	}
	if out, err := s.factory(t, src, nil, "open", "--remote", "homelab", "csv", "--prompt", prompt); err != nil {
		t.Fatalf("retry: %v\n%s", err, out)
	}
	if out, err := s.factory(t, src, nil, "open", "csv"); err != nil || !strings.Contains(out, "runs on homelab") {
		t.Fatalf("opening it again: %v\n%s", err, out)
	}
	var created int
	for _, e := range s.serverRegistry(t) {
		if e.Slug == "csv" {
			created++
		}
	}
	windows, _ := exec.Command("tmux", "-L", s.tmux, "list-windows", "-a", "-F", "#{window_name}").Output()
	if created != 1 || strings.Count(string(windows), "p-csv\n") != 1 {
		t.Fatalf("server has %d csv factories and windows:\n%s\nwant exactly one after a retry", created, windows)
	}
	if _, err := os.Stat(pwned); err == nil {
		t.Fatal("the prompt ran as shell on the server")
	}
	for deadline := time.Now().Add(10 * time.Second); !strings.Contains(paneCommands(s.tmux), "p-csv sleep"); time.Sleep(100 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("the server's csv pane runs:\n%s\nwant the stub claude; a test must never start the developer's own claude", paneCommands(s.tmux))
		}
	}
	if rules := readOr(filepath.Join(s.server, "config", "rules.md")); rules != "Write no comments." {
		t.Fatalf("the server's stage agents follow %q, not the developer's rules", rules)
	}
	if info, err := os.Stat(filepath.Join(s.root, "client", "state", "ssh")); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatal("the ssh control sockets are not in a private directory under FACTORY_STATE_DIR, so runs touch the real machine")
	}

	private := "https://git.example/acme/private.git"
	other := filepath.Join(s.root, "other")
	if out, err := exec.Command("git", "clone", "-q", src, other).CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	exec.Command("git", "-C", other, "remote", "set-url", "origin", private).Run()
	s.serveGit(t, private, filepath.Join(s.root, "missing.git"))
	if out, err := s.factory(t, other, nil, "open", "--remote", "homelab", "secret"); err == nil {
		t.Fatalf("a server without access to the repo created it:\n%s", out)
	}
	cache, _ := remote.LoadCache(filepath.Join(s.root, "client", "state"), "homelab")
	for _, e := range s.serverRegistry(t) {
		if e.Slug == "secret" || cache.Holds("secret") {
			t.Fatal("a failed creation left a factory or a reservation behind")
		}
	}
	if gitState(t, src) != before {
		t.Fatal("remote creation changed the developer's checkout")
	}
}

func paneCommands(socket string) string {
	out, _ := exec.Command("tmux", "-L", socket, "list-panes", "-a", "-F", "#{window_name} #{pane_current_command}").Output()
	return string(out)
}

func (s standIn) secondServer(t *testing.T, name string) (string, string) {
	t.Helper()
	root, socket := filepath.Join(s.root, name), "frc-"+name+"-"+uuid.NewString()
	for _, args := range [][]string{
		{"-L", socket, "-f", "/dev/null", "new-session", "-d", "-s", "scratch", "exec sleep 600"},
		{"-L", socket, "set-option", "-g", "base-index", "7"},
		{"-L", socket, "set-option", "-g", "pane-base-index", "3"},
	} {
		cmd := exec.Command("tmux", args...)
		cmd.Env = s.env
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("tmux %v: %v\n%s", args, err, out)
		}
	}
	t.Cleanup(func() { exec.Command("tmux", "-L", socket, "kill-server").Run() })
	writeFile(t, filepath.Join(root, "config", "config.yml"), fmt.Sprintf("review:\n  port: %d\n", freePort(t)))
	writeFile(t, filepath.Join(root, "home", ".gitconfig"), readOr(filepath.Join(s.server, "home", ".gitconfig")))
	return root, socket
}

func (s standIn) inProcess(t *testing.T, extra ...string) {
	t.Helper()
	for _, kv := range append(slices.Clone(s.env), extra...) {
		key, value, _ := strings.Cut(kv, "=")
		if os.Getenv(key) != value {
			t.Setenv(key, value)
		}
	}
}

func paneOf(t *testing.T, client remote.Client, id string) (remote.Factory, remote.Pane) {
	t.Helper()
	var view remote.View
	if err := client.Read(context.Background(), "view", nil, &view); err != nil {
		t.Fatal(err)
	}
	for _, f := range view.Factories {
		if f.ID == id && f.Pane != nil {
			return f, *f.Pane
		}
	}
	t.Fatalf("%s has no live pane in the view: %+v", id, view.Factories)
	return remote.Factory{}, remote.Pane{}
}

func TestRemoteAttachKeepsPaneIdentityAndAgentAlive(t *testing.T) {
	s := newStandIn(t)
	exec.Command("tmux", "-L", s.tmux, "set-option", "-g", "base-index", "1").Run()
	url := "https://git.example/acme/app.git"
	src := gitRepo(t, s.root, url)
	s.serveGit(t, url, filepath.Join(s.root, "origin.git"))
	buildRoot, buildTmux := s.secondServer(t, "build")
	writeFile(t, filepath.Join(s.root, "client", "config", "config.yml"), "remotes:\n  homelab:\n    host: homelab\n  build:\n    host: build\n")
	servers := []string{"FACTORY_SSH_STANDIN_ROOT_build=" + buildRoot, "FACTORY_SSH_STANDIN_TMUX_build=" + buildTmux}
	for _, c := range [][]string{{"homelab", "csv"}, {"build", "api"}} {
		if out, err := s.factory(t, src, servers, "open", "--remote", c[0], c[1], "--prompt", "work"); err != nil {
			t.Fatalf("creating %s on %s: %v\n%s", c[1], c[0], err, out)
		}
	}
	s.inProcess(t, servers...)
	cfg, err := state.LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	homelab, _ := remote.For(cfg, "homelab")
	build, _ := remote.For(cfg, "build")
	if out, err := exec.Command("tmux", "-L", s.tmux, "kill-window", "-t", "factory:host").CombinedOutput(); err != nil {
		t.Fatalf("stopping homelab's host: %v\n%s", err, out)
	}
	csv, csvPane := paneOf(t, homelab, "csv")
	_, apiPane := paneOf(t, build, "api")
	if csvPane.ID != apiPane.ID {
		t.Logf("pane ids differ (%s, %s); the token still has to keep the servers apart", csvPane.ID, apiPane.ID)
	}

	var view string
	wrongHost := remote.PaneTarget{Target: remote.Target{ID: "csv", Incarnation: csv.Incarnation}, Token: csvPane.Token}
	if err := build.Read(context.Background(), "attach", wrongHost, &view); err == nil {
		t.Fatal("build attached to a factory that lives on homelab")
	}
	wrongPane := remote.PaneTarget{Target: remote.Target{ID: "csv", Incarnation: csv.Incarnation}, Token: apiPane.Token}
	if err := homelab.Read(context.Background(), "attach", wrongPane, &view); err == nil {
		t.Fatal("homelab attached with another server's pane token")
	}
	right := remote.PaneTarget{Target: remote.Target{ID: "csv", Incarnation: csv.Incarnation}, Token: csvPane.Token}
	if err := homelab.Read(context.Background(), "attach", right, &view); err != nil {
		t.Fatal(err)
	}

	words := slices.Concat([]string{"ssh"}, homelab.Args(true, []string{"factory", "host", "attach", view}))
	for i, w := range words {
		words[i] = factory.ShellQuote(w)
	}
	proxy := "frc-proxy-" + uuid.NewString()
	start := exec.Command("tmux", "-L", proxy, "-f", "/dev/null", "new-session", "-d", "-x", "100", "-y", "30", "exec "+strings.Join(words, " "))
	start.Env = os.Environ()
	if out, err := start.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	t.Cleanup(func() { exec.Command("tmux", "-L", proxy, "kill-server").Run() })
	session := host.ViewSession(view)
	serverTmux := func(args ...string) string {
		out, _ := exec.Command("tmux", append([]string{"-L", s.tmux}, args...)...).Output()
		return strings.TrimSpace(string(out))
	}
	waitFor(t, "the proxy to attach", func() bool { return strings.Contains(serverTmux("list-clients", "-F", "#{session_name}"), session) })
	if shown := serverTmux("display-message", "-p", "-t", "="+session+":", "#{pane_id}"); shown != csvPane.ID {
		t.Fatalf("the view shows %s; want csv's own pane %s", shown, csvPane.ID)
	}

	exec.Command("tmux", "-L", proxy, "kill-server").Run()
	waitFor(t, "the view to close", func() bool { return !strings.Contains(serverTmux("list-sessions", "-F", "#{session_name}"), session) })
	if alive := serverTmux("display-message", "-p", "-t", csvPane.ID, "#{pane_dead}"); alive != "0" {
		t.Fatalf("closing the proxy left csv's pane %q; want the agent still running", alive)
	}
}

func waitFor(t *testing.T, what string, done func() bool) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); !done(); time.Sleep(100 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}
