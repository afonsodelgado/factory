# factory reference

Everything the [README](../README.md) leaves out: every command, setting and behaviour in detail. For the step-by-step guide, start with the README.

## Install by hand

`factory doctor` lists what is missing, each with the command that fixes it, and changes nothing. `factory bootstrap` installs the bundled skills, agents and hooks into your Claude config, backing up any file it replaces. `factory node install` installs the node. `bootstrap` never installs system packages, and running it again on an up-to-date machine changes nothing. It keeps the hooks you already have in `settings.json`. Restart running Claude Code sessions afterwards so they pick up the hooks. `factory install-hooks --remove` takes the hooks out again.

To build the binary yourself, use Go 1.26 or later:

```sh
go install github.com/afonsodelgado/factory/cmd/factory@latest
```

A binary you build has no release version: `factory version` shows `dev`, plus the commit and its date when you build from a checkout with `go build ./cmd/factory`. Built inside a git worktree, it shows the main checkout's commit, because Go reads the commit from the first `.git` directory above the source.

A permission prompt marks its factory "needs you" until the approved tool has run, on the thread that asked: the `PostToolUse` and `PostToolUseFailure` hooks report that, and `doctor` lists them as missing until `bootstrap` or `install-hooks` adds them. A session started before you updated keeps its old hooks until you restart it, and a prompt that was already open when you updated shows as answered.

## Update

```sh
factory version    # what is running, and the latest release
factory --version  # only what is running, with no network call
factory update     # update every node to the latest release
```

`factory update` downloads the release binary for your machine from GitHub over HTTPS, checks it against the release's `factory_checksums.txt`, and replaces the running one. It refuses a download that does not match, and it refuses when it cannot write to the binary's directory. Then it runs the new binary's `bootstrap`, so the skills, agents and hooks match it. When the node is installed and runs another version, `bootstrap` restarts it on the new binary.

Then update asks every other node in your list to do the same and prints how each one ended: updated, already current, unreachable, or too old to update remotely, in which case you run `factory update` on that machine once. `--this-node` updates only this machine, and `--version <tag>` installs that release instead of the latest. When this machine already runs the latest release, update still restarts a node that runs an older binary and still updates the other nodes.

Restarting a node stops a browser QA run or walkthrough in progress, which recovers as `node_restart`. Factory sessions keep running. A running hub keeps the old version: quit it with `q` and run `factory` again.

## Add a repo

The factory works in any git repo. In the hub, press `n`, then `+ add a repo…`. Type a path (`~` expands and `tab` completes directories), then press Enter to set it up and open its settings:

- It writes `.factory/config.yml` with blank build, lint and test commands. A factory detects blank commands when it runs, from the root Makefile, package scripts, `go.mod` or unambiguous CI steps, and asks for the ones it cannot find.
- It adds `.factory/` and the factory's other paths to `.git/info/exclude`.
- It remembers the main checkout in the hub's list, even when you enter a worktree path.

A non-git path or a setup failure stays in the dialog so you can correct it and retry. Adding a listed repo with a current config opens settings without repeating setup. A listed repo with no current config is set up first.

Press `ctrl+e` on any repo row to edit build, lint and test commands, QA mode, and browser start and health. A blank field shows the detected value and its source. `ctrl+s` saves; `esc` returns without saving. Both return to `n` with that repo selected. Saving preserves comments, existing key order and every other setting. Cleared commands remain as empty strings; terminal mode leaves start and health in the file.

Running `factory` inside a repo still sets it up and opens the hub. In this Here mode, `n` shows that repo and the add row; `↑↓` opens the full list to pick another repo. From outside a repo, the dialog lists them all. With no repos, it starts on the path field.

`factory forget <path>` removes a repo from the hub's list and leaves its files alone; a listed repo whose directory is gone drops out the next time the hub starts. Repos listed under `repos` in `config.yml` still show too.

Worktrees have no copy of the repo config, so the skill and its agents read it from the main checkout.

## Use

Every machine runs one node, which owns its factories: their tmux panes, review pages, attention, phone notifications and background work keep going whether or not a hub is open. `factory node install` runs it as a service; `factory node run` runs it in the foreground instead. The owner is the Tailscale login allowed through Tailscale Serve: `--owner`, `FACTORY_NODE_OWNER` or `node.owner` in `config.yml`. When `node.owner` or `node.hostname` is not set, `factory node run` reads them from `tailscale status` and saves them; it never replaces a saved owner. Offline, it runs on the saved settings, and with no owner saved it stops with setup guidance. The node listens on an owner-only `node.sock` in the state directory, which the hub and the CLI use, and on `127.0.0.1:7418` (`node.port`), which accepts only requests from Tailscale Serve on this machine that carry the owner's `Tailscale-User-Login`; until `node.hostname` names this machine's Tailscale name, requests through Serve get 421 with a message saying so. Every 10 seconds the node reads `tailscale serve status`; while any Funnel entry is on, or it cannot tell, it answers the owner's requests over the network only with a 403 naming each `tailscale funnel … off` fix, and `node.sock` keeps answering. A second `factory node run` for the same state directory refuses, and so does a node beside an open hub, `factory host run` or `factory serve` from an older release: stop those first; the node never stops them. `factory open`, the hub and every command that changes a factory refuse with this guidance when no node runs, before doing anything. `factory attention resolve` asks the node whether the item is open and prints the `state.json` edit for the supervisor to make; the node never writes `state.json`.

| Command | Does |
|---|---|
| `factory open` | opens the hub in this pane, or jumps to the hub that is already running |
| `factory open csv-export --prompt "Export contacts as CSV for ops."` | selects the factory `csv-export`, or creates it with that prompt |
| `factory open csv-export Export contacts as CSV for ops.` | the same: the words after the id are the prompt |
| `factory open csv-export` | without a prompt, a new factory's supervisor asks for one |
| `factory open csv-export --repo ~/dev/factory` | in a given repo instead of the one holding the factory or the current directory |
| `factory open csv-export --pipeline quick` | creates it with the `quick` pipeline instead of the default one |
| `factory open csv-export --stages plan,implement,review` | creates it with exactly these stages, plus the ones that always run |
| `factory open csv-export --agent engineer=sonnet:medium` | runs this factory's engineer on Sonnet at medium effort; repeat it for more agents |
| `factory delete csv-export` | deletes the factory and everything local to it, so the id can be used again |
| `factory init ~/dev/new-repo` | non-interactive equivalent for scripts and headless setups; add and configure repos interactively from the hub’s `n` dialog |
| `factory forget ~/dev/old-repo` | stops listing a repo in the hub; its files stay |

An id is made of lowercase letters, digits and dashes; `factory open "CSV Export"` uses `csv-export`. The command words (`open`, `init`, `doctor`, `setup`, `bootstrap`, `install-hooks`, `version`, `update`, `hub`, `host`, `node`, `move`, `view`, `dialog`, `hook`, `help`, `delete`, `forget`, `stage`, `review`) are refused as ids by the `n` dialog and by `factory open`; a `/factory doctor` typed straight into Claude is not guarded. Ids match exactly, and an existing factory is selected, never duplicated. The factory reads an issue tracker only when the prompt asks it to, for example "read Linear issue ENG-1234".

The binary replaces the old `factory` shell function; remove that function from your shell config.

Inside the hub, `?` lists every key. The hub opens on the factory that has waited longest for you. `g` jumps to the next one and puts you in its panel; once you have answered its last item, `⏎` in the panel takes you to the next. `M-0` returns to the factory list from any pane of the hub; `q` quits the UI and leaves every factory running. In the panel, `ctrl+n` and `ctrl+p` move to the next and previous attention item; `[` and `]` type into an answer and move between items only when the item has no form, such as a permission prompt. On a gate, `↓` past the last artifact highlights Approve, and `⏎` there approves with the notes, like `ctrl+a`, which a `C-a` tmux prefix can swallow. `p` pauses the selected factory: Claude stops and its pane closes, while the worktree, branch and state stay, and `w` wakes it again. `a` archives a done, gone or exited factory and can remove its worktree; the factory directory then moves to `.claude/factory-archive/<id>` in the main checkout. It stays listed as archived history (`H` shows it), and a new factory with the same id starts fresh.

An answer sent while the supervisor is busy waits in a queue, and is typed as soon as the supervisor waits at its prompt again; the factory list shows `answer queued` meanwhile. An answer still untyped after 10 minutes is given back: the item needs you again, with the reason, so send it again or answer in the session.

`d` deletes the selected factory in any status, and `factory delete <id> [--repo path]` does the same from the shell. Delete closes the factory's pane and its stage panes, stops the processes they started, closes its review tabs, force-removes its worktree (also when the folder was already removed by hand), force-deletes its local branch and every local branch of its stack (from `state.json`, the registry and gh-stack's own record), and removes its factory and QA directories (in the worktree, the main checkout and `.claude/factory-archive`), its `.ready` marker, activity log, drafts, hook records, launch scripts and registry entry. It never deletes `main`, `master` or the remote's default branch, and keeps a branch that another worktree has checked out. A new factory with the same id then gets that id and starts empty. Pushed PRs and remote branches are never touched. Delete always asks first; when the worktree has uncommitted changes or a branch has commits no remote branch has and whose changes are not already on the remote's default branch, it lists them and asks a second time, and `factory delete` refuses that second question when stdin is not a terminal. `--yes` skips only the first question. A delete that fails part way says which step failed and remembers the branches it still has to delete; running it again finishes the job. Deleting a factory that is already gone prints `Nothing to delete` and exits 0.

Browser QA runs on the machine that holds the factory, in its own headless Chrome; no QA job waits for or runs on a laptop. Finish or interrupt QA started by an older version before you upgrade, then start it again on its node.

Upgrading from a release with ssh remotes: the first command you type, such as `factory` or `factory doctor`, removes `remotes`, `default_remote` and `push` from `config.yml` and lists each remote it removed with the ssh host it had. It never guesses a Tailscale name from that host: run `factory node install` on each machine that should keep running factories, then `factory node add <its Tailscale name>` here. The ntfy push of `factory remote push` is gone; the phone app's notifications replace it, and factories already running on such a machine show up once its node joins the list.

### The node service

| Command | Does |
|---|---|
| `factory node install` | checks that Tailscale is logged in on an untagged device, HTTPS Certificates are on, every Funnel entry is off and `https://<machine>.<tailnet>.ts.net/` serves nothing else; saves `node.owner` and `node.hostname`; writes and loads the service; waits for the node to answer; then serves it with `tailscale serve --bg --https=443 http://127.0.0.1:<node.port>` |
| `factory node uninstall` | unloads and removes the service, and removes the Serve mapping only while it still points at the node |
| `factory node status` | prints whether the service is installed and running, the running and configured versions, the port, the tailnet, the URL and what keep-awake does now; it works while the node is stopped |
| `factory node install --join` / `--no-join` | answers the join question for scripts; without a terminal, install refuses to guess when it finds a node; `factory setup` always joins |
| `factory node list` | prints the node list, this machine first, with changes a node has not confirmed yet and the phone app's home |
| `factory node add <machine>` | checks a machine from `tailscale status` (short name, full name or tailnet address) and adds it |
| `factory node remove <node>` | removes a node from the list on every node; its factories keep running there |
| `factory node push-key export --file <path>` | writes this node's push key and phone subscriptions to a new file only you can read, for recovery; it refuses an existing file and prints only the key's fingerprint |
| `factory node push-key import [--file <path>] [--replace]` | takes a key from that file or standard input; it refuses to replace a key a phone is subscribed to unless you pass `--replace`, after which those phones subscribe again |

### Several nodes

The nodes keep one node list between them, in `roster.json` in each state directory, readable only by you. After the service and Serve are up, `factory node install` looks for your other nodes and asks `Join it? [Y/n]`; with none found, the machine starts a list of its own. Joining, `factory node add` and the hub's add all change that one list, and every node passes changes to the others it can reach every 15 seconds and right after a change. A node that was away catches up when it answers; until then the list shows what it has not confirmed. Removing a node wins over anything a node that was away still lists, so a removed node never comes back by itself: it learns it was removed when it next answers, lists only itself, and only an add from another node brings it back. Removing never stops its factories, and a node refuses to remove itself; `factory node uninstall` on that machine stops it.

Adding a machine reads `tailscale status` and asks only your own online, untagged macOS and Linux machines, at `https://<machine>.<tailnet>.ts.net`, for 2 seconds each; a typed name must be one that `tailscale status` lists, never a URL. Checking writes nothing anywhere: the machine runs its own readiness checks (tools, Claude login, git access, clones) and sends the results back, and a check with no answer in 10 seconds counts as failed. Funnel on, another owner, a tagged machine, another tailnet, an incompatible node API or a node already in another list refuse the add, with the fix. Missing tools, logins, git access or clones do not: the node joins, and creating a factory there refuses, naming each fix, until they pass. Nodes confirm each other by calling back each other's canonical HTTPS name with a one-use challenge that expires after 2 minutes; the routes for that refuse any request from a browser. The phone app's home stays the first node's URL, and a browser on it reads every node in the list; other origins get nothing. `node.target` in `config.yml` names the node this hub creates new factories on.

The nodes also keep your shared settings in sync, so a new machine needs no copying by hand:

- `rules.md`;
- the `agents`, `supervisor`, `pipelines`, `default_pipeline`, `stalled_after` and `codex` keys of `config.yml`;
- your repo list, with each repo's `.factory/config.yml`.

A node checks its own copies every 15 seconds. An edit on any node reaches every node it can reach, and the last edit wins. A node that receives a newer version saves the file it replaces as `<file>.<time>.bak`. It changes only those keys in `config.yml`, keeps your comments, and applies them without a restart. A node that joins takes the list's settings over its own. A repo another node lists is cloned on this node with this node's own git login, and gets that repo's config instead of a template.

Everything else stays on its machine: `repos` paths, `hub`, `alerts`, `icons`, `tmux`, `review`, `node`, `phone`, credentials, `.env` files and logins. Deleting a file or forgetting a repo changes only the machine you do it on. The settings live in `settings.json` in the state directory. A node on a release without this sync simply doesn't take part until `factory update` reaches it.

Adding a node also gives it the list's push key, the phones' subscriptions and the app home, sent node to node over the tailnet only to the machine that answered the challenge for that add; the key never reaches a browser, the output, a log or a factory directory, and it lands in the new node's state directory, readable only by you. Each node then notifies your phone on its own with the same key, so a phone subscribes once. When the machine already notifies a phone with a key of its own, the add says so first: `p` in the hub, or the phone's choice to switch the key, replaces it, and the phone subscribed to the old key subscribes again. Without that choice, or with `factory node install --join`, the machine joins but keeps its key, can't notify your phone and says so, and `factory node push-key export` on another node with `import --replace` there switches it later. Turning notifications on or off, a browser's renewed subscription and a push service retiring one reach every node in the list within 15 seconds; an older failure never removes a newer subscription. A node that learns it was removed forgets the list's phones and drops the notifications it had queued. A notification names the node that sent it, so the installed app opens that factory on its node even while the first node is down, and resolving one node's item never clears another node's notification.

On macOS the service is the launchd agent `~/Library/LaunchAgents/dev.factory.node.plist`. It starts when you log in: after a reboot the node waits for your login, and FileVault's login screen is not enough. On Linux it is the systemd user unit `~/.config/systemd/user/factory-node.service`, and install turns on lingering with `loginctl enable-linger` so the node starts at boot and keeps running after you log out; when that needs root, install says so and `sudo loginctl enable-linger <you>` fixes it. In a shell without a systemd user session, such as after `sudo -iu <user>`, factory finds that user's session in `/run/user/<uid>` by itself once lingering is on. Both run this binary's absolute path in your home directory with `PATH`, `HOME`, `SHELL`, locale and the `FACTORY_*`, `CLAUDE_CONFIG_DIR` and `CODEX_HOME` variables of the shell that installed it, and nothing else from its environment. The node's log is `logs/node.log` in the state directory, readable only by you. A systemd stop leaves the tmux server and its agents running.

Install refuses, before it changes anything, a tagged device (`sudo tailscale up --advertise-tags= --force-reauth`, then sign in as yourself), HTTPS Certificates off, a Funnel entry, which it lists with its own `tailscale funnel … off` command, and anything else on `https://<machine>.<tailnet>.ts.net/`. It adopts an existing mapping that already points at the node. If a step fails, it says which and records what it finished in `node-install.json` in the state directory: a service whose node never answers is unloaded again, and running install again completes the rest. The QR prints once, when an install first completes. Uninstall keeps factories, their panes, push keys, subscriptions, `config.yml` and every other Serve entry. On Linux, `tailscale serve` and `tailscale funnel` changes need sudo unless you are the Tailscale operator (`sudo tailscale set --operator=$USER`).

On a Mac, the node keeps the machine awake while it has an active factory and the Mac draws from external power: it holds a sleep assertion with `caffeinate -i`, so the Mac does not sleep when it sits idle. An active factory is one that is not paused, archived or done; a factory waiting for you counts, so the phone can still reach its review pages. The node checks every 15 seconds and lets go on battery, when the last active factory pauses or finishes, and when the node stops. `node.keep_awake: false` in `config.yml` turns it off. Keep-awake does not make closing a laptop's lid supported: a Mac with its lid closed may still sleep. Linux nodes do not apply it; keep the machine itself from suspending.

`factory doctor` checks the node: Tailscale login, that `node.owner` is that untagged user, `node.hostname`, HTTPS certificates, the Serve mapping, every Funnel entry, the service and its unit, the running process and that it runs this binary's version, lingering, keep-awake, the permissions of the state directory, sockets and log, and the push services, each failure with its own fix.

In the hub, `N` opens the Nodes popup: every node in the list with its state (`● this machine`, `● running`, `◌ asking…`, `! n to fix` or `↮ unreachable 12m`), version, factories and how many need you, the node new factories run on, and the phone app's URL with whether notifications are on. Each node answers for itself, so a slow one never holds up the others. `⏎` on this machine runs the node checks of `factory phone check`; on another node it shows that node's own readiness checks with the fix for each, and for a node that does not answer, whether Tailscale sees the machine offline or online with its node silent. `a` adds a node in two steps: pick one of your machines (offline, tagged and node-less machines show why they can't be picked) or type its name, then read its checks; only `⏎` on the checks adds it, and a refusal offers no `⏎`. `c` clones a repo a node is missing, with that node's git login; `d` makes a node the default for new factories (`node.target`); `x` removes a node after `y`, and refuses this machine with the `factory node uninstall` advice. The first time a node starts, the hub's footer says `<machine>'s node is running. N shows it and the URL your phone opens.`, once.

The phone app stays on the first node's URL and talks to every node in the list directly, each with its own 10-second deadline, its own event stream and a retry every 15 seconds, so one node being away never holds up the others. With one node it looks as before. With two or more it groups factories by repo and node (`↔ homelab`), shows an away node's factories in grey as of when it was last seen (`↮ build-box · lost 12m`) and counts them apart from failed ones; the top-bar chip shows the node count or `2 of 3` reachable and opens This phone, and a node that joins is announced once. Actions, receipts and drafts belong to the node that owns the factory and are never sent to another. An installed app still reads and acts on the other nodes while the first node is down; a first install, a phone with nothing saved and adding a node need it. This phone lists the nodes; a node's page shows its readiness checks, clones a missing repo with that node's git login and removes it from the list. Add a node picks from the same machines as the hub or takes a typed name, shows the checks, and offers Add only when nothing refuses it; it always goes through the first node's own address. A node removed from the list disappears from the phone along with everything the phone saved for it.

The hub follows every node in the list directly: this machine's over `node.sock`, the others at `https://<machine>.<tailnet>.ts.net` through Tailscale Serve, each on its own event stream, retried every 15 seconds while it does not answer. With two or more nodes, factories group by repo and node (`↔ homelab`, grey `↮ build-box · lost 12m`), the header names each node, and a lost node's factories count as unreachable, not failed; with one node the hub looks as before. Actions go only to the factory's own node and never to another when it fails. Another node's open items show in the panel like this machine's, and an answer, approval or reply goes to that node's own action route with an operation id; when the node does not answer, the panel says the outcome is unknown, asks that node's receipt again when it next answers and never sends it again by itself. A factory on another node rings the bell and sends the desktop notification when it starts needing you, but not when its node comes back from an outage. New factory lists every node, starts on `node.target` and refuses a node that is unreachable or not ready rather than starting elsewhere; on another node it uses that node's checkout of the repo when it has one and otherwise clones it there with that node's git login. A node joining is announced once. `⏎` on another node's factory asks that node to prepare a view of the factory's pane, then opens it in a proxy pane with `ssh <machine> factory host attach <view>`, the only thing ssh is used for; leaving or closing the proxy never stops the agent.

### Moving factories between nodes

| Command | Does |
|---|---|
| `factory move <id> --to <node>` | moves a factory of this node to another node in the list and follows the move until it runs there; Ctrl-C cancels it while it is not released yet |
| `factory move --all --to <node>` | moves every movable factory of this node, one at a time, and prints which moved and why each other one did not; a refusal never undoes a finished move, and Ctrl-C cancels the moves not released yet |
| `factory move <id> --to <node> --force` | takes a factory over on that node from the last checkpoint it has, when the node that holds it does not answer; prints the warning and asks first, and `--yes` answers for scripts |

A move first asks the target to check itself: protocol, disk, a clone of the repo with its own git login, its tools and agent login, and the repo's stack recipe in a scratch checkout. Then the supervisor writes a checkpoint, while it waits for you or between stages, and the node stops the factory's stages, stack and browser, refuses while an answer or an accepted action is unsettled or product files are uncommitted, pushes the working branch and every stack branch, and sends the factory folder, its QA evidence and the repo config to the target, which stages them without running anything. Credentials, `.env` files, cookies, browser profiles and conversations are never copied. At the commit the target checks itself again, this node records that it released the factory and stops all its processes and review stack mappings, and only then does the target take it over: it fetches the branches, checks out the same commits, copies the files, and starts a fresh supervisor with `/factory resume`, never the old conversation. The factory keeps its identity, this node stops listing it, and its files stay here until `factory delete <id>` removes them. A move back fast-forwards that worktree and its branches to the commits the factory made on the other node, and refuses, before anything changes, when they hold uncommitted changes or commits no git remote has.

Each step is written to `transfers/<operation>.json` in the state directory, so a move interrupted before the release finishes when you run the same `factory move` again, and the factory keeps running here meanwhile. When the target does not confirm after the release, the factory stays stopped on both nodes and the move names the way out: run `factory move <id> --to <node>` again once the target answers, which asks it what happened and never starts the factory twice, or take it back here with `--force`.

Who holds a moved factory is a small record on the factory's own git remote, in the ref `refs/factory/holders/<incarnation>`, separate from its branches: the node, a generation that grows with every move, the checkpoint and the move that wrote it, and nothing else. A node writes it only with git's compare-and-swap (`git push --force-with-lease`), so when two nodes claim a factory at once exactly one wins. Every node reads it before it starts a moved factory and every 30 seconds while it runs one: a node that finds another holder stops that factory's processes and no longer offers it, and after a restart a node keeps a moved factory from resuming, and refuses answers to it, until it has read the record. Answers and actions loaded before a move carry the old generation, and the node refuses them as stale.

A forced takeover needs a checkpoint on the node that takes over, either one a move staged there or the one a node keeps of the last factory it moved away; without one it is refused. Before each takeover it warns that the node holding the factory may only be out of reach, may keep running it until it reconnects and learns it lost it, and that work done after the checkpoint may be lost or diverge. Strict exclusion while the nodes cannot reach each other is not promised.

In the hub, `m` moves the selected factory: pick a node from the list, where unreachable nodes and nodes whose readiness checks fail can't be picked and nothing falls back to another node, then watch the move wait for a checkpoint, fail its preflight with each failing check, stop on the source while the target starts it, or name its recovery steps; `x` cancels it until the release, and `m` again on that factory reopens the running move rather than starting another. On a factory whose node does not answer, `m` offers a takeover instead and shows the node's warning, and only `y` starts it. `⏎` on another node in the Nodes popup offers `m` to move every factory of this machine there when its checks pass, with each factory's outcome as it settles and `x` to cancel the moves not released yet. In the phone app, a factory's page has Move to… (Take over… when its node is lost) with the same states and cancel, a move started on that phone keeps its progress on the factory's page and a banner in the list while the factory is in neither node's list, until the app reloads, and another node's page in This phone has Move all. A factory that a node holds but could not yet check against the holder record shows as held and unchecked, and the hub and the phone offer no actions on it.

`factory doctor` names moves that stopped before or after the release, with the command that finishes each, and any moved factory this node lists while the record says another node holds it; it says when it could not reach the git remote to check.

### The phone app

The phone app lists this machine's factories, tells you when one needs you, and lets you answer, approve, pause, wake, message a supervisor and review pages with the same comments as the desktop. It runs at `https://<machine>.<tailnet>.ts.net/` through [Tailscale Serve](https://tailscale.com/docs/reference/tailscale-cli/serve), so the phone needs Tailscale to act, but notifications arrive through the browser's push service on any network. The node serves it whenever it runs; there is no pairing. Every request must carry the node owner's Tailscale login, which Serve adds for a device signed in to Tailscale as that user. Anyone else on the tailnet gets a page that names the owner and shows nothing, and an app that was signed in as the owner clears what it saved. While Funnel is on, or before the node has confirmed it is off, the node refuses every request, and an installed app shows a Funnel page with each `tailscale funnel … off` fix and no factory data; it asks again every 15 seconds and opens by itself once Funnel is off. `N` in the hub shows the app's URL; press `P` for the Phone popup, or use the commands:

| Command | Does |
|---|---|
| `factory phone status` | prints the app's URL, the loopback port, whether the node runs and how many browsers have notifications on |
| `factory phone test` | asks the running node to send a test notification; "accepted" means the push service took it, not that the phone showed it |
| `factory phone check` | runs the node checks of `factory doctor`: Tailscale, owner, HTTPS, the serve mapping, every Funnel entry, the service, permissions and the push services, with a fix for each failure |

A factory never decides by itself that it is done. When its work is finished it raises a "Work done: mark it done or continue working?" item, and your phone gets one notification saying that the work is done. Mark it done (`ctrl+k` in the panel, or Mark done on the phone) to close the factory. Or send follow-up work: the engineer implements it, independent review and enabled QA run again, and PR verification must pass before another confirmation. Nothing is merged.

`factory node install` sets up the URL once per machine; never use Funnel. On iPhone, add the app to the Home Screen (iOS 16.4 or later) before turning on notifications. Each browser that turns notifications on is kept by its push endpoint; opening the app again re-registers the same subscription without turning it off.

Upgrading from a release with pairing: the first `factory node run` moves `phone.hostname` and `phone.port` to `node.hostname` and `node.port`, keeps the push keys, every subscription and the queued notifications, then deletes the pairing codes and device hashes. Paired phones keep getting notifications and open the app again over the tailnet as the owner; operations a phone sent before the upgrade stay readable until they settle.

### Reviewing designs and plans

Designs and plans are HTML pages in the factory directory, listed in the panel's Artifacts tab and on its gates. `ctrl+o` opens one in your browser on the node's review page at `https://<machine>.<tailnet>.ts.net/r/…`, the same page the phone app opens. The node never opens a browser itself. There:

- `C`, then click anything, leaves a comment on that element: a fix, a change or a question. *Comment on the whole page* in the comments list covers the rest.
- Comments are saved as you write them. **Send** delivers the ones not sent yet to the agent that owns the page: the designer or the architect, in its own pane.
- The agent changes the page, which reloads by itself, and answers each thread. Reply on a thread to keep the conversation going; your reply goes out with the next Send.
- While the agent works on what you sent, the factory leaves *needs you* and its phone notification is cleared. When the agent finishes, stops, or its pane dies, the factory needs you again and the phone is notified to review the page again.
- **Approve** appears while the page's gate is open and sends the same approval as the panel.

In a web repo, the design can instead be a working prototype built from the app's real components and stylesheet:

1. Set `design.prototype` in the repo config (see "Repo config").
2. The designer writes each variant into that git-ignored directory.
3. The hub runs the app's dev server; open the variants' URLs in your own browser. Until the node publishes dev servers on the tailnet, ask for changes through the design gate in the panel or the phone.

The panel shows each page's open threads. `factory review threads` lists them in a terminal. `factory review open <url>` prints the URL of any page, such as your app on its dev server, for you to open.

### Reviewing the design stack

Design review stacks remain available in repos with browser QA:

1. `factory review stack` makes the node start this worktree's stack from the repo config: `qa.start` on free loopback ports, then `qa.health`, `qa.seed` and one `qa.sign_in` to check that a browser could hold its cookies. A `qa.start` that returns 0 after starting the app in the background is fine; one that fails before `qa.health` answers is `stack_start`.
2. The node publishes the healthy app on its own tailnet HTTPS port, from 8443 to 8499, with `tailscale serve --bg --https=<port>` pointed at a loopback proxy. The proxy answers only your Tailscale login (and nothing while Funnel is on anywhere on the machine), strips Tailscale identity and `X-Factory-*` headers before the app sees a request, passes WebSockets, rewrites the app's redirects to the tailnet origin and adds the review overlay to its pages. It forwards only to that stack, never to a URL a request names, and the node's API is not on that origin.
3. The factory's Pages tab in the phone app shows the review stack. Opening it on the phone or another machine goes through a one-use link: the node runs `qa.sign_in` again for that browser and sets its cookies on the review stack's origin. The QA browser's session is never copied. A sign-in that prints a cookie for another site is refused as `phone_sign_in_unsupported`.

Comment on design elements in the overlay; comments go to the designer. Design changes require fresh approval. Pages keeps authenticated links to running review stacks.

A step that fails is named on the factory, in the phone app's Pages tab and as a `stack` item: `recipe_missing`, `stack_start`, `stack_health`, `seed`, `sign_in`, `phone_sign_in_unsupported`, `port_exhaustion`, `serve_mapping` or `node_restart`. The hub shows it as an activity note. The node journals each review stack's ports and Serve mapping in its state directory. Pausing, archiving, deleting or finishing the factory, and stopping the node, remove the mapping with `tailscale serve --https=<port> off` only while it still points at the review stack's proxy; after a crash the node does the same when it starts. It never resets Serve. Cookies are not port-scoped, so review stacks on one node share a cookie jar per browser.

## Configuration

`~/.config/factory/config.yml` (or `$FACTORY_CONFIG_DIR/config.yml`):

```yaml
repos: []  # optional; factory lists the repos you run it in by itself
hub:
  session: factory
  return_key: M-0
stalled_after: 20m
alerts:
  bell: false
  desktop: false
icons: nerd
review:
  chrome: ""
  browsers: 2
```

`alerts.desktop` sends a desktop notification when a factory needs you: through `osascript` on macOS and `notify-send` on Linux. The older `alerts.macos` key still works. `hub.session` names the session on factory's tmux server that holds the hub and the factories' panes. `icons: unicode` draws plain Unicode instead of Nerd Font icons. The node serves the factories' design and plan pages to the phone app and the hub through its own listener and to local tools through the owner-only `review.sock` in the state directory; it never opens a browser window. `review.chrome` names the Chrome binary for headless browser QA when it is not in the usual place. `review.browsers` is how many headless QA browsers the node runs at once; a QA stage over that waits, says so in the factory's activity, and starts when one finishes. Older `review.port` and `review.browser` keys are ignored. Every other value above is the default.

`stalled_after`, `agents`, `supervisor`, `pipelines`, `default_pipeline` and `codex` are shared: an edit on one node reaches your other nodes, which apply it without a restart (see [Several nodes](#several-nodes)). The other keys stay on the machine you set them on.

State (registry, hook files, drafts, the hub socket) lives in `~/.local/state/factory` or `$FACTORY_STATE_DIR`.

### Repo config

Add and configure repos from the hub: press `n`, then `+ add a repo…`, or `ctrl+e` on a listed repo. `ctrl+s` saves only the form's keys; `esc` cancels. A malformed file is shown as an error and cannot be overwritten. If the file changes while settings are open, reopen the form before saving.

Each repo's main checkout has its own config in `.factory/config.yml`. It is never committed: `.factory/` is listed in the repo's `.git/info/exclude`. It holds the repo's commands, QA and design settings, and it can override these global keys for that repo: `pipelines`, `default_pipeline`, `agents`, `supervisor`, `stalled_after` and `codex.writable_dirs`.

```yaml
commands: {build: make build, lint: make lint, test: make test}
qa: {mode: terminal}
supervisor: {effort: medium}
stalled_after: 45m
```

`agents` and `supervisor` override one field at a time, `pipelines` override by name, and `codex.writable_dirs` adds to the global list. A web repo can have the designer build prototypes from the app's real components:

```yaml
design:
  source: web
  files: [DESIGN.md]
  prototype:
    dir: apps/web/prototypes   # git-ignored, inside the web app's dev server root
    path: /prototypes          # where that dev server serves it
    css: apps/web/src/index.css
```

Browser QA runs on Mac and Linux nodes in a headless Chrome that the node starts for one QA stage at a time, with a new temporary profile, and drives only through Chrome DevTools MCP, pinned to `chrome-devtools-mcp@1.13.0` and run with usage statistics and CrUX lookups off. Before the QA pane starts, the node runs `qa.start`, waits for `qa.health` to answer `qa.health_status` (default 200) within `qa.startup_timeout` (default `2m`), then runs `qa.seed` and `qa.sign_in`, sets the printed cookies in that profile and writes an MCP config for that stage alone; it never edits your global Claude config. Seed prepares an isolated test environment and must be repeatable; sign-in prints the session cookies as a JSON list of `{name, value}`. Write `none` for an app without fixtures or sign-in: QA refuses to start, by name and before any browser launches, while either key is missing. Nobody can watch or type into that browser, so captchas and interactive 2FA are not supported: the steps run without a terminal, so a prompt fails at once. A failing step ends the stage with a `failed` result whose `data.failure` names it: `recipe_missing`, `npx_missing`, `stack_start`, `stack_health`, `seed`, `sign_in`, `browser_start`, `chrome_crash`, `qa_pane` (the QA pane closed without a result) or `node_restart`; the supervisor turns it into a failure item. The browser, the stack and the profile close when the QA result arrives, when the QA pane closes, when Chrome crashes, when the factory is paused, archived, deleted or finished, and when the node stops. The node records each job's stack process group, Chrome and profile in its state directory, so after a crash or restart it stops whatever of them still runs, removes the profile and fails that QA run with `node_restart`. Evidence outlives the profile: the node appends the console errors and failed requests of the page it opened to `.claude/qa/<factory directory>/qa-<round>/console.log` in the worktree as they happen, without query strings, and adds a final `screenshot.png`; QA saves its own screenshots there too. `factory doctor` checks `npx`, Chrome and that the MCP config uses the pinned version for repos with browser QA, and renders a local page in headless Chrome without network access; the node's readiness checks skip that render.

```yaml
qa: {mode: browser, url: 'http://localhost:${FACTORY_WEB_PORT}/', start: PORT=$FACTORY_WEB_PORT npm run dev, health: 'http://localhost:${FACTORY_WEB_PORT}/api/health', seed: npm run db:seed:test, sign_in: none}
```

`qa.url` makes a stack portable: the node picks a free loopback port for each stack, exports it to `qa.start`, `qa.seed` and `qa.sign_in` as `FACTORY_WEB_PORT` and `FACTORY_API_PORT`, expands `${FACTORY_WEB_PORT}` and `${FACTORY_API_PORT}` in `qa.url` and `qa.health`, and runs the commands in the worktree. `FACTORY_PUBLIC_ORIGIN` holds the origin a browser uses: the review stack's tailnet HTTPS origin, or the local one for QA. Point the dev server's allowed hosts or public URL at it if the app checks them. Without `qa.url` the stack uses the fixed `qa.web_port` and `qa.api_port`, so only one stack per repo runs on a node at a time; `factory doctor` warns about it. `qa.url` must be an `http` URL on `localhost` with a port. Sign-in reads credentials from the node's own environment; its cookies go only into the job's temporary profile or the browser that opened the one-use link, never into the factory directory.

`repos`, `hub`, `alerts`, `icons`, `tmux` and `review` belong to the machine; `factory doctor` reports them when a repo config sets them. The repo config itself is shared: every node that lists the repo gets the same `.factory/config.yml`. A repo set up before `.factory/` has `.claude/factory.yml`. The factory still reads it. In the hub, press `n`, then `+ add a repo…`, or use `ctrl+e` on its row: setup copies it to `.factory/config.yml` before opening settings, leaving the old file for factories already running there.

## Pipelines

A pipeline is the list of stages a factory runs. Two are built in, so they work with no config:

```yaml
pipelines:
  full:  [requirements, design, plan, implement, review, qa, pr, handoff]
  quick: [requirements, plan, implement, review, pr, handoff]
default_pipeline: full
```

Put `pipelines` and `default_pipeline` in `config.yml` to add presets or replace a built-in by name. A repo's `.factory/config.yml` can do the same, and it wins over `config.yml`. The keys are the eight stage keys above. Requirements and handoff always run. Every executable preset must include plan, implement, review and pr. Quick explicitly skips QA; design stays conditional. The name custom is reserved. An unknown stage key is a config error: `factory doctor` reports it and `factory open` refuses to start, naming the file and the key.

Pick the stages when you create a factory: the `n` dialog has a step of its own for them, "Stages & agents", between the id and the prompt. It starts at `default_pipeline`. `tab` loads the next preset, `↑↓` move through the stages, `space` checks or unchecks the selected one, and `⏎` goes on to the prompt. When the checked stages match no preset, the factory runs a `custom` pipeline. From the shell, use `factory open <id> --pipeline <name>` for a preset, or `--stages a,b,c` for an exact list; give one or the other, not both. An unknown name is refused with the list of available ones. The factory list shows the pipeline next to the stage when it is not the default.

A pipeline only removes stages. The gates stay: `quick` still shows you the plan and waits for your approval before any code is written. Every checked stage runs: the supervisor never skips one on its own judgement, so uncheck design for a change with no user interface. Skipped stages show as `–` in the stage track.

## Agent models

Each stage runs one agent, and each agent has a default model and effort in its `bundle/agents/factory-<role>.md` file. The roles are `analyst`, `designer`, `architect`, `engineer`, `reviewer`, `qa` and `ci`. To spend less, set them in `config.yml`, in a repo's `.factory/config.yml`, or for one factory:

```yaml
agents:
  analyst:  {model: sonnet}
  engineer: {model: sonnet, effort: medium}
  reviewer: {effort: medium}
  ci: {model: sonnet, effort: low}
```

Each layer overrides the one before it, one field at a time: the agent file, then `config.yml`, then the repo config, then the factory. Models are `haiku`, `sonnet`, `opus` and `fable`. Efforts are `low`, `medium`, `high`, `xhigh` and `max`. `factory doctor` reports an unknown role, model or effort, naming the file and the key.

For one factory, press `m` (model) or `e` (effort) on a stage's row in the dialog's "Stages & agents" step, or pass `--agent role=model:effort` to `factory open`. Either side can be empty, as in `--agent reviewer=:low`. A value set for this factory alone is shown in bold. The supervisor writes the result to `state.json`, so a resumed factory keeps its models. It passes them to the Agent tool on every spawn, and the files in `~/.claude/agents` stay as the bundle installed them.

## Codex and OpenCode

Every role, and the supervisor itself, can run on `claude`, `codex` or `opencode`. Set a `tool` with the model and effort:

```yaml
supervisor: {tool: claude}
agents:
  engineer: {tool: codex, model: gpt-6.1-sol, effort: high}
  reviewer: {tool: opencode, model: big-pickle}
codex:
  writable_dirs: [~/.cargo]   # extra dirs a Codex stage may write, beside the repo's git dir, the worktree's own git dir and the Go and npm caches
```

In the dialog's "Stages & agents" step, `t` cycles a row's tool, and the top row is the supervisor; `factory open` takes `--agent engineer=codex/gpt-6.1-sol:high` and `--supervisor codex/gpt-6.1-sol:high`. A layer that changes the tool drops the model and effort of the tool before it. Switching back to `claude` without a model or effort gives the role its agent file's defaults, such as `opus` at `xhigh` for the engineer; switching to Codex or OpenCode without them uses that tool's default when the stage starts. No API key is used anywhere: Codex offers the models your ChatGPT login lists (an API-key login is refused), OpenCode offers only its free models, and `qa` always runs on `claude`, because it drives Chrome. `factory doctor`, `factory open` and the dialog check a Codex model and effort against `~/.codex/models_cache.json`, and an OpenCode model and variant against the cached list of free models; when a list cannot be read yet, doctor warns that it can't check them.

A role on another tool runs in its own pane, and while it works the hub splits the session view and shows it beside the supervisor; the split closes when the stage writes its result. The supervisor starts and follows up with these roles through `factory stage`. Codex runs each stage unattended, in a sandbox that may write the worktree, the repo's git dir and the caches above, and reach the network. Chrome cannot start in that sandbox, so a Codex stage is told not to run browser tests and to name the ones it left for QA and CI.

With a Claude supervisor, `claude` roles are still subagents, except the iterating designer and the architect, which run in panes of their own so your comments reach them. OpenCode has no `/factory` command: it reads the factory skill from `~/.claude/skills`, so the app sends it `Use the factory skill for: /factory …` instead.

Your coding rules go in `~/.config/factory/rules.md` (or `$FACTORY_CONFIG_DIR/rules.md`). Every stage agent receives them, on every tool, before the repo's own `AGENTS.md` and `CLAUDE.md`.

Before the first factory on Codex, run `factory bootstrap`, then `codex` once in each repo and choose "Trust and continue", or Codex stops at that prompt. Factory starts Codex with `--dangerously-bypass-hook-trust`, so Codex never asks to trust hooks in a factory, and every enabled hook runs there, not only factory's. `factory doctor` checks the Codex login, that trust, and that a sandboxed commit, push and `gh` work. It runs the Codex and OpenCode checks whenever the config, a repo config or a factory that is not archived uses that tool, so a pick made only in the dialog is checked too.

## Iterative workflow upgrade

Deploy the binary, embedded phone app and bundled skills together. Node protocol is 3 and checkpoint schema is 2; mixed peers are rejected. Reload cached phone clients. Finish or restart old factories before upgrading: workflow schema 2 deliberately does not read old state, and rollback needs the previous binary with its original state. Keep an in-flight upgrade factory on its old binary/bundle until handoff.

The supervisor alone commits state proposals under a revision lock. Local implementation and review never publish. QA findings and regression gaps return to the original owning layer through review. Only the PR stage publishes drafts; CI fixes and follow-up work rerun independent review and enabled QA. Soft correction caps require explicit positive developer increments; refusal keeps the factory waiting. No skip bypasses a failing check or unreviewed change.

Command discovery resolves explicit per-field overrides, root Make targets, declared package scripts, Go defaults, then unambiguous CI commands. Missing commands and startup prerequisites form one question. Detection is recorded with sources and a digest, never silently written as explicit config during init. Supervisor commits require python3 for the bundled OS-locked commit script.

Handoff requires current local checks, review, enabled QA, open ready PRs, CI and repository policy. Confirmed empty check sets pass after the discovery grace as “Passed — no CI checks”; unknown or incomplete responses never pass. Required human approvals remain notes. Optional review comments default off; opt-in applies to all authors. Only the developer confirms done, bound to final head SHAs. Later head/base changes invalidate verification.

Remove obsolete installed factory-shepherd.md, factory-fixer.md and the coderabbit-comments skill directory from your agent configuration after upgrading. The installer does not delete user-installed files. A stale coderabbit repo key is ignored. No migration or reverse conversion is provided. No external service, account, database, worker fleet, merging or deployment is introduced. Local artifacts need backup, and recovery still requires a live owner and supervisor; a disconnected owner does not authorize a second mutating owner. Moves with unpublished branches are refused.
