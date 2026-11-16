package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/afonsodelgado/factory/internal/host"
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
	abs, err := filepath.Abs(*dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "factory attention:", err)
		return 1
	}
	instruction, err := resolution(host.ResolveRequest{Dir: abs, ID: flags.Arg(0), WaitingOn: *waitingOn})
	if err != nil {
		fmt.Fprintln(os.Stderr, "factory attention:", err)
		return 1
	}
	fmt.Println(instruction)
	return 0
}

func resolution(r host.ResolveRequest) (string, error) {
	cfg, err := state.LoadConfig()
	if err != nil {
		return "", err
	}
	runtime, err := localNode(cfg)
	if err != nil {
		return "", err
	}
	var instruction string
	return instruction, runtime.Request(context.Background(), "resolve", "", r, &instruction)
}
