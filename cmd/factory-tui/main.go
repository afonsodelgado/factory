package main

import (
	"fmt"
	"io"
	"os"
	"time"

	"github.com/afonsodelgado/factory/internal/state"
)

var version = "dev"

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 0 {
		usage(os.Stderr)
		return 2
	}
	switch args[0] {
	case "--version", "version":
		fmt.Println(version)
		return 0
	case "-h", "--help", "help":
		usage(os.Stdout)
		return 0
	case "hook":
		return runHook(os.Stdin)
	case "hub":
		return runHub(args[1:])
	case "view":
		return runView(args[1:])
	}
	fmt.Fprintf(os.Stderr, "factory-tui: unknown command %q\n\n", args[0])
	usage(os.Stderr)
	return 2
}

func runHook(stdin io.Reader) int {
	payload, err := io.ReadAll(stdin)
	if err == nil {
		state.RecordHook(state.StateDir(), payload, time.Now())
	}
	return 0
}

func usage(w io.Writer) {
	fmt.Fprint(w, `usage: factory-tui <command> [arguments]

commands:
  hub         run the hub in the factory tmux session
  view        run a viewer pane of the hub (started by the hub)
  hook        record a Claude Code hook event (run by Claude Code)
  --version   print the version
`)
}
