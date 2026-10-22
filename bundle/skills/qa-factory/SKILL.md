---
name: qa-factory
description: |
  Act as a QA engineer on the feature in the current branch of any repo with a
  factory profile: work out what it is meant to do from the ticket or brief, the
  diff and the handoff brief, test the expected scenarios by hand (in the browser
  against the local stack, or in a scratch tmux server for terminal apps), attack it
  with edge cases, and report bugs plus the gaps in test automation worth closing.
  Use when the factory-qa agent runs, or the user says "/qa-factory". Runs locally only.
argument-hint: "[ticket id or focus area] [--factory]"
---

# QA pass on the current branch

You are testing someone else's work. Your job is to find out where the feature
breaks, not to confirm that it works. Build your understanding from the ticket and
the diff, not from the implementer's explanation. Where the two disagree, the gap
is a finding.

Read the repo's profile, `.claude/factory.yml`, first. Its `qa.mode` picks the
environment: `browser` or `terminal`. Its `qa.notes`, when set, names a repo file
with the environment, seeding and port steps that only that repo knows. Follow that
file wherever this skill says "the notes"; where the notes and this skill disagree,
the notes win, because they know the repo.

## Factory mode

Applies when the `factory-qa` agent runs you, or the argument contains `--factory`. Everything below holds, with these changes:

- **Spec:** the factory directory's `requirements.md` is the spec, ahead of the ticket. With no ticket, it and `brief.md` are the whole spec.
- **Phase 1:** do not wait for confirmation. Write the charter as the first section of the report, then start testing. Read a clearly relevant vault entry without asking.
- **Phase 2:** the environment must run this worktree's code. In browser mode, check which process serves the profile's ports and from which directory (`lsof -nP -iTCP:<port> -sTCP:LISTEN`, then `lsof -a -p <pid> -d cwd`). If it is another checkout, leave it running and start this worktree's stack on free ports, as the notes describe. In terminal mode, build the binary from this worktree into a temporary directory.
- **Report:** do not offer to publish it. The supervisor reads it.
- **Questions:** you cannot ask the user. Anything you would have asked goes under "Not tested", with what answer would unblock it.

## Ground rules

- **Local only.** Never prod, never staging, never a production database, even read-only.
- **No real contact.** Anything that can send, dial or post for real uses reserved test numbers, `@example.com` addresses and unconfigured connectors, unless the brief says otherwise.
- **Don't change the code under test.** No edits to app code, no commits. If a fix is obvious, put it in the report.
- **Evidence or it didn't happen.** Every finding needs a repro and what you saw: the request and response, a screenshot, a `capture-pane` excerpt, a file or a DB row. Mark anything you couldn't reproduce twice as *suspected*.
- **Destructive operations** only on data you created in this session.
- **Terminal mode isolation.** Never touch the user's tmux server, their `~/.claude`, or their config and state directories. Everything runs on a scratch tmux server with temporary directories.

## Phase 1 — Understand (then stop and confirm)

Read these sources in order:

1. **Branch and ticket.** `git branch --show-current`. Take the ticket id from it (or from the argument) and read the issue and its comments through Linear (`mcp__linear__get_issue`, `list_comments`). The ticket is the spec.
2. **What was built.** `git log --oneline main..HEAD`, `git diff main...HEAD --stat`, then the diff itself. Include uncommitted work too (`git diff`, `git status`). Read every entry point the change reaches. Don't read only the diff hunks: a guard on one path often has a sibling path without it.
3. **PR**, if there is one: `gh pr view --json title,body,url`.
4. **Handoff brief**: `.claude/qa/<branch with / replaced by ->.md`, written by `/qa-brief-factory`. It says what the developer already checked and where they are unsure. Spend less time on what they checked and more on what they're unsure about.
5. **Vault.** If an INDEX entry in the knowledge vault is clearly about this feature or the surface it touches, name it and ask before reading it.

Then write a **test charter** and show it to the user before testing:

- **Intent:** 2–3 sentences in product terms.
- **Surfaces:** every page, route, command, key binding, tool and background job the change reaches.
- **Principals:** the roles, accounts or environments that matter (for a terminal app: a fresh machine, an existing install, several terminal sizes).
- **Expected behaviours:** a numbered list, each one checkable.
- **Attack plan:** the adversarial categories below that apply, and why.
- **Spec vs. diff mismatches** already visible.
- **Out of scope.**

Wait for the user to confirm or adjust the charter. Don't start testing before that.

## Phase 2 — Environment

### Browser mode

1. **Check the stack.** `curl -s <qa.health>` should answer healthy, and `curl -sI localhost:<qa.web_port>` should answer. Make sure the listener is this repo: another repo's dev server may hold the port. If nothing runs, start it with `qa.start` in the background from the repo root. If curl to localhost fails inside the sandbox, retry it outside the sandbox.
2. **Migrations, flags, principals and seed data:** follow the notes. A 500 on a new column usually means a migration was skipped.
3. **HTTP.** Use curl for role, tenant and validation matrices: it is faster and leaves cleaner evidence.
4. **Browser.** Load the Chrome tools in one ToolSearch call: `tabs_context_mcp`, `navigate`, `computer`, `read_page`, `tabs_create_mcp`, `tabs_close_mcp`, `javascript_tool`, `read_network_requests` and `read_console_messages`. Open a new tab on the web port. Use the browser for flows, rendering and stale-UI checks.

### Terminal mode

1. **Build** this worktree's binary into a temporary directory with the profile's build command, or `go build -o <tmp>/<name> ./cmd/<name>` for a Go repo.
2. **Isolate.** Create temporary directories for HOME-like state and point every config, state and socket variable the app reads at them (the notes or the repo's `CLAUDE.md` name them). Put stub binaries for external tools (`gh`, `claude`, anything that would reach the network) first on a temporary `PATH`.
3. **Scratch tmux.** Start a server with `tmux -L qa-<slug> -f /dev/null new-session -d -s qa -x <cols> -y <rows>`. Drive it with `tmux -L qa-<slug> send-keys -t <pane> …` and read it with `tmux -L qa-<slug> capture-pane -p -e -t <pane>` (`-e` keeps colours, so you can check styles). Popups need an attached client: attach one with `script -q /dev/null tmux -L qa-<slug> attach -t qa` in the background.
4. **Sizes.** Test at the size the design was drawn at and at the narrow sizes it names; `tmux -L qa-<slug> resize-window -t qa -x <cols> -y <rows>`.
5. **Clean up** at the end: `tmux -L qa-<slug> kill-server` and the temporary directories you created.

## Phase 3 — Expected scenarios

Go through the charter's expected behaviours in order. For each one, check three things:

- the direct result (the response, or the screen after the key press)
- the persisted state (the database, or the files the app writes)
- what the user sees after a reload, a restart of the app, or a resize

**A success message is not a pass until the state it claims is actually there.** Record pass, fail or blocked against each item.

## Phase 4 — Adversarial

Work through the categories the charter selected. The notes may add the categories that have actually broken in that repo.

- **Every door.** Run the same action through every entry point that reaches it (UI, API, CLI, key binding, tool, background job). A gate on one door and not another is the most common real bug.
- **Authorisation and tenancy** (web apps). Another tenant's ids with this tenant's credentials, on every verb: expect 404, not 403. Each role against each mutating route.
- **Silent acceptance.** Unknown, misspelled and wrong-schema fields; wrong query-param or flag names. If the unfiltered or default result comes back, that's a finding.
- **Validation edges.** Empty, whitespace-only and very long strings; unicode, emoji and wide characters; quotes and newlines in names; negative, zero and huge numbers; missing and malformed ids.
- **State machine.** Try every action from every state it shouldn't be allowed in.
- **Concurrency and idempotency.** The same action twice in parallel; two instances of the app at once; a file changing underneath it. Look for duplicates, lost updates and locks that never release.
- **Inert feature.** For every panel or field the change shows, find the code that writes its data. An empty state can hide the fact that nothing writes it at all.
- **Terminal rendering** (terminal mode). Resize mid-flow, narrow and short terminals, columns drifting after wide glyphs or icons, colours on the focused and unfocused element, key bindings colliding with the terminal multiplexer's.
- **Process lifecycle** (terminal mode). Kill the app mid-action, restart it, detach and re-attach; check nothing it manages was lost or duplicated.
- **Time.** Anything windowed or scheduled: a non-UTC timezone, window edges, DST dates, and a machine that slept.
- **Failure paths.** A missing tool, a failing upstream call, an invalid credential. The error must reach the user and leave no half-written state behind.

Stop each probe once you have a finding or a clean result. Don't spend more on one category than its risk deserves.

## Phase 5 — Automation gaps

1. Find the existing coverage for the changed code: co-located unit and integration tests, end-to-end or golden tests, and whatever the notes name.
2. For each **finding**, and each **important behaviour that nothing automated covers**, propose at most one test. Each proposal states:
   - **Regression it prevents:** one concrete sentence. If you can't write that sentence, drop the proposal.
   - **Level:** the one closest to the user that can see the behaviour.
   - **Where:** the file to extend. Prefer extending an existing test over creating a new file.
3. **Don't propose:**
   - tests that restate a constant or a mapping
   - mirrors of call order or mock shapes
   - one test per branch of trivial logic
   - the same behaviour at two levels
   - defensive paths that can't happen

   Three valuable proposals beat ten.

## Report

Write the report to `.claude/qa/<branch-slug>-report.md`. In chat, give a short summary with the path. Offer to publish the report as an artifact if the user wants to share it.

```markdown
# QA — <ticket>: <title>
Branch, commit, date, mode, environment, principals used.

## Verdict
Ship / ship after fixes / don't ship — one sentence why.

## Findings
Sort by severity: blocker → major → minor → nit. For each finding:
- **[severity] Title** (confirmed | suspected)
  - Repro: exact steps, command or key sequence
  - Expected vs. actual
  - Evidence: response excerpt, screenshot, capture-pane excerpt or file
  - Likely cause: file:line, if you found it

## Charter results
Expected behaviours, numbered, with pass / fail / blocked.

## Not tested
What you skipped and why.

## Automation gaps
Each gap with its regression sentence, level and file.
```

Delete nothing you seeded in the browser mode's database at the end; list it in the report so the user can reuse it. In terminal mode, kill the scratch tmux server and remove the temporary directories.
