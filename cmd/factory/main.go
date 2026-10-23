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
		return runVersion()
	case "update":
		return runUpdate(args[1:])
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
	case "delete":
		return runDelete(args[1:], os.Stdin, os.Stdout)
	case "init":
		return runInit(args[1:])
	case "doctor":
		return runDoctor(args[1:])
	case "bootstrap":
		return runBootstrap(args[1:])
	case "install-hooks":
		return runInstallHooks(args[1:])
	}
	fmt.Fprintf(os.Stderr, "factory: unknown command %q\n\n", args[0])
	usage(os.Stderr)
	return 2
}

func runHook(stdin io.Reader) int {
	payload, err := io.ReadAll(stdin)
	if err == nil {
		state.RecordHook(state.StateDir(), payload, os.Getenv("FACTORY_PANE_TOKEN"), time.Now())
	}
	return 0
}

func usage(w io.Writer) {
	fmt.Fprint(w, `usage: factory <command> [arguments]

commands:
  open [<id>] [--prompt text | <prompt…>] [--repo path] [--pipeline name]
              select the factory with this id, or create it with the prompt
              as its spec (without one the supervisor asks), then attach
  delete <id> [--repo path] [--yes]
              delete a factory: its pane, worktree, local branches, state,
              activity log, drafts and registry entry, so the id can be used
              again; pushed PRs and remote branches stay. --yes skips the first
              confirmation, never the one for unpushed or uncommitted work
  init [path] add a repo (default: the one you are in): write its factory
              profile, ignore the factory's files in .git/info/exclude and
              list it in config.yml; nothing is committed
  doctor      check what the factory needs and print the fix commands
  bootstrap   install the bundled skills, agents and hooks into the Claude config
  install-hooks [--remove]
              add (or remove) only the factory hooks in settings.json
  version     print the version and the latest release (also --version)
  update      install the latest release, then run its bootstrap
  hub         run the hub in the factory tmux session
  view        run a viewer pane of the hub (started by the hub)
  dialog      run a dialog popup of the hub (started by the hub)
  hook        record a Claude Code hook event (run by Claude Code)
`)
}
