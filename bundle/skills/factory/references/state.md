# state.json

One file per factory at `<worktree>/.claude/factory/<id>/state.json`. The supervisor is the only writer; the factory app and its node only read it. `factory attention resolve` changes nothing: it prints the edit to make, and the supervisor makes it.

| Field | Meaning |
|---|---|
| `id`, `title` | the factory id and a short title taken from the prompt |
| `repo` | the main checkout's absolute path |
| `session_id` | the supervisor's Claude Code session id, from `$CLAUDE_CODE_SESSION_ID`, refreshed on every write because it can change mid-session |
| `authorized` | `true` once `/factory` started; the per-factory commit/push/PR authorisation |
| `needs_design` | whether stage 2 runs: true exactly when `design` is in `pipeline_stages` |
| `pipeline` | the name of the pipeline this factory runs, such as `full` or `quick`, or `custom` when it was started with `--stages` |
| `pipeline_stages` | the resolved stage keys of that pipeline, in stage order; every other stage is `skipped` with the note "not in the <name> pipeline" |
| `agents` | the effective tool, model and effort of every agent role, such as `{"engineer": {"tool": "claude", "model": "sonnet", "effort": "medium"}, …}`, one entry per role; passed to the Agent tool on every spawn of a claude role, and read by the factory app to start a role on another tool |
| `worktree`, `branch`, `start_commit` | where the work happens and where it started |
| `stage` | current stage key |
| `waiting_on` | `null`, `"user"`, `"agent"` or `"ci"` |
| `waiting_reason` | one sentence when `waiting_on` is not null |
| `updated_at` | ISO 8601 UTC time of the last write, from `date -u +%Y-%m-%dT%H:%M:%SZ` run just before it; never written by hand |
| `limits` | `coderabbit_rounds`, `qa_rounds`, `stage_retries` |
| `stages.<key>` | `status` (`pending`, `running`, `blocked`, `done`, `skipped`, `failed`), `agent_id`, `rounds`, `outputs`, `artifacts`, `notes` |
| `stages.<key>.artifacts[]` | `{url, title, open_threads, threads_checked_at}`; `url` is `file://<absolute path>`, a web URL, or after a checkpoint a path relative to the factory directory; `open_threads` is the count of unresolved comment threads, `threads_checked_at` when it was counted |
| `attention` | open and resolved items the user must act on; see `attention.md` |
| `prs` | ordered bottom to top: `number`, `branch`, `title`, `qa_fixes` |
| `history` | append-only list of `{at, stage, event, detail}`; `at` comes from `date -u` like `updated_at` |
| `checkpoint` | absent until the node asks for one; then `{operation, status, reason, at}`, where `status` is `ready` or `refused` and `reason` says why a refused checkpoint cannot move (SKILL.md, "Checkpoint") |

Stage keys, in order: `requirements`, `design`, `plan`, `implement`, `review`, `coderabbit`, `qa`, `qa_fixes`, `test_gaps`, `walkthrough`, `handoff`. Factories started before `walkthrough` existed have no entry for it; treat it as `skipped`.

Factories started before pipelines have no `pipeline` or `pipeline_stages`; they run every stage, as `full` does. Factories started before agent models have no `agents`; every agent runs on its frontmatter. Factories started before every role was written list only the roles that differ from their frontmatter; the factory app runs a missing role as it resolved it when the factory was created, or on its frontmatter when it has no record of that.

Older files have `ticket` (a string or `null`) instead of `id`, and no `updated_at`, `repo`, `session_id` or `attention`, history entries without `at`, and artifacts as bare URL strings. Readers accept both forms; the supervisor writes the new form from its next write on.
