package main

import (
	"fmt"
	"os"
	"path/filepath"

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
	configFile := filepath.Join(state.ConfigDir(), "config.yml")
	r, err := install.Init(path, configFile)
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
	if r.ConfigAdded {
		fmt.Printf("  added      %s to repos in %s\n", install.HomeRelative(r.Repo), install.HomeRelative(configFile))
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
	if cfg, err := state.LoadConfig(); r.ConfigAdded && err == nil && hubRunning(cfg) {
		fmt.Println("The hub reads the config only when it starts: quit it with q, then run factory to see the repo.")
	}
	return 0
}
