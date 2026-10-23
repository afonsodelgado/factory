# factory

One Go binary, `factory`, that runs every factory in one tmux session and ships the factory skill, its agents and hooks in `bundle/`.

## Commands

```sh
go build ./...
go vet ./...
go test ./...
go run ./cmd/factory --version
```

CI runs the same three commands on ubuntu and macos for every pull request.

## Isolation

Tests, manual runs and QA never touch the real machine:

- tmux: always `tmux -L <scratch name> -f /dev/null`. Never the default server, never the user's `tmux.conf`.
- `FACTORY_CONFIG_DIR`, `FACTORY_STATE_DIR`, `FACTORY_TMUX_SOCKET` and `CLAUDE_CONFIG_DIR` point at temporary directories.
- Never run `bootstrap` or `install-hooks` against `~/.claude`, and never write to `~/.config/factory` or `~/.local/state/factory`.
- Run `init` only on a temporary git repo with `HOME` and `FACTORY_CONFIG_DIR` pointing at temporary directories, never on a real repo.

Integration tests that need tmux start their own scratch server and kill it in `t.Cleanup`. They skip when `tmux` is missing only if `CI` is unset.

## tmux

Address panes by id (`%12`) and windows by name. Never assume a window or pane index: `base-index` differs between machines.

## Code

- No comments. Clear names and small functions instead.
- Readable conditionals: name the condition when it is not obvious.
- Measure terminal widths with `lipgloss.Width`, never `len()`.
- The app never writes `state.json`; the factory supervisor is its only writer.
- A factory is an id and a prompt. There are no tickets: never add tracker integration to the app or the bundle. The skill reads a tracker only when the user's prompt asks it to.
- `state.json` once had `ticket` where it now has `id`; `state.ParseState` reads both.

## Tests

Write the fewest tests that catch a real failure. Each test names the regression it prevents. Prefer extending an existing test file over adding one.
