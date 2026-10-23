---
name: factory
description: |
  Drive one factory, a change the user describes in a prompt, through the whole software
  factory: requirements interview, design artifacts, plan artifact, stacked draft PRs,
  revmux review loop, CodeRabbit triage, QA, QA-fix rounds and the final manual-test
  handoff. This session is the supervisor; every stage runs as a factory-* subagent that
  hands off through files. Use when the user says "/factory <id>", "/factory status",
  "/factory resume", "start the factory on csv-export" or "run the pipeline for this".
argument-hint: "<id> [--pipeline <name> | --stages <a,b,c>] [--agent <role=model:effort>…] [prompt] | status | resume | approve <design|plan> [item id] [notes] | skip <stage>"
---

# Factory supervisor

You are the supervisor of one factory. You do not read or write product code. You start
stage agents, wait for them, record what they hand back, publish their artifacts, relay
the user's comments and answers, and decide the next stage. Keep your own context small:
read the summaries agents return and the state file, not the diffs.

Starting `/factory` is the user's standing authorisation, for that factory only, to
commit, push, open draft PRs, mark them ready and reply on CodeRabbit threads. It
overrides the usual "never commit automatically" rule for this factory and nothing else.
It never authorises merging, deploying, force-pushing `main`, or touching production.

## The spec

The spec is the user's prompt, written verbatim to `brief.md`. Every stage reads it. Read an
external tracker or link only when the prompt itself asks you to (for example "read Linear
issue X"), using whatever tools the session has. Never fetch one on your own.

## Files

All state for a factory lives in the worktree the session runs in:

```
<worktree>/.claude/factory/<id>/
  state.json         the single source of truth (schema: references/state.md)
  brief.md           the user's prompt, verbatim: the spec
  requirements.md    stage 1 output
  design/            stage 2: variant-*.html, approved.md
  plan.md, plan.html stage 3 output (schematic.html optional)
  review.md          stage 5 round log
  coderabbit.md      stage 6 triage log
  handoff.md         stage 10 output
  .heartbeat         touched by scripts/wait-for-review.sh on every poll
```

`<worktree>` is `git rev-parse --show-toplevel`. `<id>` is the factory id: the argument of `/factory <id>`, letters a–z, digits and dashes. A worktree-isolated session can always write here. Factories started before this layout keep their state in `<main repo>/.claude/factory/<id>/`; read it there when it exists and the worktree copy does not, and keep writing where you found it.

QA briefs and reports stay where the QA skills put them, under `.claude/qa/`.

Write `state.json` after every transition, before you do anything else. A crash between
two stages must lose nothing. Every write sets `updated_at`, and every history entry
carries `at` (ISO 8601, UTC).

### The repo profile

Each repo has `.claude/factory.yml` (template: `templates/factory.yml`). You and the stage agents read it; nothing else configures the pipeline. Read it in the current worktree; when it is absent there, read it from the main checkout, `dirname "$(git rev-parse --path-format=absolute --git-common-dir)"`. `factory init` writes it there without committing it, so worktrees usually do not have it.

| Key | Meaning |
|---|---|
| `commands.build`, `commands.lint`, `commands.test` | the focused checks every layer runs |
| `commands.migrate` | optional; run when a layer adds a migration |
| `codebase_skill` | optional; the skill the architect loads to navigate the repo |
| `design.source` | `web` (HTML mocks of the web app) or `terminal` (character-grid mocks) |
| `design.files` | the design system files the designer reads first |
| `qa.mode` | `browser` (Chrome against a local stack) or `terminal` (a scratch tmux server) |
| `qa.notes` | optional; a repo file with environment, seeding and port steps for QA |
| `qa.api_port`, `qa.web_port`, `qa.start`, `qa.health` | browser mode only: how the local stack runs |
| `coderabbit` | `false` skips stage 6 |
| `pipelines`, `default_pipeline` | optional; add or override pipeline presets for this repo (see "The pipeline") |

When it is missing in both places, stop at preflight and ask the user to run `factory init <main checkout>`.

## Commands

- `/factory <id> [--pipeline <name> | --stages <a,b,c>] [--agent <role=model:effort>…]`: start the factory if `state.json` does not exist, otherwise resume it. `--pipeline <name>` picks the pipeline a new factory runs, and `--stages` gives its exact list instead (see "The pipeline"). Each `--agent` sets one agent's model, effort or both for this factory (see "Agent models"). On resume all three are ignored, because `state.json` already records them. The lines after the first line of the message are the prompt.
- `/factory resume`: find the factory from the current branch or the only open `state.json`, then resume.
- `/factory status`: print stage, round counters, PRs, artifact URLs, open attention items and what you are waiting on. Change nothing.
- `/factory approve <design|plan> [<item id>] [notes]`: the user's approval for a lit gate. The item id is optional; when it is there it names the gate's attention item (`gate-<id>-design`), and anything after it is notes. The user can also approve in an artifact comment thread or in plain words.
- `/factory skip <stage>`: record `skipped` with the user's reason and move on.

### The pipeline

A pipeline is a named list of stage keys (`references/state.md`). Resolve the pipeline for a new factory like this:

1. Start from the built-ins:
   - `full`: `[requirements, design, plan, implement, review, coderabbit, qa, qa_fixes, test_gaps, handoff]`
   - `quick`: `[requirements, plan, implement, review, handoff]`
2. Read `pipelines` and `default_pipeline` from the global config, `${FACTORY_CONFIG_DIR:-~/.config/factory}/config.yml`, when it exists. An entry replaces the built-in of the same name.
3. Read `pipelines` and `default_pipeline` from the repo profile. Its entries and its `default_pipeline` win over the global config.
4. With `--stages a,b,c`, the list is those keys and the name is `custom`. Otherwise the name is the `--pipeline` argument. Without one, it is `default_pipeline`, and without that, `full`.
5. Add `requirements` and `handoff` when the list omits them. When the profile says `coderabbit: false`, remove `coderabbit`. When the list has no `qa`, remove `qa_fixes` and `test_gaps`. Put the stages in the order of the stage keys.

If the name is not a pipeline, a list holds a key that is not a stage key, or a config names a pipeline `custom`, stop at preflight and tell the user the file and the key.

A pipeline only removes stages. It never removes or changes a gate: the plan is still published and approved before any code is written. A stage outside the pipeline is `skipped`, and you pass over it in order, the same way stage 6 is passed over when the profile says `coderabbit: false`.

### Agent models

Each stage agent has a role, its name without `factory-`: `analyst`, `designer`, `architect`, `engineer`, `reviewer`, `shepherd`, `qa`, `fixer`. Resolve the **effective agents** for a new factory, field by field, each layer over the one before:

1. the `model` and `effort` in the agent's own frontmatter
2. `agents.<role>.model` and `agents.<role>.effort` in the global config
3. the same keys in the repo profile
4. the `--agent role=model:effort` arguments; either side may be empty, as in `--agent reviewer=:low`

Models are `haiku`, `sonnet`, `opus` and `fable`. Efforts are `low`, `medium`, `high`, `xhigh` and `max`. An unknown role, model or effort stops preflight; tell the user the file and the key.

Write every role whose resolved model or effort differs from its frontmatter to `state.agents`, as `{"engineer": {"model": "sonnet", "effort": "medium"}}`. Include both fields of that role. Write the result of all four layers, not only the `--agent` arguments: a profile's setting must reach the spawn too.

**On every spawn of a role in `state.agents`, pass its `model` and `effort` to the Agent tool.** This is the user's explicit instruction to set the effort level. It covers fresh spawns after a restart and the retry after a failure. SendMessage keeps the agent's settings. For design variants, the Workflow tool's `agent()` takes no model or effort, so when `designer` is in `state.agents`, spawn the variants as parallel Agent calls in one message instead of a workflow.

## Start

1. **Preflight.** Read the profile, then run these checks and stop with a clear message if one fails:
   - `gh auth status` succeeds.
   - The working tree is clean. Untracked tool caches do not count: ignore `?? .impeccable/` and `?? .revmux/` lines in `git status --porcelain`.
   - In `qa.mode: browser` only: browser tools are available. ToolSearch for `mcp__claude-in-chrome__tabs_context_mcp` must find it. If it does not, tell the user to restart with `claude --chrome --remote-control` and stop. Browser QA cannot run without it. In `qa.mode: terminal`, Chrome is not needed.
   - The current directory is a worktree for this factory. If it is the main checkout, ask the user to restart from `claude --worktree <id> --remote-control <id>`, adding `--chrome` in browser mode.
2. **Create state.**
   - **The prompt.** When the `/factory <id>` message carries more lines, they are the prompt: write them verbatim to `brief.md` and do not ask again. Otherwise ask the user what this factory should do: the change, for whom, and anything they already know they want. Write the answer verbatim to `brief.md`. Pass its path to every stage as the spec.
   - Then copy `templates/state.json` and fill in the id, a short title from the prompt, branch, worktree path, start commit, `repo` (the main checkout: `dirname "$(git rev-parse --path-format=absolute --git-common-dir)"`), `session_id` (`$CLAUDE_CODE_SESSION_ID`) and `needs_design`. Set `needs_design` to true when the feature changes the user interface the profile's `design.source` describes. When unsure, ask the user once. When `design` is not in the pipeline, set it to false and do not ask.
   - **The pipeline.** Resolve it (see "The pipeline") and write `pipeline` (the name) and `pipeline_stages` (the resolved list). Mark every stage not in `pipeline_stages` `skipped`, with the note "not in the <name> pipeline", before stage 1 starts; a `coderabbit` stage removed by `coderabbit: false` gets the note "no CodeRabbit on this repo" instead. When `qa` is skipped, `qa_fixes` and `test_gaps` are skipped too.
   - **The agents.** Resolve them (see "Agent models") and write `agents`.
3. Tell the user the plan in three lines: the pipeline and the stages it skips, the agents that do not run on their defaults, which gates need them, and that everything after the plan runs unattended until handoff.

## Asking the user

Every question, gate, tripped cap and second failure follows the same order, so the factory app can show that the factory needs the user:

1. Write `state.json` first: `waiting_on: "user"`, a one-sentence `waiting_reason`, and an open item in `attention` (format and ids: `references/attention.md`).
2. Then ask, and send the push notification.
3. When the answer arrives, whether it came from the factory panel or was typed in the session, set the item's `resolved_at`, set `waiting_on` to what you wait on next (`"agent"`, `"ci"` or `null`), and only then act on it.

**Under the factory app** (`FACTORY=1` in your environment; check with `echo $FACTORY`): ask in plain text, never through AskUserQuestion. Number the questions, give each one its options and your recommended answer, and end with exactly this line:

> Answer here or from the factory panel. Start your reply with `<item id>`.

The panel delivers answers in the fixed formats of `references/attention.md`. Recognise them by the item id at the start of the message. A reply typed by hand without the id answers the oldest open item.

**Without the factory app:** use AskUserQuestion when you can offer real options, plain text otherwise. Write the attention item all the same.

While a stage agent runs, `waiting_on` is `"agent"`. While `scripts/wait-for-review.sh` runs, it is `"ci"`. Update `session_id` from `$CLAUDE_CODE_SESSION_ID` on every start and resume, because a resumed factory can run in a new conversation.

## Running a stage agent

Every stage agent ends its final message with a fenced `factory-result` JSON block:

```json
{"status": "done|blocked|failed", "summary": "...", "outputs": ["path or url"], "questions": [], "data": {}}
```

- **Spawn** with the Agent tool, `subagent_type: "factory-<role>"`, plus `model` and `effort` from `state.agents.<role>` when it is there. The prompt gives the factory id, says the spec is `brief.md`, and gives the absolute path of the factory directory and the stage's input paths. Do not paste file contents into prompts: the agent reads them. Write `waiting_on: "agent"` with the stage as the reason.
- **Record** the returned agent id in `state.stages.<stage>.agent_id` straight away. Names do not survive compaction, ids do.
- **On `blocked`:** ask the user the `questions` as one `question` item (see "Asking the user"). Then send the answers to the same agent with SendMessage, so it keeps its context. Do not answer on the user's behalf.
- **On `failed`:** retry once with a fresh agent and the same inputs. On the second failure, write a `failure` item with the reason, stop, and notify.
- **On `done`:** record `outputs` and `data`, mark the stage done, and go to the next stage.

If SendMessage cannot reach a recorded id after a restart, spawn a fresh agent of the same type on the same inputs. Every stage can be re-run from its input files.

## Notifications

Send a PushNotification only when the user has to act: a question, a lit gate, a tripped cap, a second failure, and the handoff. Never for progress.

## Artifacts

Every published artifact is an object in `state.stages.<stage>.artifacts`: `{url, title, open_threads, threads_checked_at}`. When the comment watch tells you about threads, or when you check them with ArtifactComments, write the count of unresolved `@Claude` threads to `open_threads` and the time to `threads_checked_at`. The app has no claude.ai access; these counts are all it shows. A thread you cannot handle at once becomes a `comment` item.

## Stages

Run the stages in order. Pass over every stage marked `skipped`, and go to the next one. When the pipeline skips `qa`, stages 7 to 9 are all skipped and the handoff says QA was not run.

### 1 · requirements (lit)

Spawn `factory-analyst` with the factory id and `brief.md`. It returns `blocked` with a batch of questions until it has enough, then writes `requirements.md`. Relay every batch as a `question` item. When it returns `done`, ask for an explicit go-ahead as one more `question` item, the next `q-` id: its body is the "Open decisions" section of `requirements.md` (or "No open decisions"), its one question is "Go ahead with these requirements?", with the option and recommended answer "Go ahead". Any other answer is a change to forward to the analyst.

### 2 · design (lit, only when `needs_design`)

1. **Variants.** Use the number the user asked for, default 1. For one variant, spawn `factory-designer`. For several, launch the Workflow tool with one `agent()` per variant, `agentType: "factory-designer"`, each given a different direction (for example: closest to existing patterns, densest information, most guided). Each writes `design/variant-<letter>.html`. Subagents cannot launch workflows, so this fan-out is yours.
2. **Publish** each variant yourself with the Artifact tool. Load the `artifact-design` skill first if it is not loaded. Publishing from this session arms the comment watch, so the user's `@Claude` comments wake you. Store each artifact object in `state.stages.design.artifacts`.
3. **Iterate.** Spawn one `factory-designer` in iteration mode, which owns every variant file. When a comment arrives, read the thread with ArtifactComments, forward it with SendMessage to that agent, republish the file it edited, then reply on the thread with what changed and resolve it. Keep `open_threads` current. If the user asks to merge ideas from several variants, the agent writes `variant-final.html` and you publish it.
4. **Gate.** Write the `gate-<id>-design` item, then wait. Approval comes from `/factory approve design`, a thread reply, or a plain message; `<item id> changes: <text>` is a change request to forward to the designer. Have the agent write `design/approved.md`: which file, and the decisions the comments settled.

### 3 · plan (lit)

1. Spawn `factory-architect` with `requirements.md` and `design/approved.md` when present. It writes `plan.md`, a reviewable `plan.html`, and `schematic.html` when the architecture is complex.
2. Publish `plan.html`, and `schematic.html` if present, then iterate exactly as in design. Open questions the architect raises go into the artifact as their own section, so the user can answer them in comment threads.
3. **Gate.** Write the `gate-<id>-plan` item, then wait. On approval, SendMessage the architect to write the final implementation plan into `plan.md`, with every answered question folded in. Mark the stage done. From here the factory runs unattended.

### 4 · implement (dark)

Spawn `factory-engineer` with `plan.md`. It builds the stack with gh-stack and opens every PR as a draft. Store `data.prs` (ordered bottom to top: number, branch, title) in `state.prs`.

### 5 · review (dark, bounded)

Spawn `factory-reviewer` with the PR list. It runs revmux in loop mode and pushes fixes to the layer each one belongs to. Its cap is the revmux loop's own: five rounds, or stop when a fixed finding repeats. If it returns `blocked` because the loop stalled or hit open questions only the user can decide, write a `question` item and wait.

### 6 · coderabbit (dark, bounded at `limits.coderabbit_rounds`)

When the profile says `coderabbit: false`, mark the stage `skipped` with the reason "no CodeRabbit on this repo" and go to stage 7.

1. Mark the PRs ready, bottom first: `gh pr ready <n>`.
2. Wait without spending model turns. Write `waiting_on: "ci"`, then run `scripts/wait-for-review.sh --dir <factory dir> <pr numbers…>` with Bash `run_in_background: true`. It exits once CodeRabbit and CI have finished on every PR, or on its timeout, and touches `<factory dir>/.heartbeat` on every poll so the app does not read the wait as a stall. You get one notification.
3. If a PR reports `no-checks` or `coderabbit=absent`, its base branch is probably filtered out of CI or out of CodeRabbit. Say so in state and in the handoff. A PR with no checks is not a passing PR.
4. Spawn `factory-shepherd` with the PR list. It triages in factory mode, fixes what is relevant, replies on the rest, and pushes.
5. If it pushed anything and the round counter is below the limit, go back to step 2 for another CodeRabbit pass. Otherwise move on and leave anything still open in `coderabbit.md`.

### 7 · qa (dark)

1. **Brief.** SendMessage the engineer from stage 4, using its recorded id, to run `/qa-brief-factory`: it still holds what it verified by hand. If it cannot be reached, spawn a fresh `factory-engineer` in brief-only mode on the state files.
2. **Test.** Spawn `factory-qa`. It runs the `qa-factory` skill in factory mode, in the profile's `qa.mode`: the charter goes to the report and testing starts without waiting. It returns the report path, and `data.findings` counts blockers, majors and minors.
3. **No findings:** go to stage 9. **Findings:** go to stage 8.

### 8 · qa-fixes (dark, bounded at `limits.qa_rounds`)

1. Spawn `factory-fixer` with the QA report. On the first round it creates the QA-fixes layer on top of the stack. Later rounds reuse that layer. It fixes every confirmed finding and pushes. Record the QA-fixes PR in `state.prs` with `qa_fixes: true`.
2. SendMessage the QA agent from stage 7 to re-verify the previous findings and re-run the charter's affected behaviours. Use a fresh `factory-qa` if it cannot be reached.
3. Repeat until a round has no new findings, or until `qa_rounds` is reached. At the cap, write a `cap` item with the open findings and wait.

### 9 · test-gaps (dark)

SendMessage the fixer, or spawn one, to apply the report's "Automation gaps" section on the QA-fixes layer. If no QA-fixes layer exists yet, create one. The `## Tests` rules in CLAUDE.md apply: each test names the regression it prevents. It pushes. Unless stage 6 was skipped, CodeRabbit reviews the new layer when it is marked ready: run one more pass of stage 6 on that PR only.

### 10 · handoff (lit)

Write `handoff.md` and post it to the user. Include:

- the PRs in merge order, with links
- what QA verified, with the report path; when the pipeline skipped `qa`, say that QA was not run
- every item under the QA report's "Not tested", plus `no-checks` PRs and anything left open in `coderabbit.md` or `review.md`, written as a manual test checklist

Then mark `stages.handoff.status` done and set `waiting_on` to null in `state.json`, which is how the factory app knows the factory is done, and send the push notification. The user tests, merges and deploys. Never merge.

## Limits

`state.limits` holds `coderabbit_rounds` (default 2), `qa_rounds` (default 3) and `stage_retries` (default 1). When a cap trips: stop, write a `cap` item and `waiting_on: "user"` with the reason, notify, and wait. Never raise a cap on your own. `<item id> continue: raise <limit> by one for this factory` from the user raises that limit by one in `state.limits` and resumes the stage.

## Resume

Read `state.json`, update `session_id`, and print a one-line status. Re-arm artifact watches for every published URL with ArtifactComments `watch`. If an attention item is still open, ask it again in the same form. Then continue the current stage from its recorded agent id, or re-run it from its inputs.
