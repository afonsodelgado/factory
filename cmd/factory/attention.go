package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/afonsodelgado/factory/internal/state"
)

const attentionUsage = `usage: factory attention resolve --dir <factory dir> [--waiting-on agent|ci|none] <item id>`

func runAttention(args []string) int {
	if len(args) == 0 || args[0] != "resolve" {
		fmt.Fprintln(os.Stderr, attentionUsage)
		return 2
	}
	flags := flag.NewFlagSet("attention resolve", flag.ContinueOnError)
	dir := flags.String("dir", "", "the factory directory that holds state.json")
	waitingOn := flags.String("waiting-on", "", "what the factory waits on next: agent, ci or none")
	if err := flags.Parse(args[1:]); err != nil {
		return 2
	}
	validWait := *waitingOn == "" || *waitingOn == "agent" || *waitingOn == "ci" || *waitingOn == "none"
	if *dir == "" || flags.NArg() != 1 || !validWait {
		fmt.Fprintln(os.Stderr, attentionUsage)
		return 2
	}
	if err := state.ResolveAttention(filepath.Join(*dir, "state.json"), flags.Arg(0), *waitingOn, time.Now()); err != nil {
		fmt.Fprintln(os.Stderr, "factory attention:", err)
		return 1
	}
	return 0
}
