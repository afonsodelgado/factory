# factory

One Go binary, `factory`, that runs every factory in one tmux session and ships the factory skill, its agents and hooks in `bundle/`.

The Go module, `cmd/`, `internal/`, `bundle/` and `profiles/` stay at the root. The pnpm workspace includes `apps/*` and `packages/*`: reserve `apps/web` for the future website and add shared packages only when needed. Do not create placeholder directories or packages.

## Commands

```sh
make build
make lint
make test
make dev
go build ./...
go vet ./...
go test ./...
go run ./cmd/factory --version
```

`make` defaults to `build`. `go-build`, `go-lint` and `go-test` run the native Go commands above. Whole-repository build, lint and test also run the matching web targets; lint includes web typecheck. `make dev` builds Go before `web-dev`, including under `-j`, and never launches the interactive CLI. Run `go run ./cmd/factory` separately only when intentionally opening the hub.

`web-build`, `web-lint`, `web-typecheck`, `web-test` and `web-dev` skip successfully before looking for pnpm when `apps/web/package.json` is absent. Go-only contributors need no JavaScript tooling. Root pnpm scripts call these guarded targets; each target dispatches directly to `pnpm exec turbo run <task>` when the site exists.

Use Go 1.26 or later. For JavaScript, `.nvmrc` pins Node 24.22.1, `engines.node` supports 24.x, `packageManager` pins pnpm 10.33.0 and the root development dependency pins Turbo 2.11.10. Run `nvm install`, `nvm use`, `npm install --global pnpm@10.33.0`, then `pnpm install`. CI installations should use `pnpm install --frozen-lockfile`.

After a package exists, `pnpm turbo run build` is the advanced direct command. Turbo caches generic `dist/**` and `build/**` outputs and lint/typecheck/test results; dev is persistent and uncached. The future website must add framework outputs, generated-type prerequisites, environment inputs and coverage declarations. No remote cache is configured. Factory profile autodetection still prefers `go.mod` and selects the Go commands.

CI starts on every pull request and push to `main`, without workflow-level path filters. The `changes` job uses the PR changed-files API or the previous pushed head for main pushes. Go changes run `make go-build`, `make go-lint` and `make go-test` on Ubuntu and macOS with tmux/jq and no Node. Web changes install the pinned Node/pnpm tooling and frozen lockfile on Ubuntu, then run `make web-lint`, `make web-typecheck`, `make web-test` and `make web-build`; absent site tasks skip after installation.

Go owns root `*.go`, `cmd/**`, `internal/**`, `bundle/**`, `profiles/**`, `go.mod`, `go.sum` and the release workflow. Web owns `apps/**`, `packages/**`, root pnpm manifests/lockfile, `turbo.json` and `.nvmrc`. `Makefile`, `.gitignore` and the CI workflow select both. Root docs select neither. Include all data in owned directories; add new build inputs to the inline CI filters and README ownership table together.

The final `ci` job always evaluates detection and both workload results. Detection must succeed; workloads may succeed or skip, but failures, cancellations and unknown outcomes fail the aggregate. Require `ci` in branch protection and manually remove old matrix requirements when adopting it. No workflow can guarantee reporting through platform outages, manual cancellation or commit-level CI-skip directives.

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
