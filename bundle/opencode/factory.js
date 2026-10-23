import { spawn } from "node:child_process"

const FACTORY = "factory"

const childSessions = new Set()

function record(payload) {
  const hook = spawn(FACTORY, ["hook"], { stdio: ["pipe", "ignore", "ignore"] })
  hook.on("error", () => {})
  hook.stdin.on("error", () => {})
  hook.stdin.end(JSON.stringify(payload))
}

export const Factory = async ({ directory }) => {
  const report = (sessionID, event, fields = {}) => {
    if (!sessionID || childSessions.has(sessionID)) return
    record({ session_id: sessionID, cwd: directory, hook_event_name: event, ...fields })
  }
  return {
    event: async ({ event }) => {
      const props = event.properties ?? {}
      switch (event.type) {
        case "session.created":
          if (props.info?.parentID) {
            childSessions.add(props.info.id)
            return
          }
          return report(props.info?.id, "SessionStart", { source: "startup" })
        case "session.idle":
          return report(props.sessionID, "Stop")
        case "permission.asked":
          return report(props.sessionID, "PermissionRequest")
        case "permission.replied":
          return report(props.sessionID, "PostToolUse")
      }
    },
    "chat.message": async (input, output) => {
      const prompt = (output?.parts ?? []).map((part) => part.text ?? "").join("")
      report(input.sessionID, "UserPromptSubmit", { prompt })
    },
    "tool.execute.before": async (input) => report(input.sessionID, "PreToolUse", { tool_name: input.tool }),
    "tool.execute.after": async (input) => report(input.sessionID, "PostToolUse", { tool_name: input.tool }),
  }
}
