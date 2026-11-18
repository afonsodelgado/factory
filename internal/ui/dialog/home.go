package dialog

import (
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/afonsodelgado/factory/internal/node"
	"github.com/afonsodelgado/factory/internal/ui/theme"
)

type homeChoice struct {
	id, name string
}

func (h homeChoice) short() string { return shortName(h.name) }

func (h homeChoice) origin() string { return "https://" + h.name }

func (n *Nodes) homeShort() string {
	if home := shortName(strings.TrimPrefix(n.Context.Home, "https://")); home != "" {
		return home
	}
	return n.Context.Machine
}

func (n *Nodes) chosenHome() homeChoice {
	if row, onPeer := n.selected(); onPeer {
		return homeChoice{id: row.ID, name: row.Name}
	}
	return homeChoice{id: n.Context.Self, name: n.Context.Machine + "." + n.Context.Tailnet}
}

func (n *Nodes) askHome() {
	choice := n.chosenHome()
	if choice.origin() == n.Context.Home {
		n.problem = "The phone app already lives on " + choice.short() + "."
		return
	}
	n.homing = &choice
}

func (n *Nodes) homeKey(key string) {
	choice := *n.homing
	n.homing = nil
	if key != "y" {
		return
	}
	was := n.homeShort()
	roster, err := n.actions.MoveHome(uuid.NewString(), choice.id)
	if err != nil {
		n.problem = err.Error()
		return
	}
	n.Context.Home, n.movedFrom, n.Context.Offer = roster.Home, was, ""
}

func (n *Nodes) drawHomeConfirm(c *theme.Canvas) {
	choice := *n.homing
	top := c.H - 9
	c.Put(1, top, "Make "+choice.short()+" the phone app's home?", theme.Bright.B())
	paragraph(c, top+1, fmt.Sprintf("The app moves to %s. Each phone opens that address once, adds it to the Home Screen again and turns notifications on again. Until it does, the app at %s's address sends it to the new one, and every node keeps notifying it. Nothing changes until you press y.", choice.origin(), n.homeShort()), theme.Text)
	c.Unclip()
	c.Spans(1, c.H-1, []theme.Span{{Text: "y", Ink: theme.Key}, {Text: " move the home to " + choice.short() + "   ", Ink: theme.Text}, {Text: "n", Ink: theme.Key}, {Text: " keep it on " + n.homeShort(), Ink: theme.Text}})
}

func (n *Nodes) drawHomeNews(c *theme.Canvas, y int) int {
	if n.movedFrom != "" {
		y = paragraph(c, y, fmt.Sprintf("✓ The phone app lives on %s now, and every node knows. A phone that opens %s's address is sent to %s.", n.homeShort(), n.movedFrom, n.Context.Home), theme.FgInk(theme.Green)) + 1
		for _, row := range n.Context.Others {
			if p := n.peer(row.ID); p.answered && !p.answer.Snapshot.Supports(node.CanHome) {
				y = paragraph(c, y, fmt.Sprintf("! %s runs factory %s and keeps sending phones to %s's address until factory update reaches it. It still notifies every phone.", row.Short(), p.answer.Snapshot.Version, n.movedFrom), theme.FgInk(theme.Yellow)) + 1
			}
		}
		return y
	}
	if row, ok := n.offered(); ok {
		c.Put(1, y, "⌂", theme.FgInk(theme.Cyan).B())
		lines := theme.Wrap(fmt.Sprintf("%s joined. If it stays on all the time, h makes it the phone app's home, so the app installs, opens and adds nodes while %s sleeps. Shown once.", row.Short(), n.homeShort()), c.W-4)
		for i, line := range lines {
			c.Put(3, y+i, line, theme.FgInk(theme.Cyan))
		}
		y += len(lines) + 1
	}
	return y
}

func (n *Nodes) offered() (NodeRow, bool) {
	for _, row := range n.Context.Others {
		if row.ID == n.Context.Offer {
			return row, true
		}
	}
	return NodeRow{}, false
}
