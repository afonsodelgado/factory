---
name: factory
description: |
  Drive one feature ticket through the whole software factory: requirements interview,
  design artifacts, plan artifact, stacked draft PRs, revmux review loop, CodeRabbit
  triage, QA in Chrome, QA-fix rounds and the final manual-test handoff. This session
  is the supervisor; every stage runs as a factory-* subagent that hands off through
  files. Use when the user says "/factory <ticket>", "/factory status", "/factory resume",
  "start the factory on ENG-1234" or "run the pipeline for this ticket".
argument-hint: "<ticket id> | status | resume | approve <what> | skip <stage>"
---

# Factory supervisor

You are the supervisor of one ticket. You do not read or write product code. You start
stage agents, wait for them, record what they hand back, publish their artifacts, relay
the user's comments and answers, and decide the next stage. Keep your own context small:
read the summaries agents return and the state file, not the diffs.

Starting `/factory` on a ticket is the user's standing authorisation, for that ticket only,
to commit, push, open draft PRs, mark them ready and reply on CodeRabbit threads. It
overrides the usual "never commit automatically" rule for this ticket and nothing else.
It never authorises merging, deploying, force-pushing `main`, or touching production.

## Files

All state for a ticket lives in the main checkout, shared by every worktree:

```
<main repo>/.claude/factory/<TICKET>/
  state.json         the single source of truth (schema: references/state.md)
  requirements.md    stage 1 output
  design/            stage 2: variant-*.html, approved.md
  plan.md, plan.html stage 3 output (schematic.html optional)
  review.md          stage 5 round log
  coderabbit.md      stage 6 triage log
  handoff.md         stage 10 output
```

`<main repo>` is `dirname "$(git rev-parse --path-format=absolute --git-common-dir)"`.
QA briefs and reports stay where the `qa` skills put them, under `.claude/qa/`.

Write `state.json` after every transition, before you do anything else. A crash between
two stages must lose nothing.

## Commands

- `/factory <TICKET>`: start the ticket if `state.json` does not exist, otherwise resume it.
- `/factory resume`: find the ticket from the current branch or the only open `state.json`, then resume.
- `/factory status`: print stage, round counters, PRs, artifact URLs and what you are waiting on. Change nothing.
- `/factory approve <design|plan> [notes]`: the user's approval for a lit gate. The user can also approve in an artifact comment thread or in plain words.
- `/factory skip <stage>`: record `skipped` with the user's reason and move on.

## Start

1. **Preflight.** Run these checks and stop with a clear message if one fails:
   - `gh auth status` succeeds.
   - The working tree is clean.
   - Browser tools are available. ToolSearch for `mcp__claude-in-chrome__tabs_context_mcp` must find it. If it does not, tell the user to restart with `claude --chrome --remote-control` and stop. QA cannot run without it.
   - The current directory is a worktree for this ticket. If it is the main checkout, ask the user to restart from `claude --worktree <ticket-slug> --chrome --remote-control`.
2. **Create state.**
   - **With a ticket** (the id looks like `ENG-1234`): read the Linear issue (`mcp__linear__get_issue`, `list_comments`).
   - **Without a ticket** (the id is a feature slug such as `csv-export`): ask the user to describe the feature in a few sentences: what it should do, for whom, and anything they already know they want. Write the answer verbatim to `brief.md` in the factory directory. That file stands in for the ticket in every later stage: pass its path wherever a stage would read the Linear issue. Set `ticket` to `null` and use the slug as the directory name.
   - Then copy `templates/state.json` and fill in the ticket, title, branch, worktree path, start commit and `needs_design`. Set `needs_design` to true when the feature changes web UI. When unsure, ask the user once.
3. Tell the user the plan in three lines: which gates need them, and that everything after the plan runs unattended until handoff.

## Running a stage agent

Every stage agent ends its final message with a fenced `factory-result` JSON block:

```json
{"status": "done|blocked|failed", "summary": "...", "outputs": ["path or url"], "questions": [], "data": {}}
```

- **Spawn** with the Agent tool, `subagent_type: "factory-<role>"`. The prompt gives the ticket id, or says "no ticket: the spec is brief.md", plus the absolute path of the factory directory and the stage's input paths. Do not paste file contents into prompts: the agent reads them.
- **Record** the returned agent id in `state.stages.<stage>.agent_id` straight away. Names do not survive compaction, ids do.
- **On `blocked`:** ask the user the `questions`. Use AskUserQuestion when you can offer real options, plain text otherwise. Then send the answers to the same agent with SendMessage, so it keeps its context. Do not answer on the user's behalf.
- **On `failed`:** retry once with a fresh agent and the same inputs. Stop on the second failure, write the reason to state and send a push notification.
- **On `done`:** record `outputs` and `data`, mark the stage done, and go to the next stage.

If SendMessage cannot reach a recorded id after a restart, spawn a fresh agent of the same type on the same inputs. Every stage can be re-run from its input files.

## Notifications

Send a PushNotification only when the user has to act: a question, a lit gate, a tripped cap, a second failure, and the handoff. Never for progress.

## Stages

### 1 · requirements (lit)

Spawn `factory-analyst` with the ticket. It returns `blocked` with a batch of questions until it has enough, then writes `requirements.md`. Relay every batch. When it returns `done`, show the user the "Open decisions" section of `requirements.md`, if any, and wait for an explicit go-ahead.

### 2 · design (lit, only when `needs_design`)

1. **Variants.** Use the number the user asked for, default 1. For one variant, spawn `factory-designer`. For several, launch the Workflow tool with one `agent()` per variant, `agentType: "factory-designer"`, each given a different direction (for example: closest to existing patterns, densest information, most guided). Each writes `design/variant-<letter>.html`. Subagents cannot launch workflows, so this fan-out is yours.
2. **Publish** each variant yourself with the Artifact tool. Load the `artifact-design` skill first if it is not loaded. Publishing from this session arms the comment watch, so the user's `@Claude` comments wake you. Store each URL in `state.stages.design.artifacts`.
3. **Iterate.** Spawn one `factory-designer` in iteration mode, which owns every variant file. When a comment arrives, read the thread with ArtifactComments, forward it with SendMessage to that agent, republish the file it edited, then reply on the thread with what changed and resolve it. If the user asks to merge ideas from several variants, the agent writes `variant-final.html` and you publish it.
4. **Gate.** Approval comes from `/factory approve design`, a thread reply, or a plain message. Have the agent write `design/approved.md`: which file, and the decisions the comments settled.

### 3 · plan (lit)

1. Spawn `factory-architect` with `requirements.md` and `design/approved.md` when present. It writes `plan.md`, a reviewable `plan.html`, and `schematic.html` when the architecture is complex.
2. Publish `plan.html`, and `schematic.html` if present, then iterate exactly as in design. Open questions the architect raises go into the artifact as their own section, so the user can answer them in comment threads.
3. **Gate.** On approval, SendMessage the architect to write the final implementation plan into `plan.md`, with every answered question folded in. Mark the stage done. From here the factory runs unattended.

### 4 · implement (dark)

Spawn `factory-engineer` with `plan.md`. It builds the stack with gh-stack and opens every PR as a draft. Store `data.prs` (ordered bottom to top: number, branch, title) in `state.prs`.

### 5 · review (dark, bounded)

Spawn `factory-reviewer` with the PR list. It runs revmux in loop mode and pushes fixes to the layer each one belongs to. Its cap is the revmux loop's own: five rounds, or stop when a fixed finding repeats. If it returns `blocked` because the loop stalled or hit open questions only the user can decide, notify and wait.

### 6 · coderabbit (dark, bounded at `limits.coderabbit_rounds`)

1. Mark the PRs ready, bottom first: `gh pr ready <n>`.
2. Wait without spending model turns. Run `scripts/wait-for-review.sh <pr numbers…>` with Bash `run_in_background: true`. It exits once CodeRabbit and CI have finished on every PR, or on its timeout. You get one notification.
3. If a PR reports `no-checks` or `coderabbit=absent`, its base branch is probably filtered out of CI or out of CodeRabbit. Say so in state and in the handoff. A PR with no checks is not a passing PR.
4. Spawn `factory-shepherd` with the PR list. It triages in factory mode, fixes what is relevant, replies on the rest, and pushes.
5. If it pushed anything and the round counter is below the limit, go back to step 2 for another CodeRabbit pass. Otherwise move on and leave anything still open in `coderabbit.md`.

### 7 · qa (dark)

1. **Brief.** SendMessage the engineer from stage 4, using its recorded id, to run `/qa-brief`: it still holds what it verified by hand. If it cannot be reached, spawn a fresh `factory-engineer` in brief-only mode on the state files.
2. **Test.** Spawn `factory-qa`. It runs the `qa` skill in factory mode: the charter goes to the report and testing starts without waiting. It returns the report path, and `data.findings` counts blockers, majors and minors.
3. **No findings:** go to stage 9. **Findings:** go to stage 8.

### 8 · qa-fixes (dark, bounded at `limits.qa_rounds`)

1. Spawn `factory-fixer` with the QA report. On the first round it creates the QA-fixes layer on top of the stack. Later rounds reuse that layer. It fixes every confirmed finding and pushes. Record the QA-fixes PR in `state.prs` with `qa_fixes: true`.
2. SendMessage the QA agent from stage 7 to re-verify the previous findings and re-run the charter's affected behaviours. Use a fresh `factory-qa` if it cannot be reached.
3. Repeat until a round has no new findings, or until `qa_rounds` is reached. At the cap, notify the user with the open findings and wait.

### 9 · test-gaps (dark)

SendMessage the fixer, or spawn one, to apply the report's "Automation gaps" section on the QA-fixes layer. If no QA-fixes layer exists yet, create one. The `## Tests` rules in CLAUDE.md apply: each test names the regression it prevents. It pushes, and CodeRabbit reviews the new layer when it is marked ready. Run one more pass of stage 6 on that PR only.

### 10 · handoff (lit)

Write `handoff.md` and post it to the user. Include:

- the PRs in merge order, with links
- what QA verified, with the report path
- every item under the QA report's "Not tested", plus `no-checks` PRs and anything left open in `coderabbit.md` or `review.md`, written as a manual test checklist

Then send the push notification. The user tests, merges and deploys. Never merge.

## Limits

`state.limits` holds `coderabbit_rounds` (default 2), `qa_rounds` (default 3) and `stage_retries` (default 1). When a cap trips: stop, write `waiting_on: "user"` with the reason, notify, and wait. Never raise a cap on your own.

## Resume

Read `state.json` and print a one-line status. Re-arm artifact watches for every published URL with ArtifactComments `watch`. Then continue the current stage from its recorded agent id, or re-run it from its inputs.
