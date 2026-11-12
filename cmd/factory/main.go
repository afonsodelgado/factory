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
	case "host":
		return runHost(args[1:])
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
	case "attention":
		return runAttention(args[1:])
	case "remote":
		return runRemote(args[1:])
	case "--remote", "--local":
		return runOpen(args)
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
  open … --remote <name> | --local [--git-remote name]
              --remote creates a new factory on that remote, --local here;
              without either, new factories go to default_remote
  remote add <name> <host> [--user name] [--port n] [--identity file]
  remote list | remove <name> | default <name | local>
  remote push [--server url | --off]
              add, list and remove remotes, pick where new factories run,
              and set up phone push through ntfy
  delete <id> [--repo path] [--remote name | --local] [--yes]
              delete a factory: its pane, worktree, local branches, state,
              activity log, drafts and registry entry, so the id can be used
              again; pushed PRs and remote branches stay. --yes skips the first
              confirmation, never the one for unpushed or uncommitted work
  init [path] add a repo (default: the one you are in) without opening the
              hub: write its .factory/config.yml, ignore the factory's files
              in .git/info/exclude and list it in the hub; nothing is
              committed. factory and factory open do this by themselves
              in a repo the hub does not list yet
  forget <path> | --remote <name> <id>
              stop listing a repo in the hub; its files stay. With
              --remote, hide a remote factory here; the remote keeps it
  doctor [--remote name]
              check what the factory needs and print the fix commands;
              --remote checks only that remote, over ssh
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
  review open [--factory id] [--for design|walkthrough] <url>
              open any page, such as the running app, in the factory's
              Chrome with the comment layer
  review stack [--for design|walkthrough]
              run the stack that factory-qa wrote to review/stack.json in a
              window of its own, then open the app signed in for the
              walkthrough
  attention resolve --dir <factory dir> [--waiting-on agent|ci|none] <id>
              set an attention item's resolved_at in state.json, and
              waiting_on when given (run by the supervisor)
  version     print the version and check the latest release on GitHub;
              --version prints only the local version
  update      install the latest release, then run its bootstrap
  hub         run the hub in the factory tmux session
  host run    run the factories without the hub, in a window of the factory
              tmux session that keeps going after you disconnect (on a
              server)
  host request
              answer one JSON request from another machine on stdin (run
              over ssh by the factory on that machine)
  view        run a viewer pane of the hub (started by the hub)
  dialog      run a dialog popup of the hub (started by the hub)
  hook        record a hook event (run by Claude Code, Codex and OpenCode)
`)
}
