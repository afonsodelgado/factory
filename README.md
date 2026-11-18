<p align="center">
  <img src="docs/images/banner.svg" alt="factory: manage your agents, like a factory. Stay in control when it matters." width="100%">
</p>

<p align="center">
  <a href="https://github.com/afonsodelgado/factory/releases/latest"><img alt="Latest release" src="https://img.shields.io/github/v/release/afonsodelgado/factory?style=flat-square&color=7aa2f7&labelColor=1a1b26"></a>
  <a href="https://github.com/afonsodelgado/factory/actions/workflows/ci.yml"><img alt="CI" src="https://img.shields.io/github/actions/workflow/status/afonsodelgado/factory/ci.yml?branch=main&style=flat-square&label=ci&labelColor=1a1b26"></a>
  <a href="go.mod"><img alt="Go 1.26 or later" src="https://img.shields.io/badge/go-1.26%2B-7dcfff?style=flat-square&labelColor=1a1b26"></a>
  <a href="LICENSE"><img alt="MIT license" src="https://img.shields.io/github/license/afonsodelgado/factory?style=flat-square&color=9ece6a&labelColor=1a1b26"></a>
  <img alt="macOS and Linux" src="https://img.shields.io/badge/macOS%20%C2%B7%20Linux-bb9af7?style=flat-square&labelColor=1a1b26">
  <img alt="Claude Code, Codex and OpenCode" src="https://img.shields.io/badge/Claude%20Code%20%C2%B7%20Codex%20%C2%B7%20OpenCode-ff9e64?style=flat-square&labelColor=1a1b26">
</p>

<p align="center">
  <b>You're the manager.</b> Give the instructions and your <b>factory</b> takes it to a tested, ready-to-deploy feature, and only stops when it needs you.
</p>

<p align="center">
  <a href="#how-it-works">How it works</a> ·
  <a href="#1-set-up-your-machine">Your machine</a> ·
  <a href="#2-add-a-remote-server">Remote servers</a> ·
  <a href="#3-set-up-your-phone">Phone</a> ·
  <a href="docs/reference.md">Reference</a>
</p>

<p align="center">
  <img src="docs/images/hub.png" alt="The factory hub: every factory in one tmux window, with the selected factory's live session on the right and a panel under it for the question it is waiting on." width="100%">
</p>

## How it works

A **factory** is an id and a prompt: "csv-export: let ops export contacts as CSV". It gets its own branch and worktree, and a team of agents takes it from requirements to tested, open pull requests. It stops only when it needs you.

```
requirements → design ↔ plan → implement ↔ review → qa → pr → handoff
   you answer    you approve                                    you confirm
```

- **You stay in control.** You answer the requirements questions, approve the design and the plan before any code is written, and confirm at the end. Merging is always yours.
- **One agent per stage.** Each stage can run on Claude Code, Codex or OpenCode, with its own model and effort. Everything runs on your own subscriptions, with no API keys.
- **Run as many as you like.** The hub shows every factory in one window, and `g` jumps to the one that needs you.
- **Run them anywhere.** Each machine runs a **node**, an always-on service that owns its factories. Add a home server or a VPS as another node, and the hub and your phone show the factories of every node together.
- **Nothing lands in your repo.** The factory keeps its files in `.factory/` and `.claude/`, both out of git.

## Before you start

You need:

- a Mac or a Linux machine;
- a [Tailscale](https://tailscale.com) account (free), which connects your machines and your phone privately;
- a GitHub account;
- a Claude subscription, for Claude Code.

## 1. Set up your machine

**1. Install factory.** In a terminal, run:

```sh
curl -fsSL https://raw.githubusercontent.com/afonsodelgado/factory/main/install.sh | sh
```

It installs `factory` in `~/.local/bin` and starts `factory setup`. On a Mac, install [Homebrew](https://brew.sh) and the [Tailscale app](https://tailscale.com/download/mac) first, and log in to Tailscale.

**2. Answer setup.** Setup lists what it will install and asks once. Then it walks you through the logins:

- **Tailscale:** open the link it prints and log in.
- **HTTPS for your tailnet:** the first time only, turn on HTTPS Certificates at [login.tailscale.com/admin/dns](https://login.tailscale.com/admin/dns), then press Enter.
- **GitHub:** open the link it prints and enter the code.
- **Git:** confirm your name and email for commits.
- **Claude Code:** it opens Claude. Type `/login`, finish in the browser, then type `/exit`.

At the end, setup prints this machine's node URL. If it stops with a problem, fix what it says and run `factory setup` again: it skips everything already done.

**3. Open the hub.**

```sh
factory
```

**4. Add a repo.** In the hub, press `n`, choose `+ add a repo…`, type the path to a git repo and press Enter. Fill in its build, lint and test commands, or leave them blank and the factory finds them. Press `ctrl+s` to save.

**5. Start a factory.** Press `n` again, pick the repo, type an id such as `csv-export`, choose the stages, and write what you want. Or, from the shell:

```sh
factory open csv-export "Let ops export contacts as CSV from the contacts page."
```

The factory starts on its own. When it needs you, its row is marked and `g` takes you to the question. Press `?` in the hub to see every key, and `q` to close the hub. Factories keep running when the hub is closed.

## 2. Add a remote server

A home server or a VPS can run factories while your laptop sleeps. It becomes a second node: you start and answer its factories from your laptop's hub or your phone, as if they ran locally.

**1. Prepare the server.** Use Ubuntu Server 24.04 or another recent Linux. A separate user for the agents keeps them away from the rest of the server. From your admin account:

```sh
sudo adduser factory
```

**2. Install factory as that user.**

```sh
sudo -iu factory
curl -fsSL https://raw.githubusercontent.com/afonsodelgado/factory/main/install.sh | sh
```

The `factory` user can't use `sudo`, so setup prints the few commands that need root (system packages, Tailscale and starting the node at boot) and stops. Copy them, run them from your admin account, then go back to the `factory` user and run:

```sh
factory setup
```

Log in to Tailscale, GitHub and Claude Code as in part 1. Use the same Tailscale account as your laptop.

**3. It joins your other nodes by itself.** Setup finds the node on your laptop and joins its list, so keep your laptop on with its node running. The server then copies your shared settings from it:

- your coding rules;
- your agent, model and pipeline settings;
- your repos, which it clones with its own GitHub key, together with each repo's factory config.

Nothing else is copied: no credentials, `.env` files or logins. From then on, a change to these settings on any node reaches the others within seconds.

**4. Use it from your laptop.** In the hub, press `N` to see your nodes. To start new factories on the server by default, select it and press `d`. To move a running factory there, select the factory and press `m`.

## 3. Set up your phone

The phone app shows every factory on every node, notifies you when one needs you, and lets you answer, approve and review from anywhere.

1. Install the Tailscale app on your phone and log in with the same account.
2. In your phone's browser, open the URL of your first node, the machine you set up in part 1: `https://<machine>.<tailnet>.ts.net`. In the hub, `N` shows it. There is nothing to pair: Tailscale already tells the node it's you.
3. Add it to your Home Screen. On iPhone, use Share → Add to Home Screen, and open it from there.
4. In the app, turn on notifications.
5. To check that notifications arrive, run this on the node's machine:

   ```sh
   factory phone test
   ```

The app lives on that first node and reaches your other nodes from there.

## Keep it up to date

```sh
factory update
```

This updates this machine, restarts its node, and updates every other node in your list.

## When something is wrong

```sh
factory doctor
```

It checks everything factory needs and prints a fix command for each problem. It changes nothing. `factory node status` shows whether this machine's node runs and which version it runs.

## Settings

Most people never need to change these. The few you might:

- **Coding rules:** write them in `~/.config/factory/rules.md`. Every agent follows them, on every node.
- **Cheaper models:** in `~/.config/factory/config.yml`:

  ```yaml
  agents:
    engineer: {model: sonnet, effort: medium}
    reviewer: {effort: medium}
  ```

- **A repo's commands and QA:** in the hub, press `n`, select the repo and press `ctrl+e`.

Every setting, command and key is in the [reference](docs/reference.md). To work on factory itself, see [development](docs/development.md).
