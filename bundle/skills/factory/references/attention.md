# Attention items

An attention item is something the user must act on. The supervisor writes it to `state.json` *before* it asks, and resolves it when the answer arrives: `"${FACTORY_BIN:-factory}" attention resolve --dir <factory dir> [--waiting-on agent|ci|none] <item id>` checks that the item is still open and prints the exact edit (`resolved_at`, `waiting_on`, `updated_at`), which the supervisor then makes to `state.json` itself before it continues. The factory app shows open items (no `resolved_at`) in its panel and never writes them. It stops showing an item once its stage is `done` or `skipped`, or the factory is done. A `handoff` item from before handoffs asked the user stays on a done factory until the user answers it.

```json
{
  "id": "q-7452-1",
  "kind": "question",
  "title": "3 questions from factory-analyst",
  "body": "Three answers before the analyst can write requirements.md.",
  "options": [["10 per minute per IP", "30 per minute per IP"], [], ["10,000 entries, 60 s TTL"]],
  "recommended": ["10 per minute per IP", "429 with Retry-After", "10,000 entries, 60 s TTL"],
  "questions": ["How many failed auth attempts before a client is refused?", "What does a rate-limited request get back?", "How big may the negative cache grow?"],
  "artifact_url": null,
  "thread_id": null,
  "stage": "requirements",
  "created_at": "2026-10-22T14:38:00Z",
  "resolved_at": null
}
```

| Field | Meaning |
|---|---|
| `id` | the item id, see below |
| `kind` | `question`, `gate`, `comment`, `cap`, `failure` or `handoff` |
| `title` | one line |
| `body` | the context the user needs: the reason, the open findings, the thread excerpt |
| `questions` | `question` only: the questions in order |
| `options` | `question` only: one list of options per question, empty for free text |
| `recommended` | `question` only: one recommended answer per question |
| `stage` | the stage key the item belongs to, normally the current `stage`; gate, cap and failure ids also end with it |
| `artifact_url`, `thread_id` | `gate` and `comment`: the artifact, and the thread for a comment. Factories started before the review page have `comment` items; new ones do not |
| `created_at`, `resolved_at` | ISO 8601 UTC. Set `created_at` when you write the item and never change it: the id and `created_at` together tell the hub and the phone that an item is new |

## Ids

| Kind | Id |
|---|---|
| question batch | `q-<id>-<n>`, `n` counting batches from 1 |
| gate | `gate-<id>-design`, `gate-<id>-plan`, `gate-<id>-walkthrough` |
| artifact comment | `comment-<id>-<n>` |
| cap | `cap-<id>-<stage>` |
| failure | `fail-<id>-<stage>` |
| handoff | `handoff-<id>-<n>`, `n` counting handoffs from 1; the user's answer decides whether `stages.handoff.status` becomes `done` |

`<id>` is the factory id, as in the factory directory name. A cap or failure id ends with the stage key, which is how the app knows which stage `/factory skip` names and which limit a cap raises (`coderabbit_rounds` for `coderabbit`, `qa_rounds` otherwise).

## Messages from the panel

The app delivers answers into the session as one message, in exactly these forms. Recognise them by the item id at the start (or after the command):

| Item | Message |
|---|---|
| question batch | `<id> answers:` then one line per question, `N) <option text or typed text>`; a question the user did not touch is `N) recommended` |
| gate approve | `/factory approve <design\|plan\|walkthrough> <id> <notes>` on one line |
| gate changes | `<id> changes: <text>` |
| artifact comment | `<id> reply: <text>` (reply on the item's thread with that text) |
| cap, one more round | `<id> continue: raise <coderabbit_rounds\|qa_rounds> by one for this factory` |
| cap or failure, skip | `/factory skip <stage>` |
| failure, retry | `/factory resume` |
| handoff, mark done | `<id> done: the developer marked this factory done` (resolve it, mark the factory done and stop; never merge) |
| handoff, continue working | `<id> continue: <text>` (the next piece of work, see stage 11) |
| any item, already handled | `<id> handled: settled outside the panel` (resolve it, do not ask it again) |
| review page, send | `review-<id>-<n> comments: <batch file>` |

Slash commands are always one line. Free text may span several lines.

A `review-<id>-<n> comments:` message comes from the review page, not from an attention item. The hub sends the batch straight to the agent that owns the page when that agent has run in its own pane, and to you only when it has not. When it reaches you, send the batch file to the owning agent as a follow-up (the designer for `design/`, the architect for `plan.html` and `schematic.html`), and resolve nothing: the agent answers each thread itself with `factory review reply`. The page's Approve button sends `/factory approve <stage> <gate id>`, the same as the panel.

The panel hides an item as soon as the user sends it, and holds the message until your session is idle at its prompt. It keeps the user's draft until your session has received the message, which it reads from the session's own `UserPromptSubmit` hook. When the message does not arrive, for example because the session was showing a stage agent's transcript and the agent got it instead, the panel says so and the user sends it again.

Example of a question answer:

```
q-7452-1 answers:
1) 10 per minute per IP
2) 429 with Retry-After. Log the key prefix, never the full key.
3) recommended
```

The user can also answer in the session by hand, without the format. Resolve the item either way.
