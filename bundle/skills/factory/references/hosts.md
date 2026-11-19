# Supervisor tools

Claude, Codex and OpenCode use the same durable workflow protocol. Start every stage through `factory stage start --role <role> --effect <committed effect id> --task <path>`; follow-ups use stage send with a newly recorded run. The node selects the recorded tool/model/effort and delivers completion through the inbox. Discovery is a recorded side run, not a stage: `factory stage discover --task <path>` opens the discoverer beside the active stage, on Claude, and its completion arrives through the inbox as `stage discoverer done: <result>`. QA remains Claude-only. Do not use an unrecorded Agent call as a substitute.

If a skill or tool is unavailable, report that prerequisite rather than claiming its work passed. Use noninteractive `gh stack view --json`. The supervisor alone invokes commit-state.py with the current session identity; nodes and stage agents never call it or write state.json.

Wait in scripts or node-managed panes rather than repeated model turns. Reconcile a pending effect after restart using recorded pane intent, live pane and matching result. An uncertain dispatch stays blocked pending reconciliation; transport retries reuse the logical effect and never consume a correction cycle.
