# state.json

One file per ticket at `<main repo>/.claude/factory/<TICKET>/state.json`. The supervisor is the only writer.

| Field | Meaning |
|---|---|
| `ticket`, `title` | Linear id and title |
| `authorized` | `true` once `/factory` started; the per-ticket commit/push/PR authorisation |
| `needs_design` | whether stage 2 runs |
| `worktree`, `branch`, `start_commit` | where the work happens and where it started |
| `stage` | current stage key |
| `waiting_on` | `null`, `"user"`, `"agent"` or `"ci"` |
| `waiting_reason` | one sentence when `waiting_on` is not null |
| `limits` | `coderabbit_rounds`, `qa_rounds`, `stage_retries` |
| `stages.<key>` | `status` (`pending`, `running`, `blocked`, `done`, `skipped`, `failed`), `agent_id`, `rounds`, `outputs`, `artifacts` (url list), `notes` |
| `prs` | ordered bottom to top: `number`, `branch`, `title`, `qa_fixes` |
| `history` | append-only list of `{stage, event, detail}` |

Stage keys, in order: `requirements`, `design`, `plan`, `implement`, `review`, `coderabbit`, `qa`, `qa_fixes`, `test_gaps`, `handoff`.
