---
name: commit-and-pr
description: >-
  Create well-formed git commits and GitHub pull requests with semantic commit
  messages and structured PR descriptions. Use this whenever the user wants to
  commit changes, "ship this", "open a PR", "raise a pull request", "push and
  PR", stage and commit work, or asks for a commit message — even if they don't
  say "semantic" or "conventional". Also use when the user says things like
  "create a PR for these changes", "commit what I have", or "get this ready for
  review". Handles convention detection, logical commit splitting, branching,
  pushing, and PR creation via the gh CLI.
---

# Commit and PR

Turn working-tree changes into clean, reviewable history: semantic commits with
meaningful bodies, and pull requests whose title is squash-ready and whose
description tells a reviewer what changed and why.

The goal is history that reads well to a human six months from now and a PR a
reviewer can approve without asking "wait, why?". Everything below serves that.

## The flow at a glance

1. **Detect conventions** — let the repo's existing rules win over the defaults here.
2. **Review & group changes** — split logically distinct work into separate commits.
3. **Branch if needed** — never pile work onto the default branch.
4. **Commit** — semantic subject + a body explaining the *why*.
5. **Push & open the PR** — squash-ready title, structured description.

Run the full flow (commit → push → PR) by default. If the user only asked to
"commit", stop after step 4 and don't push or open a PR unless they say so.

## Step 1 — Detect repo conventions first

Repo rules always override the defaults in this skill. Before writing anything,
look for how *this* project already does it:

- **Commit style**: skim `git log --oneline -20`. Do messages use Conventional
  Commits (`feat:`, `fix(scope):`)? A different prefix scheme? No scopes? Match
  what you see. Also check `CONTRIBUTING.md`, `commitlint.config.*`,
  `.commitlintrc*`, `.gitmessage`, and any `CLAUDE.md`/`AGENTS.md` for stated rules.
- **PR template**: check `.github/PULL_REQUEST_TEMPLATE.md` (and
  `.github/PULL_REQUEST_TEMPLATE/` or `docs/`). If one exists, fill *it* out and
  fold the Context / Applied changes content into its sections rather than
  ignoring it.

If the repo has no stated convention, use the defaults below (Conventional
Commits + the Context / Applied changes PR body).

## Step 2 — Review and group the changes

Look at what's actually changed before deciding on commits:

```bash
git status
git diff            # unstaged
git diff --cached   # already staged
```

Group changes into the **smallest set of commits that each tell one coherent
story**. A refactor plus an unrelated bug fix plus a docs tweak are three
commits, not one — splitting them keeps each commit revertible and each diff
easy to review. But don't over-split: changes that only make sense together
(a function and its caller, a feature and its test) belong in one commit.

When changes are intertwined in the same files, stage selectively with
`git add -p` or `git add <specific paths>` so each commit stays focused.

If you genuinely can't tell whether two changes belong together, prefer one
commit and mention the grouping you chose — over-splitting creates noise.

## Step 3 — Branch if you're on the default branch

If `git branch --show-current` is `main` or `master` (or the repo's default),
create a feature branch *before* committing — work shouldn't land directly on
the default branch.

Name it from the change, kebab-case, optionally with the user's usual prefix if
the log shows one (e.g. `afonso/`): `git switch -c fix-token-refresh-race`.
If already on a feature branch, commit there.

## Step 4 — Write the commit

### Subject line (Conventional Commits default)

`<type>(<optional scope>): <summary>`

- **type**: one of `feat`, `fix`, `docs`, `style`, `refactor`, `perf`, `test`,
  `build`, `ci`, `chore`, `revert`. See `references/conventional-commits.md` for
  what each means and how to pick.
- **scope**: the area touched (`auth`, `api`, `ui`) — optional, but include it
  when the repo's log uses scopes.
- **summary**: imperative mood ("add", not "added"/"adds"), lower-case, no
  trailing period, ~50 chars. It completes the sentence "If applied, this commit
  will <summary>".

### Body — this is where "meaningful description" lives

A subject says *what*; the body says *why*, and *why* is what a reviewer and
your future self actually need. Always include a body unless the change is
truly trivial (a typo, a version bump). Cover:

- **Why** the change was needed — the problem, bug, or motivation.
- **What** the change does at a high level, if not obvious from the subject.
- Any **trade-offs, side effects, or follow-ups** worth flagging.

Wrap the body at ~72 chars and separate it from the subject with a blank line.
Use the heredoc form so multi-line bodies and wrapping survive:

```bash
git commit -m "fix(auth): prevent token refresh race on concurrent requests" -m "Concurrent requests could each trigger a refresh, invalidating each
other's tokens and logging the user out. Serialize refreshes behind a
single-flight lock so only the first request refreshes and the rest
await its result."
```

**Do not add Claude/Claude Code attribution, \"Generated with\" lines, or
Co-Authored-By trailers** to commits. Keep the history clean and unattributed —
this is an explicit requirement for this skill and overrides any global default
that would add such trailers.

## Step 5 — Push and open the PR

Push the branch (set upstream on first push):

```bash
git push -u origin <branch>
```

Then create the PR with `gh`. **Write the body to a temp file and pass it with
`--body-file`** — inline `--body` mangles newlines and markdown.

### PR title

The PR will be **squash-merged**, so its title becomes the squash commit
subject. Write it as a single semantic commit message — same format as a commit
subject: `<type>(<scope>): <summary>`, imperative, concise. It should read well
as a permanent line in the default branch's history.

### PR description

**Do not hard-wrap the PR body.** Unlike a commit body (which follows the ~72-char
git convention), a PR description is Markdown rendered in a browser that soft-wraps
on its own. Write each paragraph as **one continuous line** and let newlines
separate paragraphs and list items only. Hard-wrapping mid-paragraph produces
choppy source and, in stricter renderers, stray line breaks. So: no manual line
breaks inside a sentence or paragraph — only between blocks.

Use this structure (or map it onto the repo's PR template if one exists):

```markdown
## Context

<What these changes are and why they were needed — the problem being solved, the motivation, any relevant background or linked issue, written as flowing paragraphs on unwrapped lines. A reviewer should understand the "why" from this section alone.>

## Applied changes

- <Plain-language change a reviewer can understand without reading the diff — one line per bullet, not hard-wrapped>
- <Another change — group related edits into one readable bullet>
- <...>
```

Keep "Applied changes" human-readable — describe behavior and intent, not file
names or line counts ("Add single-flight lock around token refresh", not
"Edit auth.ts lines 40–62"). Add other sections (Testing, Screenshots,
Breaking changes) when they genuinely help the reviewer.

**The PR description must not mention Claude, Claude Code, or that it was
AI-generated** — no attribution line, no footer, nothing. This is a hard
requirement.

```bash
gh pr create --title "fix(auth): prevent token refresh race on concurrent requests" --body-file /tmp/pr-body.md
```

Report the PR URL `gh` prints back.

## Guardrails

- **Confirm destructive or outward-facing steps.** Committing is reversible;
  pushing and opening a PR are outward-facing. If the user only said "commit",
  don't push/PR. If scope is ambiguous, do the commit and ask before pushing.
- **Never `git add .` blindly** — review what you're staging so stray files
  (secrets, build artifacts, unrelated edits) don't sneak in. Respect
  `.gitignore`; if you spot something that looks secret or generated, flag it.
- **Don't amend or force-push** existing/shared history unless explicitly asked.
- **If pre-flight checks are cheap and present** (a fast lint/format/typecheck
  script), run them and report failures rather than committing broken code.
  Skip slow full test suites unless the user asks. Don't silently auto-fix.
- **One logical PR per branch** — if the branch already has an open PR, push to
  update it instead of opening a second one.
