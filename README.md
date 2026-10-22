# factory

`factory-tui` runs every factory in one tmux session. A factory is a Claude Code session running `/factory` in its own worktree; the app lists them on the left, shows the selected one's live session on the right, and puts a "Needs attention" panel under it for questions, gates and the rest.

## Use

```sh
factory-tui open                    # attach to the hub, starting it if needed
factory-tui open ENG-1234           # select ENG-1234's factory, or create it
factory-tui open ENG-1234/cors-fix  # a ticket with a feature name
factory-tui open csv-export         # a feature with no ticket
factory-tui open ENG-1234 --repo ~/dev/factory
```

Tickets match exactly: `ENG-740` never opens `eng-7400-…`. An existing factory is selected, never duplicated.

Replace the old `factory` shell function with this one line:

```sh
factory() { factory-tui open "$@"; }
```

Inside the hub, `?` lists every key. `M-0` returns to the factory list from any pane of the hub; `q` quits the UI and leaves every factory running.

## Configuration

`~/.config/factory-tui/config.yml` (or `$FACTORY_TUI_CONFIG_DIR/config.yml`):

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

State (registry, hook files, drafts, the hub socket) lives in `~/.local/state/factory-tui` or `$FACTORY_TUI_STATE_DIR`. Each repo carries its factory profile in `.claude/factory.yml`.
