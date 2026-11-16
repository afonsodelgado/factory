package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"github.com/afonsodelgado/factory/internal/host"
	"github.com/afonsodelgado/factory/internal/install"
	"github.com/afonsodelgado/factory/internal/node"
	"github.com/afonsodelgado/factory/internal/remote"
	"github.com/afonsodelgado/factory/internal/state"
)

func runForget(args []string) int {
	if len(args) == 3 && args[0] == "--remote" {
		return exitOf(forgetRemote(args[1], args[2]))
	}
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "usage: factory forget <path> | factory forget --remote <name> <id>")
		return 2
	}
	path, err := filepath.Abs(state.ExpandHome(args[0]))
	if err != nil {
		fmt.Fprintln(os.Stderr, "factory forget:", err)
		return 1
	}
	if main, _, err := install.MainCheckout(path); err == nil {
		path = main
	}
	known, err := state.LoadKnownRepos(state.StateDir())
	if err != nil {
		fmt.Fprintln(os.Stderr, "factory forget:", err)
		return 1
	}
	repo, ok := configuredRepo(known, path)
	if !ok {
		repo = filepath.Clean(path)
	}
	forgot, err := state.ForgetRepo(state.StateDir(), repo)
	if err != nil {
		fmt.Fprintln(os.Stderr, "factory forget:", err)
		return 1
	}
	cfg, err := state.LoadConfig()
	if err != nil {
		fmt.Fprintln(os.Stderr, "factory forget:", err)
		return 1
	}
	if listed, ok := configuredRepo(cfg.Repos, repo); ok {
		fmt.Printf("%s is listed in repos in %s; remove it there too.\n", install.HomeRelative(listed), install.HomeRelative(cfg.ConfigFile()))
		return 1
	}
	if !forgot {
		fmt.Printf("The hub has no repo at %s. Nothing changed.\n", install.HomeRelative(repo))
		return 0
	}
	if node.Answers(cfg.NodeSocket()) {
		node.Unix(cfg.NodeSocket()).Request(context.Background(), "repo", "", host.RepoRequest{Repo: repo, Forget: true}, nil)
	}
	fmt.Printf("The hub no longer lists %s. Its factories, worktrees and .factory/ stay; run factory there to add it back.\n", install.HomeRelative(repo))
	return 0
}

func forgetRemote(name, id string) error {
	err := remote.UpdateCache(state.StateDir(), name, func(c *remote.Cache) error {
		i := slices.IndexFunc(c.Inventory, func(l remote.Listing) bool { return l.ID == id })
		if i < 0 {
			return fmt.Errorf("this machine knows no factory %s on %s", id, name)
		}
		c.Forgotten = append(c.Forgotten, c.Inventory[i].Incarnation)
		return nil
	})
	if err == nil {
		fmt.Printf("%s on %s is hidden here and keeps running there. factory open --remote %s %s shows it again.\n", id, name, name, id)
	}
	return err
}
