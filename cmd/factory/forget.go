package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/afonsodelgado/factory/internal/hub"
	"github.com/afonsodelgado/factory/internal/install"
	"github.com/afonsodelgado/factory/internal/state"
)

func runForget(args []string) int {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "usage: factory forget <path>")
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
	if hubRunning(cfg) {
		request(cfg.SocketPath(), "repo", hub.RepoRequest{Repo: repo, Forget: true})
	}
	fmt.Printf("The hub no longer lists %s. Its factories, worktrees and .factory/ stay; run factory there to add it back.\n", install.HomeRelative(repo))
	return 0
}
