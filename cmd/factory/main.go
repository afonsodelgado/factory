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
	case "--version":
		fmt.Println(versionLine())
		return 0
	case "version":
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
	case "forget":
		return runForget(args[1:])
	case "doctor":
		return runDoctor(args[1:])
	case "bootstrap":
		return runBootstrap(args[1:])
	case "install-hooks":
		return runInstallHooks(args[1:])
	case "stage":
		return runStage(args[1:])
	case "review":
		return runReview(args[1:])
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
  open [<id>] [--prompt text | <prompt…>] [--repo path]
              [--pipeline name | --stages a,b,c]
              [--agent role=model:effort | role=tool/model:effort]
              [--supervisor tool/model:effort]
              select the factory with this id, or create it with the prompt
              as its spec (without one the supervisor asks), then attach
  delete <id> [--repo path] [--yes]
              delete a factory: its pane, worktree, local branches, state,
              activity log, drafts and registry entry, so the id can be used
              again; pushed PRs and remote branches stay. --yes skips the first
              confirmation, never the one for unpushed or uncommitted work
  init [path] add a repo (default: the one you are in) without opening the
              hub: write its .factory/config.yml, ignore the factory's files
              in .git/info/exclude and list it in the hub; nothing is
              committed. factory and factory open do this by themselves
              in a repo the hub does not list yet
  forget <path>
              stop listing a repo in the hub; its files stay
  doctor      check what the factory needs and print the fix commands
  bootstrap   install the bundled skills, agents and hooks into the Claude
              config, and for Codex and OpenCode when they are installed
  install-hooks [--remove]
              add (or remove) only the factory hooks, in Claude's
              settings.json and Codex's hooks.json
  stage start --role <role> --task <file> [--wait]
  stage send --role <role> --message <file> [--wait]
              run a stage agent on its own tool in a pane beside the
              supervisor, or send it a follow-up; prints the result file
              (run by the supervisor from its pane)
  stage ci <pr number>...
              wait for CI and CodeRabbit on these PRs, then type the
              result into the supervisor (for Codex and OpenCode)
  review reply --thread <id> [--resolve] [text…]
              answer a comment thread from the review page, and resolve it
              when the change is made (run by a stage agent)
  review threads [--page path] [--open]
              list this factory's comment threads
  version     print the version and check the latest release on GitHub;
              --version prints only the local version
  update      install the latest release, then run its bootstrap
  hub         run the hub in the factory tmux session
  view        run a viewer pane of the hub (started by the hub)
  dialog      run a dialog popup of the hub (started by the hub)
  hook        record a hook event (run by Claude Code, Codex and OpenCode)
`)
}
