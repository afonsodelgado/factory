---
name: qa-brief-factory
description: |
  Write a short handoff brief for a later /qa-factory session at the end of a
  development session: what the feature should do, what was already verified by
  hand, where the developer is unsure, and the setup it needs. Use when the factory
  engineer finishes its stack, or the user says "/qa-brief-factory".
---

# QA handoff brief

Write `.claude/qa/<branch>.md`, where `<branch>` is `git branch --show-current` with `/` replaced by `-`. If the file already exists, overwrite it. The `/qa-factory` session reads it later in a fresh context.

Give QA what only this session knows. Do **not** explain how the code works: QA reads the diff itself, and your explanation would carry your assumptions into its testing.

Keep the brief to 30 lines at most:

```markdown
# <factory id>: <title>

## Intent
2–3 sentences in product terms: who can now do what, and what must not happen.

## Behaviours to verify
Numbered, checkable statements, including what must be refused.

## Already verified by hand
What was actually exercised in this session, and how (curl, browser, scratch tmux, test).
Leave out anything that was only reasoned about.

## Unsure about / risky
Shortcuts taken, paths not exercised, decisions that could be wrong,
doors to the same action that may not share the gate.

## Setup
The QA mode from the repo config, `.factory/config.yml` (the worktree's, or the main checkout's when the worktree has none, or `.claude/factory.yml` in a repo set up before `.factory/`), and what the environment needs: flags,
migrations, seed data, principals, stub tools, terminal sizes, environment variables. With browser QA, the node starts the stack unattended from the repo config's `qa.start` and `qa.health`, plus `qa.seed` and `qa.sign_in` when they are set, and drives a headless Chrome nobody can type into, so name every untracked file or secret the stack needs and anything those commands do not cover. When `qa.sign_in` is blank, QA signs up or logs in through the app on the local stack: name the documented test credentials or environment variables it can use, and any step it cannot pass unattended, such as an emailed code.

## Out of scope
Known gaps deliberately left for later, so they aren't reported as bugs.
```

Build "Already verified" and "Unsure about" from what actually happened in this conversation, not from what the code suggests. If the session didn't exercise something, it goes under "Unsure about". Then tell the user the path.

In factory mode use the supervisor-recorded local stack and shared command recipe. Never pull, push, submit or sync during implementation, review or QA. Report untested behaviour and prior unresolved findings explicitly.
