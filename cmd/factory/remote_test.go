package main

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"

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
	if out, err := exec.Command("tmux", "-L", s.tmux, "-f", "/dev/null", "new-session", "-d", "-s", "scratch", "exec sleep 600").CombinedOutput(); err != nil {
		t.Fatalf("tmux: %v\n%s", err, out)
	}
	t.Cleanup(func() { exec.Command("tmux", "-L", s.tmux, "kill-server").Run() })
	writeFile(t, filepath.Join(s.server, "config", "config.yml"), fmt.Sprintf("review:\n  port: %d\n", freePort(t)))
	client := filepath.Join(root, "client")
	writeFile(t, filepath.Join(client, "config", "config.yml"), "remotes:\n  homelab:\n    host: homelab\n")
	s.env = append(os.Environ(),
		"PATH="+s.bin+string(os.PathListSeparator)+os.Getenv("PATH"),
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

func TestRemoteCreationRetryDoesNotDuplicateFactory(t *testing.T) {
	s := newStandIn(t)
	url := "https://git.example/acme/app.git"
	src := gitRepo(t, s.root, url)
	s.serveGit(t, url, filepath.Join(s.root, "origin.git"))
	before := gitState(t, src)
	pwned := filepath.Join(s.root, "pwned")
	prompt := "Add CSV export $(touch " + pwned + ")"

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
