package main

import (
	"fmt"
	"os"

	"github.com/afonsodelgado/factory/internal/hub"
	"github.com/afonsodelgado/factory/internal/install"
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
		fmt.Printf("  added      %s to the hub's repos\n", install.HomeRelative(r.Repo))
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "factory init:", err)
		return 1
	}
	if !r.Changed() {
		fmt.Println("Nothing changed.")
	}
	fmt.Printf("\nThe repo config is %s. Edit it to match the repo; nothing in the repo is committed.\n", install.HomeRelative(r.Profile))
	if r.ProfileWritten && r.CopiedFrom == "" {
		fmt.Println("CodeRabbit is off in it. Set coderabbit: true only if CodeRabbit reviews this repo's pull requests.")
	}
	if cfg, err := state.LoadConfig(); r.Remembered && err == nil && hubRunning(cfg) {
		if repo, ok := configuredRepo(cfg.Repos, r.Repo); ok {
			request(cfg.SocketPath(), "repo", hub.RepoRequest{Repo: repo})
		}
	}
	return 0
}

func setUpRepo(path string, named bool) (repo, notice string, err error) {
	if _, _, err := install.MainCheckout(path); err != nil && !named {
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
		return fmt.Sprintf("Set up %s: wrote %s with CodeRabbit off. Edit it to match the repo.", repo, install.HomeRelative(r.Profile))
	case r.Remembered:
		return fmt.Sprintf("Added %s to the hub.", repo)
	}
	return ""
}
