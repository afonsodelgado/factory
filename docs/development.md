# Developing factory

The Go module stays at the repository root: `cmd/`, `internal/` and `bundle/` keep their existing paths. The pnpm workspace holds the website in `apps/web` (`@factory/web`), a static Astro site with React islands, and the phone app in `apps/pwa` (`@factory/pwa`), a Vite and React PWA that the Go binary embeds. `packages/*` is for shared JavaScript packages and does not exist until one has real code.

Go development needs Go 1.26 or later, Make, tmux and jq. JavaScript tooling uses Node 24 (`.nvmrc` pins 24.22.1), pnpm 10.33.0 (`packageManager` in `package.json`) and Turbo 2.11.10. With nvm installed, set up the pinned tools from the repository root:

```sh
nvm install
nvm use
npm install --global pnpm@10.33.0
pnpm install
pnpm --filter @factory/web exec playwright install chromium
```

| Command | Does |
|---|---|
| `make` or `make build` | Build the site and the phone app, copy the app into `bundle/pwa/dist`, then build Go |
| `make test` | Test Go, the site and the phone app |
| `make lint` | Run Go vet, then lint and typecheck both apps |
| `make dev` | Build Go, then start the site at `http://localhost:4321/` |
| `make go-build`, `make go-test`, `make go-lint` | Run `go build ./...`, `go test ./...`, `go vet ./...` |
| `make web-build`, `make web-test`, `make web-lint`, `make web-typecheck`, `make web-dev` | Run the corresponding Turbo task for the site |
| `make pwa-build`, `make pwa-test`, `make pwa-lint`, `make pwa-typecheck`, `make pwa-dev` | Run the corresponding Turbo task for the phone app |
| `make pwa-embed` | Replace `bundle/pwa/dist` with `apps/pwa/dist` and check its build stamp |

With the site present, the whole-repository `make build`, `make lint`, `make test` and `make dev` need Node, pnpm and the Playwright Chromium browser. `make go-build`, `make go-lint` and `make go-test` stay Go-only. The web targets still print a skip message and succeed before looking for pnpm when `apps/web/package.json` is absent. `make dev` builds Go first even with `-j`; it does not start the interactive CLI. Run `go run ./cmd/factory` separately when you intend to open the hub.

The site's scripts are `build` (`astro build` into `apps/web/dist`), `lint` (Prettier), `typecheck` (`astro sync`, then `astro check`), `test` (Playwright against its own `astro preview` on port 4322), `dev` (port 4321) and `preview` (the production build on port 4321). Both servers bind `127.0.0.1` and fail instead of moving to another port when theirs is taken. Every server runs with `--ignore-lock`, so it stays in the foreground even when Astro detects a coding agent, and never hands over to a server that is already running.

`SITE_URL`, an http(s) origin read at build time, sets the absolute URLs in the sitemap, `robots.txt` and the social image tags. It defaults to `http://localhost:4321`: the site runs only locally, has no canonical link and is not deployed. The font subsets, Open Graph image and favicon in `apps/web/public` are committed; `pnpm --filter @factory/web fonts` and `pnpm --filter @factory/web assets` regenerate them, as `apps/web/public/fonts/NOTICE.md` describes. Ordinary builds never do.

Root `pnpm build`, `pnpm test`, `pnpm lint`, `pnpm typecheck` and `pnpm dev` call the guarded web targets. `pnpm turbo run build` is the direct advanced entry point. Turbo caches the site's `dist/` and generated `.astro/` types, and caches lint, typecheck and test results; `test` depends on the same package's `build`, because the browser tests serve `dist`. The build-time `SITE_URL` variable is part of the build, typecheck and test hashes. Development tasks are persistent and uncached. There is no coverage collection, and remote caching is not configured.

The phone app's `dev` serves `127.0.0.1:4323` and its Playwright tests start their own fake gateway on 4324; both fail rather than move. `make pwa-embed` is never cached, so a Turbo cache hit still refreshes the embedded copy. A Go-only build embeds only `bundle/pwa/unavailable.html`: the node answers the app's routes with a "run make build" page. The app's icons in `apps/pwa/public/icons` are committed; `pnpm --filter @factory/pwa icons` regenerates them from the site's favicon.

Command detection prefers the root Makefile's build, lint and test targets, then package scripts, then Go defaults, so this repository resolves to its `make` targets. The workspace does not change the repo config.

## CI

Pull requests and pushes to `main` always start CI. Changed paths select the Go matrix on Ubuntu/macOS, the web job on Ubuntu, or both. Go jobs install tmux, jq, Node and pnpm, build and embed the phone app, then run `make go-build`, `make go-lint` and `make go-test`. The web job installs Node from `.nvmrc`, pnpm from `packageManager`, dependencies with `pnpm install --frozen-lockfile` and Playwright's Chromium with its system dependencies, then runs lint, typecheck, test and build for the site and the phone app.

| Changed paths | Validation |
|---|---|
| Root `*.go`, `cmd/**`, `internal/**`, `bundle/**` except `bundle/review/**`, `go.mod`, `go.sum`, `install.sh`, release workflow | Go |
| `bundle/review/**` (the review overlay the phone app's browser tests load) | Go and web |
| `apps/pwa/**`, `apps/web/public/fonts/**` (the app's embedded fonts), root pnpm manifests and lockfile, `turbo.json`, `.nvmrc` | Go and web |
| Other `apps/**` (the site in `apps/web`: source, public assets, scripts, tests and config), `packages/**` | Web |
| `Makefile`, `.gitignore`, CI workflow | Go and web |
| Root README, CLAUDE and ordinary docs outside Go-owned directories | Neither workload |

Mixed changes run the union of the matching workloads. Go-owned directories include their data and embedded documentation. Add new build inputs to this table and the inline CI filters together. Pull requests use the changed-files API; main pushes compare against the previous pushed head, including every commit in the push.

Require the final `ci` check in branch protection. It reports success only when change detection succeeds and both workloads succeed or are skipped; failures, cancellations and unexpected results fail it. After adopting this workflow, replace any old matrix requirements with `ci` manually. A started workflow reports this aggregate after path skips or dependency failures, but platform outages, manual workflow cancellation and commit-level CI-skip directives still affect reporting.
## Releases

Pushes to `main` publish a release when they change root Go files, `cmd/`, `internal/`, `bundle/`, `go.mod`, `go.sum`, the release workflow, or what the embedded phone app is built from: `apps/pwa/`, the site's fonts it bundles, root pnpm manifests and lockfile, `turbo.json` and `.nvmrc`. Other site, CI-only and root documentation changes alone do not release; embedded documentation in `bundle/` does. The release installs the frozen JavaScript dependencies, builds and embeds the phone app, and fails when a binary lacks the app's build stamp. The patch version goes up by one from the highest stable semantic-version tag (the first is `v0.1.0`), with a binary for `darwin_arm64`, `darwin_amd64`, `linux_amd64` and `linux_arm64` and their sha256 sums in `factory_checksums.txt`, which `factory update` and `install.sh` verify. `install.sh` is served from `main`, so changing it alone does not release. An already-tagged main HEAD skips automatic release.

For a minor or major bump, push the tag yourself (`git tag v0.2.0 && git push origin v0.2.0`), and that exact release is published instead. A `v*` tag push releases regardless of changed paths, including tags on site-only commits.
