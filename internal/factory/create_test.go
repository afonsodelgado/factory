package factory

import (
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
}
