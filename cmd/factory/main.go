package main

import (
	"fmt"
	"io"
	"os"
	"slices"
	"time"

	"github.com/afonsodelgado/factory/internal/state"
)

var version = "dev"

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	listDroppedRemotes(args)
	if len(args) == 0 {
		return runOpen()
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
	case "setup":
		return runSetup(args[1:])
	case "install-hooks":
		return runInstallHooks(args[1:])
	case "workflow":
		return runWorkflow(args[1:])
	case "stage":
		return runStage(args[1:])
	case "review":
		return runReview(args[1:])
	case "attention":
		return runAttention(args[1:])
	case "node":
		return runNode(args[1:])
	case "move":
		return runMove(args[1:])
	case "phone":
		return runPhone(args[1:])
	}
	fmt.Fprintf(os.Stderr, "factory: unknown command %q\n\n", args[0])
	usage(os.Stderr)
	return 2
}

var typedCommands = []string{"delete", "init", "forget", "doctor", "setup", "bootstrap", "install-hooks", "update", "node", "move", "phone"}

func listDroppedRemotes(args []string) {
	typed := len(args) == 0 || slices.Contains(typedCommands, args[0]) && !slices.Equal(args[:min(2, len(args))], []string{"node", "run"})
	if !typed {
		return
	}
	remotes, err := state.DropLegacyRemotes(state.ConfigDir())
	if err != nil || len(remotes) == 0 {
		return
	}
	fmt.Fprintln(os.Stderr, "factory: ssh remotes are gone, so these were removed from config.yml with default_remote and push:")
	for _, r := range remotes {
		fmt.Fprintln(os.Stderr, "  "+r)
	}
	fmt.Fprintln(os.Stderr, "Run factory node install on each machine that should keep running factories, then add it here with factory node add <its Tailscale name>.")
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

With no command, factory opens the hub, or jumps to it, and sets up the
repo you are in. Press n in the hub to create a factory.

commands:
  delete <id> [--repo path] [--yes]
              delete a factory: its pane, worktree, local branches, state,
              activity log, drafts and registry entry, so the id can be used
              again; pushed PRs and remote branches stay. --yes skips the first
              confirmation, never the one for unpushed or uncommitted work
  init [path] add a repo (default: the one you are in) without opening the
              hub: write its .factory/config.yml, ignore the factory's files
              in .git/info/exclude and list it in the hub; nothing is
              committed. factory does this by itself
              in a repo the hub does not list yet
  forget <path>
              stop listing a repo in the hub; its files stay
  doctor      check what the factory needs and print the fix commands
  setup [--tailscale] [--yes]
              install everything this machine needs: packages, GitHub and
              Claude Code logins, tools, plugins, skills and hooks, then run
              the node; it asks once, uses sudo only for system steps and
              skips what is already done; --tailscale also adds Tailscale,
              so your phone and other machines reach this node, and joins
              your other nodes; --yes takes every default without asking
  bootstrap   install the bundled skills, agents and hooks into the Claude
              config, and for Codex and OpenCode when they are installed
  install-hooks [--remove]
              add (or remove) only the factory hooks, in Claude's
              settings.json and Codex's hooks.json
  workflow next <state.json> <event.json>
              print the state proposal an event makes, without writing it
              (run by the supervisor)
  workflow commands <repo>
              print the repo's resolved build, lint, test and QA commands
  workflow watch --dir <factory dir> --run <run id> <pr number>...
              wait for CI on these PRs without model turns and queue the
              observations for the supervisor (run by the CI agent)
  workflow comments <state.json> [<dispositions.json>]
              refresh review comments and queue them, with any proposed
              dispositions, for the supervisor (run by the CI agent)
  stage start --role <role> --effect <id> --task <file> [--wait] [--factory id]
  stage send --role <role> --effect <id> --message <file> [--wait] [--factory id]
              run a stage agent on its own tool in a pane beside the
              supervisor, or send it a follow-up, for a committed workflow
              effect; prints the result file (run by the supervisor from
              its pane; --factory reaches a factory from outside its panes)
  stage ci <pr number>...
              wait for CI on these PRs, then type the
              result into the supervisor (for Codex and OpenCode)
  review reply --thread <id> [--resolve] [--factory id] [text…]
              answer a comment thread from the review page, and resolve it
              when the change is made (run by a stage agent)
  review threads [--page path] [--open] [--factory id]
              list this factory's comment threads
  review open [--factory id] [--for design] <url>
              check that any page, such as the running app, belongs to the
              factory and print its URL to open in your browser
  review stack [--for design]
              start the stack from the repo config on free ports, publish it
              on its own tailnet HTTPS port and print its URL
  review link <url>
              print a link to open in your browser
  attention resolve --dir <factory dir> [--waiting-on agent|ci|none] <id>
              check with the node that the item is open, then print the
              state.json edit that resolves it; the supervisor makes the edit
              (run by the supervisor)
  version     print the version and check the latest release on GitHub;
              --version prints only the local version
  update [--version <tag>] [--this-node]
              install the latest release, run its bootstrap, restart this
              machine's node on it, then ask every other node in the list to
              update and restart itself; --this-node updates only this machine
  node run [--owner login]
              run this machine's node in the foreground: it owns the
              factories, their panes, review pages and notifications whether
              or not a hub is open; --owner is the Tailscale login allowed
              through Tailscale Serve, read from Tailscale when not saved
  node install [--join | --no-join]
              check Tailscale, HTTPS and Funnel, run the node as a launchd
              agent or systemd user unit, then serve it at
              https://<machine>.<tailnet>.ts.net; the first install prints that
              URL as a QR; --join joins the node found on the tailnet and
              --no-join stays a node of its own, both without asking
  node uninstall
              stop and remove the service and its Serve mapping; factories,
              their state and phone notifications stay
  node status
              print the service, the running and configured versions, the
              port and the tailnet, also while the node is stopped
  node list   list the nodes that share this node's list
  node add <machine>
              check a tailnet machine and add its node to the list
  node remove <node>
              remove a node from the list
  node push-key export --file <path> | import [--file <path>] [--replace]
              copy the phone notification key to or from another node, so
              every node notifies the same phone subscription
  move <id> --to <node>
              move a factory to another node: it stops at a checkpoint,
              pushes its branches, and the node there resumes it with a fresh
              supervisor; Ctrl-C cancels it until it is released
  move --all --to <node>
              move every movable factory of this node, one at a time, and
              report which moved and why the others did not
  move <id> --to <node> --force [--yes]
              take a factory over from the last checkpoint on that node when
              its holder cannot be reached; prints the warning and asks first
              (--yes answers it for scripts)
  hub         show the node's factories in the factory tmux session
  host attach <view>
              show a pane the node prepared for another machine's hub (run
              over ssh by that hub)
  view       run a viewer pane of the hub (started by the hub)
  dialog      run a dialog popup of the hub (started by the hub)
  hook        record a hook event (run by Claude Code, Codex and OpenCode)
`)
}
