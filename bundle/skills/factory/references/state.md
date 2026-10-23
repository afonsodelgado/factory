# state.json

One file per ticket at `<worktree>/.claude/factory/<id>/state.json`. The supervisor is the only writer; the factory app only reads it.

| Field | Meaning |
|---|---|
| `ticket`, `title` | Linear id (or `null` without a ticket) and title |
| `repo` | the main checkout's absolute path |
| `session_id` | the supervisor's Claude Code session id, from `$CLAUDE_CODE_SESSION_ID`, updated on every start and resume |
| `authorized` | `true` once `/factory` started; the per-ticket commit/push/PR authorisation |
| `needs_design` | whether stage 2 runs |
| `worktree`, `branch`, `start_commit` | where the work happens and where it started |
| `stage` | current stage key |
| `waiting_on` | `null`, `"user"`, `"agent"` or `"ci"` |
| `waiting_reason` | one sentence when `waiting_on` is not null |
| `updated_at` | ISO 8601 UTC time of the last write |
| `limits` | `coderabbit_rounds`, `qa_rounds`, `stage_retries` |
| `stages.<key>` | `status` (`pending`, `running`, `blocked`, `done`, `skipped`, `failed`), `agent_id`, `rounds`, `outputs`, `artifacts`, `notes` |
| `stages.<key>.artifacts[]` | `{url, title, open_threads, threads_checked_at}`; `open_threads` is the count of unresolved comment threads, `threads_checked_at` when it was counted |
| `attention` | open and resolved items the user must act on; see `attention.md` |
| `prs` | ordered bottom to top: `number`, `branch`, `title`, `qa_fixes` |
| `history` | append-only list of `{at, stage, event, detail}` |

Stage keys, in order: `requirements`, `design`, `plan`, `implement`, `review`, `coderabbit`, `qa`, `qa_fixes`, `test_gaps`, `handoff`.

Older files have no `updated_at`, `repo`, `session_id` or `attention`, history entries without `at`, and artifacts as bare URL strings. Readers accept both forms; the supervisor writes the new form from its next write on.
