package factory

import "testing"

func TestCreateCommand(t *testing.T) {
	noPromptInBrowserRepo := Launch{Repo: "/r/cowork", Slug: "eng-740", SessionID: "5f0c9d2e", Chrome: true, RemoteControl: true}
	want := "claude --worktree eng-740 --session-id 5f0c9d2e --chrome --remote-control eng-740 '/factory eng-740'; cd '/r/cowork/.claude/worktrees/eng-740'"
	if got := noPromptInBrowserRepo.CreateCommand(); got != want {
		t.Errorf("factory without a prompt runs =\n%s\nwant\n%s", got, want)
	}

	promptInTerminalRepo := Launch{Repo: "/r/factory", Slug: "csv-export", Prompt: "Export contacts as CSV.\nIt's for ops, who'd rather not wait.\n", SessionID: "2b7e4c1a", RemoteControl: true}
	want = "claude --worktree csv-export --session-id 2b7e4c1a --remote-control csv-export '/factory csv-export\nExport contacts as CSV.\nIt'\\''s for ops, who'\\''d rather not wait.'; cd '/r/factory/.claude/worktrees/csv-export'"
	if got := promptInTerminalRepo.CreateCommand(); got != want {
		t.Errorf("factory with a prompt runs =\n%s\nwant\n%s", got, want)
	}
}
