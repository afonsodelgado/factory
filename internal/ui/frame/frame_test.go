package frame

import (
	"regexp"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
)

var escapes = regexp.MustCompile("\x1b\\[[0-9;:]*m")

func TestTopStripFitsItsWidthAndKeepsTheLabel(t *testing.T) {
	strip := Render("top", 24, 1, "ENG-7452  Rate limits for failed auth attempts", "~/dev/cowork/.claude/worktrees/eng-7452-auth-limits", true)
	if w := lipgloss.Width(strip); w != 24 || strings.Contains(strip, "\n") || !strings.Contains(escapes.ReplaceAllString(strip, ""), "ENG-7452") {
		t.Errorf("top strip %q is %d cells wide; want exactly 24 on one line with the label kept: an overflowing one-row pane scrolls and shows nothing", strip, w)
	}
}
