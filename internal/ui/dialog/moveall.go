package dialog

import (
	"context"
	"fmt"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/google/uuid"

	"github.com/afonsodelgado/factory/internal/node"
	"github.com/afonsodelgado/factory/internal/ui/theme"
)

const batchOutcomeX = 24

func (n *Nodes) takesFactories(row NodeRow) bool {
	p := n.peer(row.ID)
	return p.answered && p.err == nil && p.answer.Report.Problems == 0
}

func (n *Nodes) moveAll(row NodeRow) tea.Cmd {
	actions, op := n.actions, uuid.NewString()
	n.screen, n.batch, n.problem = moveAllScreen, node.BatchProgress{Operation: op, TargetName: row.Short(), Running: true}, ""
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), checkWait)
		defer cancel()
		b, err := actions.MoveAll(ctx, op, row.Name)
		return batchArrived{batch: b, err: err}
	}
}

func (n *Nodes) batchArrived(msg batchArrived) tea.Cmd {
	if msg.err != nil {
		n.problem = msg.err.Error()
		n.batch.Running = n.batch.Running && len(n.batch.Items) > 0
	} else {
		n.batch = msg.batch
	}
	if !n.batch.Running {
		return nil
	}
	return tea.Tick(movePoll, func(time.Time) tea.Msg { return batchTick{} })
}

func (n *Nodes) pollBatch() tea.Cmd {
	actions, op := n.actions, n.batch.Operation
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), checkWait)
		defer cancel()
		b, err := actions.Batch(ctx, op)
		return batchArrived{batch: b, err: err}
	}
}

func (n *Nodes) moveAllKey(key string) tea.Cmd {
	switch {
	case key == "esc":
		n.screen = peerDetailsScreen
	case key == "x" && n.batch.Running && !n.batch.Cancelled:
		actions, op := n.actions, n.batch.Operation
		return func() tea.Msg {
			b, err := actions.CancelAll(context.Background(), op)
			return batchArrived{batch: b, err: err}
		}
	}
	return nil
}

func (n *Nodes) drawMoveAllConfirm(c *theme.Canvas) {
	row, _ := n.selected()
	text := fmt.Sprintf("Move every factory on %s to %s? Each one stops at its next checkpoint and resumes on %s. One that can't move stays on %s, with the reason.", n.Context.Machine, row.Short(), row.Short(), n.Context.Machine)
	paragraph(c, c.H-5, text, theme.FgInk(theme.Yellow))
	c.Spans(1, c.H-1, []theme.Span{{Text: "y", Ink: theme.Key}, {Text: " move all   ", Ink: theme.Text}, {Text: "n", Ink: theme.Key}, {Text: " keep them here", Ink: theme.Text}})
}

func (n *Nodes) drawMoveAll(c *theme.Canvas) {
	b := n.batch
	c.Put(1, 0, "Moving everything on "+n.Context.Machine+" to "+b.TargetName, theme.Bright.B())
	c.Right(c.W-2, 0, n.batchSummary(), theme.Muted)
	y := 2
	if len(b.Items) == 0 && b.Running {
		c.Put(3, y, "◌ listing the factories that can move…", theme.FgInk(theme.Cyan))
		y++
	}
	for _, item := range b.Items {
		text, ink := batchOutcome(item, b.TargetName)
		c.PutMax(3, y, item.Factory, theme.Bright, batchOutcomeX-4)
		c.PutMax(batchOutcomeX, y, text, ink, c.W-batchOutcomeX-1)
		y++
	}
	y = paragraph(c, y+1, "Each factory moves on its own; a finished move is never undone.", theme.Muted) + 1
	n.drawProblem(c, y)
	hints := "esc back"
	if b.Running && !b.Cancelled {
		hints = "x cancel the moves not released yet   esc back; they keep going"
	}
	c.Put(1, c.H-1, hints, theme.Text)
}

func (n *Nodes) batchSummary() string {
	b := n.batch
	moved := 0
	for _, item := range b.Items {
		if item.State == node.StateActive {
			moved++
		}
	}
	switch {
	case b.Cancelled && b.Running:
		return "cancelling…"
	case b.Running:
		return fmt.Sprintf("%d of %d moved", moved, len(b.Items))
	}
	return fmt.Sprintf("%d moved, %d not moved", moved, len(b.Items)-moved)
}

func batchOutcome(item node.ItemOutcome, target string) (string, theme.Ink) {
	switch item.State {
	case node.StateActive:
		return "✓ moved to " + target, theme.FgInk(theme.Green)
	case node.StateCancelled:
		return "– not moved: cancelled", theme.Muted
	case node.StateQueued:
		return "· waiting its turn", theme.Muted
	case node.StateRecoveryNeeded, node.StateInterrupted:
		return "! stopped mid-move: " + item.Reason + "; factory doctor shows how to recover it", theme.FgInk(theme.Yellow)
	case node.StateRefused, node.StatePreflightFailed:
		return "✗ not moved: " + item.Reason, theme.FgInk(theme.Red)
	case node.StateCheckpointWaiting:
		return "◌ waiting for a checkpoint", theme.FgInk(theme.Cyan)
	case node.StateStaged:
		return "◌ " + target + " has the checkpoint", theme.FgInk(theme.Cyan)
	case node.StateReleased:
		return "◌ released; " + target + " is starting it", theme.FgInk(theme.Cyan)
	}
	return "◌ moving", theme.FgInk(theme.Cyan)
}
