package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/afonsodelgado/factory/internal/hub"
	"github.com/afonsodelgado/factory/internal/install"
	"github.com/afonsodelgado/factory/internal/remote"
	"github.com/afonsodelgado/factory/internal/state"
)

const (
	remoteUsage = `usage: factory remote add <name> <[user@]host[:port]> [--user name] [--port n] [--identity file]
       factory remote list
       factory remote remove <name>
       factory remote default <name | local>
       factory remote push [--server url | --off | --test]`
)

func runRemote(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, remoteUsage)
		return 2
	}
	var err error
	switch {
	case args[0] == "add":
		err = remoteAdd(args[1:])
	case args[0] == "list" && len(args) == 1:
		err = remoteList()
	case args[0] == "remove" && len(args) == 2:
		err = remoteRemove(args[1])
	case args[0] == "default" && len(args) == 2:
		err = remoteDefault(args[1])
	case args[0] == "push":
		err = remotePush(args[1:])
	default:
		fmt.Fprintln(os.Stderr, remoteUsage)
		return 2
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "factory remote:", err)
		return 1
	}
	return 0
}

func remoteAdd(args []string) error {
	flags := flag.NewFlagSet("remote add", flag.ContinueOnError)
	user := flags.String("user", "", "the ssh user; default: what ssh uses for this host")
	port := flags.Int("port", 0, "the ssh port; default: what ssh uses for this host")
	identity := flags.String("identity", "", "the ssh key file; default: ssh-agent and your ssh config")
	if err := flags.Parse(reorder(args)); err != nil || flags.NArg() != 2 {
		return errors.New(remoteUsage)
	}
	name := flags.Arg(0)
	r, err := remote.ParseTarget(flags.Arg(1))
	if err != nil {
		return err
	}
	if *user != "" {
		r.User = *user
	}
	if *port != 0 {
		r.Port = *port
	}
	r.Identity = *identity
	if err := addRemote(name, r); err != nil {
		return err
	}
	fmt.Printf("Added %s. Check it with factory doctor --remote %s; create a factory there with factory open --remote %s <id>.\n", name, name, name)
	return nil
}

func addRemote(name string, r state.Remote) error {
	return state.UpdateMachine(state.ConfigDir(), func(m *state.Machine) error {
		if _, taken := m.Remotes[name]; taken {
			return fmt.Errorf("a remote is already named %s; remove it first to point it somewhere else", name)
		}
		if m.Remotes == nil {
			m.Remotes = map[string]state.Remote{}
		}
		m.Remotes[name] = r
		return nil
	})
}

func remoteList() error {
	cfg, err := state.LoadConfig()
	if err != nil {
		return err
	}
	if len(cfg.Remotes) == 0 {
		fmt.Println("No remotes. Add one with factory remote add <name> <host>.")
		return nil
	}
	for _, name := range slices.Sorted(maps.Keys(cfg.Remotes)) {
		client, _ := remote.For(cfg, name)
		cache, _ := remote.LoadCache(cfg.StateDir, name)
		line := fmt.Sprintf("%-12s %s", name, client.Endpoint())
		if cfg.DefaultRemote == name {
			line += "  default"
		}
		if cache.Release != "" {
			line += fmt.Sprintf("  %s, %s", cache.Release, factories(len(cache.Shown())))
		}
		if remote.PushPending(cfg.StateDir, name, cfg.Push) {
			line += "  push settings pending"
		}
		fmt.Println(line)
	}
	return nil
}

func factories(n int) string {
	if n == 1 {
		return "1 factory"
	}
	return fmt.Sprintf("%d factories", n)
}

func remoteRemove(name string) error {
	wasDefault, err := removeRemote(name)
	if err != nil {
		return err
	}
	fmt.Printf("Removed %s. Its factories keep running there, and it keeps sending phone push if that is on; this machine just stops connecting to it.\n", name)
	if wasDefault {
		fmt.Println("New factories now run on this machine.")
	}
	return nil
}

func removeRemote(name string) (wasDefault bool, err error) {
	err = state.UpdateMachine(state.ConfigDir(), func(m *state.Machine) error {
		if _, ok := m.Remotes[name]; !ok {
			return fmt.Errorf("no remote is named %s", name)
		}
		delete(m.Remotes, name)
		if m.DefaultRemote == name {
			m.DefaultRemote, wasDefault = "", true
		}
		return nil
	})
	return wasDefault, err
}

func remoteDefault(name string) error {
	if name == "local" {
		name = ""
	}
	if err := setDefaultRemote(name); err != nil {
		return err
	}
	if name == "" {
		fmt.Println("New factories run on this machine.")
		return nil
	}
	fmt.Printf("New factories run on %s. Existing factories stay where they are.\n", name)
	return nil
}

func setDefaultRemote(name string) error {
	return state.UpdateMachine(state.ConfigDir(), func(m *state.Machine) error {
		m.DefaultRemote = name
		return nil
	})
}

func remotePush(args []string) error {
	flags := flag.NewFlagSet("remote push", flag.ContinueOnError)
	server := flags.String("server", "", "the ntfy server; default "+state.DefaultPushServer)
	off := flags.Bool("off", false, "turn phone push off")
	test := flags.Bool("test", false, "ask every remote to send a test push now")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 || (*off && *server != "") || (*test && (*off || *server != "")) {
		return errors.New(remoteUsage)
	}
	if *test {
		return testPush()
	}
	push, err := updatePush(func(p *state.Push) {
		switch {
		case *off:
			p.Topic = ""
		case p.Topic == "":
			p.Topic = newTopic()
		}
		if *server != "" {
			p.Server = *server
		}
	})
	if err != nil {
		return err
	}
	if !push.On() {
		fmt.Println("Phone push is off.")
	} else {
		fmt.Printf("Phone push is on. In the ntfy app on your phone, subscribe to %s/%s; anyone with that address can read the pushes, so keep it private.\n", push.ServerURL(), push.Topic)
	}
	return syncPush(push)
}

func eachRemote(do func(context.Context, remote.Client) string) ([]string, error) {
	cfg, err := state.LoadConfig()
	if err != nil {
		return nil, err
	}
	var lines []string
	for _, name := range slices.Sorted(maps.Keys(cfg.Remotes)) {
		client, _ := remote.For(cfg, name)
		ctx, cancel := context.WithTimeout(context.Background(), probeWait)
		lines = append(lines, name+": "+do(ctx, client))
		cancel()
	}
	return lines, nil
}

func printEach(lines []string, err error) error {
	for _, line := range lines {
		fmt.Println("  " + line)
	}
	return err
}

func syncPush(push state.Push) error {
	return printEach(eachRemote(func(ctx context.Context, client remote.Client) string {
		if err := client.SyncPush(ctx, push); err != nil {
			return "pending, it takes the change once it answers (" + err.Error() + ")"
		}
		return "has it"
	}))
}

func testPush() error { return printEach(pushTests()) }

func pushTests() ([]string, error) {
	return eachRemote(func(ctx context.Context, client remote.Client) string {
		if err := client.Read(ctx, "push-test", nil, nil); err != nil {
			return "no test push: " + err.Error()
		}
		return "sent a test push"
	})
}

func updatePush(change func(*state.Push)) (state.Push, error) {
	var push state.Push
	err := state.UpdateMachine(state.ConfigDir(), func(m *state.Machine) error {
		change(&m.Push)
		push = m.Push
		return nil
	})
	return push, err
}

func newTopic() string {
	b := make([]byte, 32)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func createRemote(cfg *state.Config, name string, r hub.OpenRequest, gitRemote string) error {
	repo, err := factoryRepo(r.Repo)
	if err != nil {
		return err
	}
	create := remote.Create{ID: r.Name, Prompt: r.Prompt, Pipeline: r.Pipeline, Stages: r.Stages, Agents: r.Agents, Supervisor: r.Supervisor}
	return remote.CreateOn(cfg, name, repo, gitRemote, create, func(line string) { fmt.Println(line) })
}

func factoryRepo(named string) (string, error) {
	path := "."
	if named != "" {
		path = state.ExpandHome(named)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	repo, _, err := install.MainCheckout(abs)
	if err != nil {
		return "", fmt.Errorf("%s is not inside a git repo; run factory open --remote from the repo, or pass --repo", abs)
	}
	return repo, nil
}

func remotesToCheck(cfg *state.Config, only string) []string {
	if only != "" {
		return []string{only}
	}
	return slices.Sorted(maps.Keys(cfg.Remotes))
}

func remoteDoctor(cfg *state.Config, name string) (string, []install.Check) {
	header := fmt.Sprintf("factory doctor · %s · over ssh, checks only, changes nothing", name)
	client, err := remote.For(cfg, name)
	if err != nil {
		return header, []install.Check{{Name: "remote", Mark: install.Fail, Detail: err.Error(), Fix: "factory remote list"}}
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	start := time.Now()
	hello, err := client.Hello(ctx)
	var unreachable *remote.TransportError
	if errors.As(err, &unreachable) {
		checks := []install.Check{{Name: "ssh", Mark: install.Fail, Detail: unreachable.Error(), Fix: "check " + client.Remote.Host + " is on and reachable: ssh " + client.Endpoint()}}
		return header, append(checks, skipped(remote.ServerChecks, "skipped, needs ssh")...)
	}
	checks := []install.Check{{Name: "ssh", Mark: install.Pass, Detail: fmt.Sprintf("%s in %.1fs", client.Endpoint(), time.Since(start).Seconds())}}
	var missing *remote.CommandError
	if errors.As(err, &missing) {
		checks = append(checks, install.Check{Name: "factory", Mark: install.Fail, Detail: missing.Detail, Fix: "install factory where ssh commands find it: ssh " + name + " 'command -v factory'  must print its path"})
		return header, append(checks, skipped(remote.ServerChecks[1:], "skipped, needs factory on "+name)...)
	}
	if err != nil {
		checks = append(checks, install.Check{Name: "factory", Mark: install.Fail, Detail: err.Error(), Fix: "ssh " + name + " factory --version"})
		return header, append(checks, skipped(remote.ServerChecks[1:], "skipped, needs a matching factory")...)
	}
	header = fmt.Sprintf("factory doctor · %s · %s · %s · over ssh, checks only, changes nothing", name, hello.Release, hello.Platform)
	if hello.Release == version {
		checks = append(checks, install.Check{Name: "factory", Mark: install.Pass, Detail: hello.Release + ", the same as this machine"})
	} else {
		checks = append(checks, install.Check{Name: "factory", Mark: install.Fail, Detail: fmt.Sprintf("%s on %s, %s here", hello.Release, name, version), Fix: "factory update && ssh " + name + " factory update  so both run the same release"})
	}
	var urls []string
	if repo, err := factoryRepo(""); err == nil {
		if url, err := remote.GitURL(repo, ""); err == nil {
			urls = append(urls, url)
		}
	}
	var server []install.Check
	if err := client.Read(ctx, "doctor", urls, &server); err != nil {
		return header, append(checks, install.Check{Name: "server", Mark: install.Fail, Detail: err.Error()})
	}
	for _, c := range server {
		checks = append(checks, onServer(name, c))
	}
	if cfg.Push.On() {
		return header, append(checks, install.Check{Name: "push", Mark: install.Pass, Detail: "on, through " + cfg.Push.ServerURL()})
	}
	return header, append(checks, install.Check{Name: "push", Mark: install.Warn, Detail: "off, so your phone hears nothing while this laptop sleeps", Fix: "factory remote push"})
}

func skipped(names []string, why string) []install.Check {
	checks := make([]install.Check, len(names))
	for i, name := range names {
		checks[i] = install.Check{Name: name, Mark: install.Skip, Detail: why}
	}
	return checks
}

var serverCommands = []string{"factory", "gh", "claude", "codex", "opencode", "git", "cat", "curl", "sudo"}

func onServer(name string, c install.Check) install.Check {
	command, note, _ := strings.Cut(c.Fix, "  ")
	program, _, _ := strings.Cut(command, " ")
	switch {
	case c.Fix == "":
	case slices.Contains(serverCommands, program):
		c.Fix = "ssh -t " + name + " " + shellQuote(command)
		if note != "" {
			c.Fix += "  " + note
		}
	default:
		c.Fix = "on " + name + ": " + c.Fix
	}
	return c
}
