---
name: requirements-interview
description: |
  Interview the developer about a feature before any design or planning, so the
  requirements are complete: read the user's prompt and the code it touches, find the
  gaps, ask about them in short batches, and write a requirements document. Use when the
  user says "interview me about this feature", "ask me questions about the
  requirements", "/requirements-interview", or describes a change and asks what is
  missing. Also used by the factory-analyst agent.
argument-hint: "<description or brief path> [--out <path>]"
---

# Requirements interview

Your job is to find what the prompt does not say, before anyone designs or plans. The
prompt is the starting point, not the finished spec. A question is worth asking only if different
answers would lead to different work.

## 1 · Read before you ask

1. Read the user's prompt: `brief.md` in the factory directory, the description you were given, or ask the user for a short one first. It is the spec. Read an external tracker or link only when the prompt itself asks you to (for example "read Linear issue X"), using whatever tools the session has. Never fetch one on your own.
2. Find the code the feature touches: the routes, pages, schemas, Copilot tools and jobs. Read enough to know what exists today and what the change would collide with. Never ask the user something the code can answer.
3. If a knowledge vault INDEX entry is clearly about this area, name it. Read it only after the user agrees, or when you are running inside the factory, where the vault counts as an input.

## 2 · Find the gaps

Check each dimension. Skip the ones that clearly do not apply:

- **Who:** roles, tiers (staff, super admin), workspaces, API keys. Who must be refused.
- **Doors:** every way to reach the action: UI, REST, public API, Copilot tools, background jobs. Which ones are in scope.
- **Behaviour:** the happy path, empty states, limits, ordering, defaults, what an existing record does after the change.
- **Failure:** what the user sees when an upstream call fails, and what must not be left half-written.
- **Data:** new fields, migrations, backfill, retention, analytics impact.
- **Flags and rollout:** behind a flag or not, what flag-off means, rollback.
- **Out of scope:** what the user explicitly does not want now.
- **Done:** how the user will know it works. This becomes QA's charter.

## 3 · Ask in batches

- At most five questions per batch, most consequential first.
- Every question carries your recommended answer and the reason, so the user can reply "yes" to most of them.
- Prefer questions with 2 to 4 concrete options. Use free text only when the answer space is open.
- After each batch, fold the answers in and look for the new gaps they opened. Stop when a batch would hold only low-stakes questions. Put those under "Assumed" instead.

**Interactive:** ask with AskUserQuestion.

**Inside the factory** (you are the `factory-analyst` agent): you cannot ask directly. End your turn with a `factory-result` block with `status: "blocked"` and the batch as `questions`, each written as `"Q: … Recommended: … because …"`. The answers arrive in your next message.

## 4 · Write the document

Write to `--out` when given, otherwise to `.claude/factory/<id>/requirements.md`. Keep it to what the next stage needs:

```markdown
# <id>: <title>

## Goal
2–3 sentences in product terms: who can now do what.

## Requirements
Numbered, each one checkable. Include what must be refused.

## Doors in scope
Each surface, and which roles reach it.

## Data and rollout
Fields, migrations, flags, backfill.

## Out of scope

## Assumed
Low-stakes defaults taken without asking, so the user can veto them.

## Open decisions
Anything still unresolved. Empty means ready to design or plan.

## Acceptance
How the user will judge it done.
```

Then report the path. Do not start designing or planning.
