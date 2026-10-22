# Conventional Commits — type reference

Read this when you're unsure which `type` fits, or how to handle breaking
changes. The format is `<type>(<optional scope>): <summary>`.

## Types

| Type       | Use when…                                                                 |
|------------|---------------------------------------------------------------------------|
| `feat`     | You add user-facing functionality (a new feature, endpoint, option).      |
| `fix`      | You fix a bug — wrong behavior, crash, regression.                        |
| `docs`     | Documentation only (README, comments, guides). No code behavior change.   |
| `style`    | Formatting/whitespace/semicolons only — no logic change.                  |
| `refactor` | Restructure code without changing behavior (rename, extract, dedupe).     |
| `perf`     | A change made specifically to improve performance.                        |
| `test`     | Add or fix tests, no production code change.                              |
| `build`    | Build system, bundler, or dependency changes (package.json, Dockerfile).  |
| `ci`       | CI config and scripts (GitHub Actions, pipelines).                        |
| `chore`    | Maintenance that doesn't fit above (configs, tooling, version bumps).      |
| `revert`   | Reverting a previous commit.                                              |

## Picking between close calls

- New behavior the user can observe → `feat`. Internal-only change with no
  observable behavior difference → `refactor` (or `perf` if speed was the goal).
- Fixing something that was working incorrectly → `fix`. Changing something that
  worked fine into something new → `feat`.
- Dependency bump that adds a feature → still `build`/`chore`; the *type*
  describes the kind of change, not its downstream effect.

## Scope

Optional, lower-case, names the area touched: `feat(auth):`, `fix(api):`,
`refactor(ui):`. Use it when the repo's existing log uses scopes; match their
vocabulary (look at recent commits for the scope names already in use). Omit it
rather than inventing an awkward one.

## Breaking changes

If the change is backward-incompatible, signal it both ways the spec allows:

- Add `!` after the type/scope: `feat(api)!: drop support for v1 tokens`
- Add a `BREAKING CHANGE:` footer in the body explaining the migration:

```
feat(api)!: require auth header on all endpoints

BREAKING CHANGE: unauthenticated requests now return 401. Clients must
send a Bearer token; see docs/auth.md for migration.
```

## Examples

- `feat(billing): add proration for mid-cycle plan changes`
- `fix(upload): handle empty multipart body without 500`
- `refactor(parser): extract token scanner into its own module`
- `perf(search): cache compiled regexes across requests`
- `docs: document the rate-limit headers`
- `chore(deps): bump axios to 1.7.4`
