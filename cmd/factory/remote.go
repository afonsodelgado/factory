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
	"sync"
	"syscall"
	"time"

	"github.com/google/uuid"

	"github.com/afonsodelgado/factory/internal/hub"
	"github.com/afonsodelgado/factory/internal/install"
	"github.com/afonsodelgado/factory/internal/remote"
	"github.com/afonsodelgado/factory/internal/state"
)

const (
	remoteUsage = `usage: factory remote add <name> <host> [--user name] [--port n] [--identity file]
       factory remote list
       factory remote remove <name>
       factory remote default <name | local>
       factory remote push [--server url | --off]`
	refreshWait = 3 * time.Second
	createWait  = 15 * time.Minute
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
	name, host := flags.Arg(0), flags.Arg(1)
	err := state.UpdateMachine(state.ConfigDir(), func(m *state.Machine) error {
		if _, taken := m.Remotes[name]; taken {
			return fmt.Errorf("a remote is already named %s; remove it first to point it somewhere else", name)
		}
		if m.Remotes == nil {
			m.Remotes = map[string]state.Remote{}
		}
		m.Remotes[name] = state.Remote{Host: host, User: *user, Port: *port, Identity: *identity}
		return nil
	})
	if err != nil {
		return err
	}
	fmt.Printf("Added %s. Check it with factory doctor --remote %s; create a factory there with factory open --remote %s <id>.\n", name, name, name)
	return nil
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
	wasDefault := false
	err := state.UpdateMachine(state.ConfigDir(), func(m *state.Machine) error {
		if _, ok := m.Remotes[name]; !ok {
			return fmt.Errorf("no remote is named %s", name)
		}
		delete(m.Remotes, name)
		if m.DefaultRemote == name {
			m.DefaultRemote, wasDefault = "", true
		}
		return nil
	})
	if err != nil {
		return err
	}
	fmt.Printf("Removed %s. Its factories keep running there, and it keeps sending phone push if that is on; this machine just stops connecting to it.\n", name)
	if wasDefault {
		fmt.Println("New factories now run on this machine.")
	}
	return nil
}

func remoteDefault(name string) error {
	if name == "local" {
		name = ""
	}
	if err := state.UpdateMachine(state.ConfigDir(), func(m *state.Machine) error {
		m.DefaultRemote = name
		return nil
	}); err != nil {
		return err
	}
	if name == "" {
		fmt.Println("New factories run on this machine.")
		return nil
	}
	fmt.Printf("New factories run on %s. Existing factories stay where they are.\n", name)
	return nil
}

func remotePush(args []string) error {
	flags := flag.NewFlagSet("remote push", flag.ContinueOnError)
	server := flags.String("server", "", "the ntfy server; default "+state.DefaultPushServer)
	off := flags.Bool("off", false, "turn phone push off")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 || (*off && *server != "") {
		return errors.New(remoteUsage)
	}
	var push state.Push
	err := state.UpdateMachine(state.ConfigDir(), func(m *state.Machine) error {
		switch {
		case *off:
			m.Push.Topic = ""
		case m.Push.Topic == "":
			m.Push.Topic = newTopic()
		}
		if *server != "" {
			m.Push.Server = *server
		}
		push = m.Push
		return nil
	})
	if err != nil {
		return err
	}
	if !push.On() {
		fmt.Println("Phone push is off.")
		return nil
	}
	fmt.Printf("Phone push is on. In the ntfy app on your phone, subscribe to %s/%s; anyone with that address can read the pushes, so keep it private.\n", push.ServerURL(), push.Topic)
	return nil
}

func newTopic() string {
	b := make([]byte, 32)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func whereToRun(cfg *state.Config, id, remoteFlag string, local bool) (string, error) {
	if remoteFlag != "" && local {
		return "", errors.New("give --remote or --local, not both")
	}
	if remoteFlag != "" {
		if _, ok := cfg.Remotes[remoteFlag]; !ok {
			return "", fmt.Errorf("no remote is named %s; factory remote list shows them", remoteFlag)
		}
	}
	if id == "" {
		if remoteFlag != "" {
			return "", fmt.Errorf("name the factory to create on %s", remoteFlag)
		}
		return "", nil
	}
	here, elsewhere := localFactory(cfg, id), holders(cfg, id)
	switch {
	case here && remoteFlag != "":
		return "", fmt.Errorf("%s runs on this machine; open it without --remote", id)
	case here:
		return "", nil
	case len(elsewhere) > 1 && remoteFlag == "":
		return "", fmt.Errorf("%s exists on %s; pick one with --remote", id, strings.Join(elsewhere, " and "))
	case len(elsewhere) > 0 && local:
		return "", fmt.Errorf("%s runs on %s; open it with --remote %s, or pick another id", id, elsewhere[0], elsewhere[0])
	case len(elsewhere) > 0 && remoteFlag != "" && !slices.Contains(elsewhere, remoteFlag):
		return "", fmt.Errorf("%s runs on %s; pick another id to create one on %s", id, elsewhere[0], remoteFlag)
	case len(elsewhere) > 0 && remoteFlag == "":
		return elsewhere[0], nil
	case remoteFlag != "" || local:
		return remoteFlag, nil
	}
	return cfg.DefaultRemote, nil
}

func localFactory(cfg *state.Config, id string) bool {
	registry, err := state.LoadRegistry(cfg.RegistryPath())
	return err == nil && slices.ContainsFunc(registry.Factories, func(e state.Entry) bool { return e.Slug == id })
}

func holders(cfg *state.Config, id string) []string {
	var names []string
	for _, name := range slices.Sorted(maps.Keys(cfg.Remotes)) {
		if cache, err := remote.LoadCache(cfg.StateDir, name); err == nil && cache.Holds(id) {
			names = append(names, name)
		}
	}
	return names
}

func refreshInventories(cfg *state.Config) {
	ctx, cancel := context.WithTimeout(context.Background(), refreshWait)
	defer cancel()
	var wg sync.WaitGroup
	for name := range cfg.Remotes {
		client, err := remote.For(cfg, name)
		if err != nil {
			continue
		}
		wg.Go(func() { client.Inventory(ctx) })
	}
	wg.Wait()
}

func withCreationLock(cfg *state.Config, fn func() error) error {
	if err := os.MkdirAll(cfg.StateDir, 0o700); err != nil {
		return err
	}
	lock, err := os.OpenFile(filepath.Join(cfg.StateDir, "create.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	return fn()
}

func refuseKnownID(cfg *state.Config, id string) error {
	refreshInventories(cfg)
	if elsewhere := holders(cfg, id); len(elsewhere) > 0 {
		return fmt.Errorf("%s already runs on %s; pick another id, or open that one with --remote %s", id, elsewhere[0], elsewhere[0])
	}
	return nil
}

func createRemote(cfg *state.Config, name string, r hub.OpenRequest, gitRemote string) error {
	client, err := remote.For(cfg, name)
	if err != nil {
		return err
	}
	repo, err := factoryRepo(r.Repo)
	if err != nil {
		return err
	}
	gitURL, err := remote.GitURL(repo, gitRemote)
	if err != nil {
		return err
	}
	profile, err := os.ReadFile(state.ProfilePath(repo))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if _, err := state.ReadProfile(repo); err != nil {
		return fmt.Errorf("%s: %w", state.ProfilePath(repo), err)
	}
	var reservation remote.Reservation
	err = withCreationLock(cfg, func() error {
		refreshInventories(cfg)
		if localFactory(cfg, r.Name) {
			return fmt.Errorf("%s runs on this machine; pick another id", r.Name)
		}
		if others := slices.DeleteFunc(holders(cfg, r.Name), func(n string) bool { return n == name }); len(others) > 0 {
			return fmt.Errorf("%s already runs on %s; pick another id", r.Name, others[0])
		}
		return remote.UpdateCache(cfg.StateDir, name, func(c *remote.Cache) error {
			if i := slices.IndexFunc(c.Reservations, func(res remote.Reservation) bool { return res.ID == r.Name }); i >= 0 {
				reservation = c.Reservations[i]
				return nil
			}
			reservation = remote.Reservation{ID: r.Name, GitURL: gitURL, Op: uuid.NewString(), At: time.Now()}
			c.Reservations = append(c.Reservations, reservation)
			return nil
		})
	})
	if err != nil {
		return err
	}
	if reservation.GitURL != gitURL {
		return fmt.Errorf("a creation of %s from %s on %s has not finished; run it again from that repo to see what happened", r.Name, reservation.GitURL, name)
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWait)
	defer cancel()
	create := remote.Create{ID: r.Name, GitURL: gitURL, Prompt: r.Prompt, Pipeline: r.Pipeline, Stages: r.Stages, Agents: r.Agents, Supervisor: r.Supervisor, Profile: profile}
	fmt.Printf("Creating %s on %s: the server clones or fetches %s first, which can take a while.\n", r.Name, name, gitURL)
	var listing remote.Listing
	err = client.Mutate(ctx, "create", reservation.Op, create, &listing)
	if errors.Is(err, remote.ErrUncertain) {
		return fmt.Errorf("%s on %s: %w", r.Name, name, err)
	}
	settle := remote.UpdateCache(cfg.StateDir, name, func(c *remote.Cache) error {
		c.Reservations = slices.DeleteFunc(c.Reservations, func(res remote.Reservation) bool { return res.ID == r.Name })
		if err == nil {
			c.Inventory = append(slices.DeleteFunc(c.Inventory, func(l remote.Listing) bool { return l.ID == r.Name }), listing)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("%s did not start on %s: %w", r.Name, name, err)
	}
	if settle != nil {
		return settle
	}
	fmt.Printf("%s runs on %s, cloned from %s. It keeps going when this machine sleeps.\n", r.Name, name, gitURL)
	return nil
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

var serverCheckNames = []string{"factory", "tmux", "git", "gh", "gh-stack", "claude", "git access", "bundle", "hooks", "forwarding"}

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
		return header, append(checks, skipped(serverCheckNames, "skipped, needs ssh")...)
	}
	checks := []install.Check{{Name: "ssh", Mark: install.Pass, Detail: fmt.Sprintf("%s in %.1fs", client.Endpoint(), time.Since(start).Seconds())}}
	if err != nil {
		checks = append(checks, install.Check{Name: "factory", Mark: install.Fail, Detail: err.Error(), Fix: "ssh " + name + " factory --version"})
		return header, append(checks, skipped(serverCheckNames[1:], "skipped, needs a matching factory")...)
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
