package host

import (
	"fmt"
	"strings"
	"testing"
)

func TestParseMenuReadsOnlyRealPromptsAndTheKeysThatAnswerThem(t *testing.T) {
	menus := []struct {
		fixture string
		want    string
	}{
		{"mcp.txt", "1 Use this and all future MCP servers in this project [1] | 2 Use this MCP server [2] | 3 Continue without using this MCP server [3]"},
		{"codex-update.txt", "1 Update now (runs `npm install -g @openai/codex`) [1] | 2 Skip [2] | 3 Skip until next version [3]"},
		{"ssh.txt", "yes yes [yes Enter] | no no [no Enter]"},
	}
	for _, c := range menus {
		menu, ok := ParseMenu(fixtureScreen(t, c.fixture))
		var got []string
		for _, choice := range menu.Choices {
			got = append(got, fmt.Sprintf("%s %s %v", choice.ID, choice.Label, choice.keys))
		}
		if !ok || strings.Join(got, " | ") != c.want {
			t.Errorf("%s parsed to %q, %v; want %q, or the phone types the wrong keys", c.fixture, got, ok, c.want)
		}
	}
	for _, fixture := range []string{"refused.txt", "tool-output.txt"} {
		if menu, ok := ParseMenu(fixtureScreen(t, fixture)); ok {
			t.Errorf("%s parsed as a prompt with %+v; a session at its input box or printing a list must not read as needing you", fixture, menu.Choices)
		}
	}
}
