# State protocol

`state.json` schema_version is 2. Only the supervisor writes it. The schema and pure transitions are defined by `internal/state/workflow.go` and `internal/workflow`; `factory workflow next <snapshot> <event>` prints a proposal without side effects. A malformed or unsupported snapshot blocks recovery.

Preserve id, incarnation, holder_generation, repo, worktree, branch, start_commit, authorized, agents and supervisor. Revision increases once per new processed event. Workflow holds runs, active_run, pending effect, nested return_to, gate and suspended correction. Each run records stage, role, round, loop, heads, paths and outcome. Local stage receipts cache transport state, never choose the next stage.

Stack entries are ordered bottom to top: branch, parent, head, base and title. PR entries additionally record number, URL and observed head/base SHA. Findings have stable IDs, owning stage/layer, severity, disposition and evidence. Recipe stores commands, working directories, sources and source digest. Verification binds to that digest and ordered head/base tuples. Confirmation records the handoff item, exact heads, receipt and time.

Events require id, kind, revision, incarnation, holder_generation and real UTC at. Kinds: start, commands, comments_preference, effect_received, result, approval, design_return, cap, observe, comments_stop, lost_run, handoff and continue. Developer events additionally carry item_id, artifact_revision, actual answer and receipt or conversation reference. Cap increments must be positive. Agent results cannot grant approval, accept a risk or confirm done.

Stage results use schema_version 2, run_id, input_heads, output_heads, status, findings, evidence and outputs. Status is done, blocked or failed. A clean review/QA uses pass evidence tied to its input heads. Engineer and CI check evidence includes every command, directory, head, exit_status and log path. The supervisor independently checks the output heads and evidence before the result event.

The commit script accepts state path, proposal path and current supervisor session identity. Its OS-released lock prevents concurrent stale writes. It preserves unowned metadata and atomically replaces the file. Duplicate event delivery is a no-op. Never edit state to bypass a rejected proposal.
