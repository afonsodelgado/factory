# factory

One Go binary, `factory`, that runs every factory in one tmux session and ships the factory skill, its agents and hooks in `bundle/`.

The Go module, `cmd/`, `internal/` and `bundle/` stay at the root. Repo configs live in each repo's `.factory/config.yml`, never in this repository. The pnpm workspace includes `apps/*` and `packages/*`: `apps/web` (`@factory/web`) is the website, a static Astro site with React islands; `apps/pwa` (`@factory/pwa`) is the phone app, a Vite and React PWA embedded in the binary from `bundle/pwa/dist`; add shared packages only when needed. Do not create placeholder directories or packages.

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

`make` defaults to `build`. `go-build`, `go-lint` and `go-test` run the native Go commands above. Whole-repository build, lint and test also run the matching `web-*` and `pwa-*` targets; lint includes both typechecks. `make build` builds both apps, runs the uncached `pwa-embed` (copies `apps/pwa/dist` to the git-ignored `bundle/pwa/dist` and checks its build stamp), then builds Go, even under `-j`. A Go-only build embeds only the tracked `bundle/pwa/unavailable.html`, which the node then serves in place of the phone app. `make dev` builds Go before `web-dev`, including under `-j`, and never launches the interactive CLI. Run `go run ./cmd/factory` separately only when intentionally opening the hub.

`web-*` and `pwa-*` targets dispatch to `pnpm exec turbo run <task> --filter=<package>`, and skip successfully before looking for pnpm only when that app's `package.json` is absent. With the site present, whole-repository build, lint, test and dev need Node, pnpm and Playwright's Chromium; `go-build`, `go-lint` and `go-test` stay Go-only. Root pnpm scripts call these guarded targets.

The site's `dev` serves `127.0.0.1:4321`, `preview` serves the production build on 4321, and the Playwright tests start their own preview on 4322. The phone app's `dev` serves 4323 and its Playwright tests start their own fake gateway on 4324. Every server uses a strict port and fails rather than moving.

Use Go 1.26 or later. For JavaScript, `.nvmrc` pins Node 24.22.1, `engines.node` supports 24.x, `packageManager` pins pnpm 10.33.0 and the root development dependency pins Turbo 2.11.10. Run `nvm install`, `nvm use`, `npm install --global pnpm@10.33.0`, `pnpm install`, then `pnpm --filter @factory/web exec playwright install chromium`. CI installations should use `pnpm install --frozen-lockfile`.

`pnpm turbo run build` is the advanced direct command. Turbo caches `dist/**`, `build/**` and Astro's generated `.astro/**`, plus lint/typecheck/test results; typecheck runs `astro sync` first so it works from a clean checkout, and test depends on the package's own build. `SITE_URL` is the only environment input: an http(s) origin for the sitemap, robots and social image URLs, defaulting to `http://localhost:4321`. Never add a canonical link or a production domain until the site is made public. There is no coverage output. Dev is persistent and uncached. No remote cache is configured. Repo config autodetection still prefers `go.mod` and selects the Go commands.

CI starts on every pull request and push to `main`, without workflow-level path filters. The `changes` job uses the PR changed-files API or the previous pushed head for main pushes. Go changes install tmux/jq and the pinned Node/pnpm tooling on Ubuntu and macOS, run `make pwa-build` and `make pwa-embed`, then `make go-build`, `make go-lint` and `make go-test`. Web changes install the pinned Node/pnpm tooling, frozen lockfile and Playwright Chromium on Ubuntu, then run lint, typecheck, test and build for both apps.

Go owns root `*.go`, `cmd/**`, `internal/**`, `bundle/**`, `go.mod`, `go.sum` and the release workflow. Web owns `apps/**` and `packages/**`. `apps/pwa/**`, `apps/web/public/fonts/**`, root pnpm manifests/lockfile, `pnpm-workspace.yaml`, `turbo.json` and `.nvmrc` feed the embedded phone app, and `bundle/review/**` feeds its browser tests, so they select both, as do `Makefile`, `.gitignore` and the CI workflow. Root docs select neither. Include all data in owned directories; add new build inputs to the inline CI filters and README ownership table together.

The final `ci` job always evaluates detection and both workload results. Detection must succeed; workloads may succeed or skip, but failures, cancellations and unknown outcomes fail the aggregate. Require `ci` in branch protection and manually remove old matrix requirements when adopting it. No workflow can guarantee reporting through platform outages, manual cancellation or commit-level CI-skip directives.

## Releases

Automatic main releases select root `*.go`, `cmd/**`, `internal/**`, `bundle/**`, `go.mod`, `go.sum`, `.github/workflows/release.yml` and the phone app's inputs (`apps/pwa/**`, `apps/web/public/fonts/**`, root pnpm manifests/lockfile, `pnpm-workspace.yaml`, `turbo.json`, `.nvmrc`). All files in owned directories count, including embedded Markdown and fixtures. Other site, shared JavaScript package, Makefile, CI-only and root documentation changes alone do not release. Releases build and embed the phone app before testing and fail when a binary lacks its build stamp. Pushes of `v*` tags bypass path filtering and preserve the explicit tag.

Version selection skips an already-tagged main HEAD, otherwise increments the highest stable semantic-version tag's patch, starting at `v0.1.0`. Keep the existing four release binaries, asset names and `main.version` linker variable unchanged. No website deployment is configured.

## Isolation

Tests, manual runs and QA never touch the real machine:

- tmux: always `tmux -L <scratch name> -f /dev/null`. Never the default server, never the user's `tmux.conf`.
- `FACTORY_CONFIG_DIR`, `FACTORY_STATE_DIR`, `FACTORY_TMUX_SOCKET` and `CLAUDE_CONFIG_DIR` point at temporary directories.
- Chrome: tests and QA start it headless with a temporary profile, never the user's profile or `$FACTORY_STATE_DIR/chrome` of the real machine.
- Never run `bootstrap` or `install-hooks` against `~/.claude`, and never write to `~/.config/factory` or `~/.local/state/factory`.
- `factory node install`, `uninstall`, `status` and `run`, `factory doctor`, `factory bootstrap` and `factory phone check` call `tailscale`, `launchctl`, `systemctl` and `loginctl` by name, and a running node also calls `pmset` and `caffeinate` unless `node.keep_awake` is false. Run them only with a temporary `HOME` and fakes of those commands first on `PATH`; tests inject a runner instead, and tests that start a node set `keep_awake: false`. Never let them reach the real service manager or tailnet.
- `factory` and `factory open` set up the repo they run in, as `init` does. Run `init`, `factory` and `factory open` only in a temporary git repo, with `HOME`, `FACTORY_CONFIG_DIR` and `FACTORY_STATE_DIR` pointing at temporary directories, never in a real repo.

Integration tests that need tmux start their own scratch server and kill it in `t.Cleanup`. They skip when `tmux` is missing only if `CI` is unset.

Tests with several nodes use `internal/node/nodetest`: in-process nodes with their own temporary state directories behind a fake tailnet that routes `https://<machine>.<tailnet>.ts.net` to each node's listener, sets the caller's `Tailscale-User-Login` as Serve would (none for a tagged machine), and answers each machine's own `tailscale status --json` and `serve status --json`. Two real `factory node run` processes cannot reach each other without a tailnet; never add a flag or variable that redirects node-to-node HTTPS or relaxes certificate checks to get around that.

## tmux

Address panes by id (`%12`) and windows by name. Never assume a window or pane index: `base-index` differs between machines.

Hub popups such as `N` (Nodes) and `P` (Phone) are tmux client overlays, so `capture-pane` on the hub's server never shows them, and they run with that server's global environment. To see one by hand, start the inner scratch server from a shell whose `HOME`, `FACTORY_*` and `CLAUDE_CONFIG_DIR` are temporary and whose `PATH` puts the fakes first, run `factory node run` from the same environment, then run the hub in a second scratch server from a temporary repo with `TMUX` unset (`tmux -L <outer> -f /dev/null new -d -x 120 -y 40 factory`), send the key there and capture the outer pane.

## Code

- No comments. Clear names and small functions instead.
- Readable conditionals: name the condition when it is not obvious.
- Measure terminal widths with `lipgloss.Width`, never `len()`.
- The app never writes `state.json`; the factory supervisor is its only writer. `factory attention resolve` only checks the item with the node and prints the edit for the supervisor to make.
- A factory is an id and a prompt. There are no tickets: never add tracker integration to the app or the bundle. The skill reads a tracker only when the user's prompt asks it to.
- `state.json` once had `ticket` where it now has `id`; `state.ParseState` reads both.

## Tests

Write the fewest tests that catch a real failure. Each test names the regression it prevents. Prefer extending an existing test file over adding one.
