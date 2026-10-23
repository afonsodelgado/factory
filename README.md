# factory

`factory` runs every factory in one tmux session. A factory is a Claude Code session running `/factory` in its own worktree; the app lists them on the left, shows the selected one's live session on the right, and puts a "Needs attention" panel under it for questions, gates and the rest.

## Install

Download the binary for your machine from the private repo's releases (`darwin_arm64`, `darwin_amd64`, `linux_amd64` or `linux_arm64`):

```sh
gh release download --repo afonsodelgado/factory --pattern 'factory_darwin_arm64' --output ~/.local/bin/factory
chmod +x ~/.local/bin/factory
```

Or build it with Go 1.26 or later:

```sh
GOPRIVATE=github.com/afonsodelgado/* go install github.com/afonsodelgado/factory/cmd/factory@latest
```

Then check the machine and install the factory into your Claude config. `doctor` lists what is missing, each with the command that fixes it, and changes nothing; `bootstrap` installs the bundled skills, agents and hooks, backing up any file it replaces.

```sh
factory doctor
factory bootstrap
```

`bootstrap` never installs system packages; it prints the `doctor` commands for those. Running it again on an up-to-date machine changes nothing, and it keeps the hooks you already have in `settings.json`. Restart running Claude Code sessions afterwards so they pick up the hooks. `factory install-hooks --remove` takes the hooks out again.

Each repo needs a factory profile at `.claude/factory.yml`; the bundled skill's `templates/factory.yml` is the template, and `profiles/cowork/` has cowork's.

## Use

| Command | Does |
|---|---|
| `factory open` | attaches to the hub, starting it if needed |
| `factory open ENG-1234` | selects ENG-1234's factory, or creates it |
| `factory open ENG-1234/cors-fix` | the same, for a ticket with a feature name |
| `factory open csv-export` | a feature with no ticket; the supervisor asks for its brief |
| `factory open ENG-1234 --repo ~/dev/factory` | in a given repo instead of the one holding the ticket or the current directory |

Tickets match exactly: `ENG-740` never opens `eng-7400-…`. An existing factory is selected, never duplicated.

The binary replaces the old `factory` shell function; remove that function from your shell config.

Inside the hub, `?` lists every key. `M-0` returns to the factory list from any pane of the hub; `q` quits the UI and leaves every factory running.

## Configuration

`~/.config/factory/config.yml` (or `$FACTORY_CONFIG_DIR/config.yml`):

```yaml
repos: [~/dev/cowork, ~/dev/factory]
hub:
  session: factory
  return_key: M-0
stalled_after: 20m
alerts:
  bell: false
  macos: false
icons: nerd
claude:
  remote_control: true
```

`return_key: prefix` binds `prefix f` instead of `M-0`; `icons: unicode` draws plain Unicode instead of Nerd Font icons. Every value above is the default.

State (registry, hook files, drafts, the hub socket) lives in `~/.local/state/factory` or `$FACTORY_STATE_DIR`. Each repo carries its factory profile in `.claude/factory.yml`.
