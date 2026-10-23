# factory

One Go binary, `factory`, that runs every factory in one tmux session and ships the factory skill, its agents and hooks in `bundle/`.

The Go module, `cmd/`, `internal/` and `bundle/` stay at the root. Repo configs live in each repo's `.factory/config.yml`, never in this repository. The pnpm workspace includes `apps/*` and `packages/*`: `apps/web` (`@factory/web`) is the website, a static Astro site with React islands; add shared packages only when needed. Do not create placeholder directories or packages.

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

`web-build`, `web-lint`, `web-typecheck`, `web-test` and `web-dev` dispatch to `pnpm exec turbo run <task>`, and skip successfully before looking for pnpm only when `apps/web/package.json` is absent. With the site present, whole-repository build, lint, test and dev need Node, pnpm and Playwright's Chromium; `go-build`, `go-lint` and `go-test` stay Go-only. Root pnpm scripts call these guarded targets.

The site's `dev` serves `127.0.0.1:4321`, `preview` serves the production build on 4321, and the Playwright tests start their own preview on 4322. Every server uses a strict port and fails rather than moving.

Use Go 1.26 or later. For JavaScript, `.nvmrc` pins Node 24.22.1, `engines.node` supports 24.x, `packageManager` pins pnpm 10.33.0 and the root development dependency pins Turbo 2.11.10. Run `nvm install`, `nvm use`, `npm install --global pnpm@10.33.0`, `pnpm install`, then `pnpm --filter @factory/web exec playwright install chromium`. CI installations should use `pnpm install --frozen-lockfile`.

`pnpm turbo run build` is the advanced direct command. Turbo caches `dist/**`, `build/**` and Astro's generated `.astro/**`, plus lint/typecheck/test results; typecheck runs `astro sync` first so it works from a clean checkout, and test depends on the package's own build. `SITE_URL` is the only environment input: an http(s) origin for the sitemap, robots and social image URLs, defaulting to `http://localhost:4321`. Never add a canonical link or a production domain until the site is made public. There is no coverage output. Dev is persistent and uncached. No remote cache is configured. Repo config autodetection still prefers `go.mod` and selects the Go commands.

CI starts on every pull request and push to `main`, without workflow-level path filters. The `changes` job uses the PR changed-files API or the previous pushed head for main pushes. Go changes run `make go-build`, `make go-lint` and `make go-test` on Ubuntu and macOS with tmux/jq and no Node. Web changes install the pinned Node/pnpm tooling, frozen lockfile and Playwright Chromium on Ubuntu, then run `make web-lint`, `make web-typecheck`, `make web-test` and `make web-build`.

Go owns root `*.go`, `cmd/**`, `internal/**`, `bundle/**`, `go.mod`, `go.sum` and the release workflow. Web owns `apps/**`, `packages/**`, root pnpm manifests/lockfile, `turbo.json` and `.nvmrc`. `Makefile`, `.gitignore` and the CI workflow select both. Root docs select neither. Include all data in owned directories; add new build inputs to the inline CI filters and README ownership table together.

The final `ci` job always evaluates detection and both workload results. Detection must succeed; workloads may succeed or skip, but failures, cancellations and unknown outcomes fail the aggregate. Require `ci` in branch protection and manually remove old matrix requirements when adopting it. No workflow can guarantee reporting through platform outages, manual cancellation or commit-level CI-skip directives.

## Releases

Automatic main releases select root `*.go`, `cmd/**`, `internal/**`, `bundle/**`, `go.mod`, `go.sum` and `.github/workflows/release.yml`. All files in owned directories count, including embedded Markdown and fixtures. Site, shared JavaScript packages, JS tooling, Makefile, CI-only and root documentation changes alone do not release. Pushes of `v*` tags bypass path filtering and preserve the explicit tag.

Version selection skips an already-tagged main HEAD, otherwise increments the highest stable semantic-version tag's patch, starting at `v0.1.0`. Keep the existing four release binaries, asset names and `main.version` linker variable unchanged. No website deployment is configured.

## Isolation

Tests, manual runs and QA never touch the real machine:

- tmux: always `tmux -L <scratch name> -f /dev/null`. Never the default server, never the user's `tmux.conf`.
- `FACTORY_CONFIG_DIR`, `FACTORY_STATE_DIR`, `FACTORY_TMUX_SOCKET` and `CLAUDE_CONFIG_DIR` point at temporary directories.
- Never run `bootstrap` or `install-hooks` against `~/.claude`, and never write to `~/.config/factory` or `~/.local/state/factory`.
- `factory` and `factory open` set up the repo they run in, as `init` does. Run `init`, `factory` and `factory open` only in a temporary git repo, with `HOME`, `FACTORY_CONFIG_DIR` and `FACTORY_STATE_DIR` pointing at temporary directories, never in a real repo.

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
