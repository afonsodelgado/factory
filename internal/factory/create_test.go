package factory

import "testing"

func TestCreateCommand(t *testing.T) {
	ticketInBrowserRepo := Launch{Repo: "/r/cowork", Slug: "eng-740-cors", Ticket: "ENG-740", SessionID: "5f0c9d2e", Chrome: true, RemoteControl: true}
	want := "claude --worktree eng-740-cors --session-id 5f0c9d2e --chrome --remote-control eng-740-cors '/factory ENG-740'; cd '/r/cowork/.claude/worktrees/eng-740-cors'"
	if got := ticketInBrowserRepo.CreateCommand(); got != want {
		t.Errorf("ticket factory command =\n%s\nwant\n%s", got, want)
	}

	briefInTerminalRepo := Launch{Repo: "/r/factory", Slug: "csv-export", Brief: "Export contacts as CSV.\nIt's for ops, who'd rather not wait.\n", SessionID: "2b7e4c1a", RemoteControl: true}
	want = "claude --worktree csv-export --session-id 2b7e4c1a --remote-control csv-export '/factory csv-export\nExport contacts as CSV.\nIt'\\''s for ops, who'\\''d rather not wait.'; cd '/r/factory/.claude/worktrees/csv-export'"
	if got := briefInTerminalRepo.CreateCommand(); got != want {
		t.Errorf("no-ticket factory command =\n%s\nwant\n%s", got, want)
	}
}
