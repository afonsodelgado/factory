# Supervising from Codex or OpenCode

The factory app sets `FACTORY_SUPERVISOR` to the tool this session runs in: `claude`, `codex` or `opencode`. Unset means `claude`. On Codex and OpenCode, the skill is the same except where it names a tool only Claude Code has. Do this instead:

| The skill says | On Codex or OpenCode |
|---|---|
| `$CLAUDE_CODE_SESSION_ID` | Leave `session_id` as it is. The factory app learns the session from its hooks. |
| Spawn a stage agent with the Agent tool | Every role, `claude` roles included, goes through `"${FACTORY_BIN:-factory}" stage start --role <role> --task <file>`, **without** `--wait`. Record `stage:<role>` as its agent id, set `waiting_on: "agent"`, and end your turn. |
| SendMessage a stage agent | `"${FACTORY_BIN:-factory}" stage send --role <role> --message <file>`, without `--wait`, then end your turn. |
| The agent's final message | A message `stage <role> done: <result file>` arrives in this session when the agent finishes. Read the `factory-result` JSON from that file and continue as the skill says for `done`, `blocked` or `failed`. |
| Workflow fan-out for design variants | Start the variants one after another with `"${FACTORY_BIN:-factory}" stage start --role designer`, waiting for each `done` message. |
| `scripts/wait-for-review.sh` with `run_in_background` | Run `"${FACTORY_BIN:-factory}" stage ci <pr numbers…>` and end your turn. The app waits for CI and CodeRabbit and types the summary into this session. |
| AskUserQuestion | Always ask in plain text, in the factory-app form. |
| Browser QA on a server | `"${FACTORY_BIN:-factory}" stage start --role qa --task <file>`, as for any role. `stage qa done: <file>` arrives once the developer has run it on the laptop and its report is back. |
| ToolSearch for the Chrome tools | Skip the preflight check. QA always runs on `claude`, which `"${FACTORY_BIN:-factory}" stage start --role qa` opens with Chrome. |

Panel messages reach Codex as `$factory approve …`, `$factory skip …` and `$factory resume`, because Codex refuses an unknown `/` command; read them as the `/factory` commands of the same name. OpenCode has no `/factory` command, so its messages start with `Use the factory skill for: /factory`, followed by the same words; OpenCode finds the skill in `~/.claude/skills`, where `factory bootstrap` installs it.
