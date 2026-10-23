package factory

import (
	"strings"
	"testing"

	"github.com/afonsodelgado/factory/internal/state"
)

func TestCreateCommand(t *testing.T) {
	noPromptInBrowserRepo := Launch{Repo: "/r/cowork", Slug: "eng-740", SessionID: "5f0c9d2e", Chrome: true, RemoteControl: true}
	want := "claude --worktree eng-740 --session-id 5f0c9d2e --chrome --remote-control eng-740 '/factory eng-740'; cd '/r/cowork/.claude/worktrees/eng-740'"
	if got := noPromptInBrowserRepo.CreateCommand(); got != want {
		t.Errorf("factory without a prompt runs =\n%s\nwant\n%s", got, want)
	}

	promptInTerminalRepo := Launch{Repo: "/r/factory", Slug: "csv-export", Prompt: "Export contacts as CSV.\nIt's for ops, who'd rather not wait.\n", Pipeline: "quick", SessionID: "2b7e4c1a", RemoteControl: true}
	want = "claude --worktree csv-export --session-id 2b7e4c1a --remote-control csv-export '/factory csv-export --pipeline quick\nExport contacts as CSV.\nIt'\\''s for ops, who'\\''d rather not wait.'; cd '/r/factory/.claude/worktrees/csv-export'"
	if got := promptInTerminalRepo.CreateCommand(); got != want {
		t.Errorf("factory with a prompt and the quick pipeline runs =\n%s\nwant\n%s", got, want)
	}

	customWithCheaperAgents := Launch{Repo: "/r/factory", Slug: "csv-export", Prompt: "Export contacts.", Pipeline: state.CustomPipeline, Stages: []string{"requirements", "plan", "implement", "handoff"}, Agents: map[string]state.AgentSetting{"reviewer": {Effort: "low"}, "engineer": {Model: "sonnet", Effort: "medium"}}, SessionID: "2b7e4c1a"}
	want = "claude --worktree csv-export --session-id 2b7e4c1a '/factory csv-export --stages requirements,plan,implement,handoff --agent engineer=sonnet:medium --agent reviewer=:low\nExport contacts.'; cd '/r/factory/.claude/worktrees/csv-export'"
	if got := customWithCheaperAgents.CreateCommand(); got != want {
		t.Errorf("factory with its own stages and agent overrides runs =\n%s\nwant\n%s\nthe skill parses this first line, so its shape is a contract", got, want)
	}
	onCodex := Launch{Repo: "/r/factory", Slug: "csv-export", Prompt: "Export contacts.", Supervisor: state.AgentSetting{Tool: state.ToolCodex, Model: "gpt-5.6-sol", Effort: "high"}, AddDirs: []string{"/r/factory/.git"}}
	want = "git -C '/r/factory' worktree add -b worktree-csv-export '/r/factory/.claude/worktrees/csv-export' origin/HEAD || git -C '/r/factory' worktree add -b worktree-csv-export '/r/factory/.claude/worktrees/csv-export'\ncd '/r/factory/.claude/worktrees/csv-export' && codex -m gpt-5.6-sol -c model_reasoning_effort=high -a never -s workspace-write -c sandbox_workspace_write.network_access=true --add-dir '/r/factory/.git' '$factory csv-export\nExport contacts.'"
	if got := onCodex.CreateCommand(); got != want {
		t.Errorf("a Codex supervisor runs =\n%s\nwant\n%s\nCodex refuses a /factory line, and without the worktree branch delete cannot clean up", got, want)
	}
	onOpenCode := Launch{Repo: "/r/factory", Slug: "csv-export", Prompt: "Export contacts.", Supervisor: state.AgentSetting{Tool: state.ToolOpenCode, Model: "big-pickle"}}
	wantKickoff := ShellQuote("Use the factory skill for: /factory csv-export\nExport contacts.")
	if got := onOpenCode.CreateCommand(); !strings.HasSuffix(got, "--prompt "+wantKickoff) {
		t.Errorf("an OpenCode supervisor runs\n%s\nwant it to end with --prompt %s: OpenCode has no /factory command, so the bare line would never load the skill", got, wantKickoff)
	}
	if got := ForSupervisor(state.ToolOpenCode, "/factory resume"); got != "Use the factory skill for: /factory resume" {
		t.Errorf("a panel message reaches OpenCode as %q; want it to name the skill", got)
	}
}
