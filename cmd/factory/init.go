package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/afonsodelgado/factory/internal/gitclone"
	"github.com/afonsodelgado/factory/internal/host"
	"github.com/afonsodelgado/factory/internal/install"
	"github.com/afonsodelgado/factory/internal/node"
	"github.com/afonsodelgado/factory/internal/state"
)

func runInit(args []string) int {
	path := "."
	switch len(args) {
	case 0:
	case 1:
		path = state.ExpandHome(args[0])
	default:
		fmt.Fprintln(os.Stderr, "usage: factory init [path]")
		return 2
	}
	r, err := install.Init(path, state.StateDir())
	if r.Repo != "" {
		fmt.Printf("factory init · %s\n\n", install.HomeRelative(r.Repo))
	}
	switch {
	case r.CopiedFrom != "":
		fmt.Printf("  copied     %s to %s; delete the old file once the factories running there finish\n", install.HomeRelative(r.CopiedFrom), install.HomeRelative(r.Profile))
	case r.ProfileWritten:
		fmt.Println("  wrote      " + install.HomeRelative(r.Profile))
	}
	for _, p := range r.Excluded {
		fmt.Printf("  excluded   %s in %s\n", p, install.HomeRelative(r.ExcludeFile))
	}
	if r.Remembered {
		fmt.Printf("  added      %s to this machine's repos\n", install.HomeRelative(r.Repo))
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "factory init:", err)
		return 1
	}
	if !r.Changed() {
		fmt.Println("Nothing changed.")
	}
	fmt.Println()
	fmt.Println(initAdvice(r))
	if cfg, err := state.LoadConfig(); r.Remembered && err == nil && node.Answers(cfg.NodeSocket()) {
		if repo, ok := configuredRepo(cfg.Repos, r.Repo); ok {
			node.Unix(cfg.NodeSocket()).Request(context.Background(), "repo", "", host.RepoRequest{Repo: repo}, nil)
		}
	}
	return 0
}

func initAdvice(r install.InitResult) string {
	name, config := filepath.Base(r.Repo), install.HomeRelative(r.Profile)
	if !r.ProfileWritten || r.CopiedFrom != "" {
		return fmt.Sprintf("The repo config is %s. Its commands stay as they are; a factory discovers only the ones left blank. Edit them from the hub's n dialog (ctrl+e).", config)
	}
	advice := fmt.Sprintf("%s's first factory on this machine works out how to build, lint, test and run it, runs each command here and saves the ones that work in %s. Put the repo's secrets (.env files and the like) here first, as you would to run it yourself.\n", name, config)
	_, err := gitclone.RemoteURL(r.Repo)
	switch {
	case errors.Is(err, gitclone.ErrNoNetworkRemote):
		return advice + fmt.Sprintf("! %s has no network git remote, so it stays on this machine: no other node can add it, and its factories can't move. Push it to a remote to change that.", name)
	case err != nil:
		return advice + fmt.Sprintf("! Other nodes can't use %s yet: %v.", name, err)
	}
	return advice + fmt.Sprintf("%s is on this machine only. To work on it from another node, clone it there and run factory init in it, or add it from that node's hub (n → + add a repo…).", name)
}

func setUpRepo(path string) (repo, notice string, err error) {
	if _, _, err := install.MainCheckout(path); err != nil {
		return "", "", nil
	}
	r, err := install.Init(path, state.StateDir())
	if err != nil {
		return r.Repo, "", err
	}
	return r.Repo, setupNotice(r), nil
}

func setupNotice(r install.InitResult) string {
	repo := install.HomeRelative(r.Repo)
	switch {
	case r.CopiedFrom != "":
		return fmt.Sprintf("Set up %s: copied %s to %s.", repo, install.HomeRelative(r.CopiedFrom), install.HomeRelative(r.Profile))
	case r.ProfileWritten:
		return fmt.Sprintf("Added %s on this machine. Its first factory here works out how to build, lint, test and run it and saves the commands in %s; ctrl+e shows or edits them.", repo, install.HomeRelative(r.Profile))
	case r.Remembered:
		return fmt.Sprintf("Added %s on this machine. It already had a repo config; a factory discovers only the commands left blank.", repo)
	}
	return ""
}
