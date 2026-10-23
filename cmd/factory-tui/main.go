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
		return runOpen(nil)
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
	case "dialog":
		return runDialog(args[1:])
	case "open":
		return runOpen(args[1:])
	case "doctor":
		return runDoctor(args[1:])
	case "bootstrap":
		return runBootstrap(args[1:])
	case "install-hooks":
		return runInstallHooks(args[1:])
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
  open [<ticket|slug>] [--repo path]
              create or select a factory, then attach to the hub
  doctor      check what the factory needs and print the fix commands
  bootstrap   install the bundled skills, agents and hooks into the Claude config
  install-hooks [--remove]
              add (or remove) only the factory-tui hooks in settings.json
  hub         run the hub in the factory tmux session
  view        run a viewer pane of the hub (started by the hub)
  dialog      run a dialog popup of the hub (started by the hub)
  hook        record a Claude Code hook event (run by Claude Code)
  --version   print the version
`)
}
