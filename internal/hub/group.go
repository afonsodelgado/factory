package hub

import (
	"fmt"
	"slices"

	"github.com/afonsodelgado/factory/internal/tmux"
)

const movingWindow = "hub-moving"

type group struct {
	window   *tmux.Cell
	parent   *tmux.Cell
	from, to int
	cell     *tmux.Cell
	box      tmux.Cell
}

func (e *Engine) groupOf(window, pane string) (group, error) {
	root, err := e.TM.WindowLayout(window)
	if err != nil {
		return group{}, err
	}
	panes, err := e.TM.Panes()
	if err != nil {
		return group{}, err
	}
	chrome := map[string]bool{}
	for _, p := range panes {
		if p.WindowID == window && p.UI != "" && p.UI != "slot" {
			chrome[p.ID] = true
		}
	}
	path := root.Path(pane)
	if path == nil {
		return group{}, fmt.Errorf("pane %s not found in window %s", pane, window)
	}
	return findGroup(root, path, chrome), nil
}

func findGroup(root *tmux.Cell, path []*tmux.Cell, chrome map[string]bool) group {
	holdsChrome := func(c *tmux.Cell) bool {
		return slices.ContainsFunc(c.Leaves(), func(p string) bool { return chrome[p] })
	}
	for i := len(path) - 2; i >= 0; i-- {
		parent := path[i]
		if !slices.ContainsFunc(parent.Children, holdsChrome) {
			continue
		}
		from := slices.Index(parent.Children, path[i+1])
		to := from
		for from > 0 && !holdsChrome(parent.Children[from-1]) {
			from--
		}
		for to < len(parent.Children)-1 && !holdsChrome(parent.Children[to+1]) {
			to++
		}
		return boxed(group{window: root, parent: parent, from: from, to: to, cell: spanOf(parent, from, to)})
	}
	return boxed(group{window: root, cell: root})
}

func boxed(g group) group {
	g.box = tmux.Cell{X: g.cell.X, Y: g.cell.Y, Width: g.cell.Width, Height: g.cell.Height}
	return g
}

func spanOf(parent *tmux.Cell, from, to int) *tmux.Cell {
	if from == to {
		return parent.Children[from]
	}
	cells := slices.Clone(parent.Children[from : to+1])
	first, last := cells[0], cells[len(cells)-1]
	c := &tmux.Cell{Horizontal: parent.Horizontal, Children: cells, X: first.X, Y: first.Y}
	if parent.Horizontal {
		c.Width, c.Height = last.X+last.Width-first.X, parent.Height
	} else {
		c.Width, c.Height = parent.Width, last.Y+last.Height-first.Y
	}
	return c
}

func (g group) replace(with *tmux.Cell) {
	with.Fit(g.box.X, g.box.Y, g.box.Width, g.box.Height)
	if g.parent == nil {
		*g.window = *with
		return
	}
	g.parent.Children = slices.Concat(g.parent.Children[:g.from], flatten(with, g.parent.Horizontal), g.parent.Children[g.to+1:])
}

func flatten(c *tmux.Cell, horizontal bool) []*tmux.Cell {
	if c.Leaf() || c.Horizontal != horizontal {
		return []*tmux.Cell{c}
	}
	return c.Children
}

func (g group) extras(slot string) []string {
	return slices.DeleteFunc(g.cell.Leaves(), func(p string) bool { return p == slot })
}

func (e *Engine) exchange(l Layout, in tmux.Pane) error {
	shown, err := e.groupOf(l.Window, l.Slot)
	if err != nil {
		return err
	}
	parked, err := e.groupOf(in.WindowID, in.ID)
	if err != nil {
		return err
	}
	leaving, arriving := shown.extras(l.Slot), parked.extras(in.ID)
	if len(leaving) == 0 && len(arriving) == 0 {
		return e.TM.Swap(in.ID, l.Slot)
	}
	commands := [][]string{{"swap-pane", "-d", "-s", in.ID, "-t", l.Slot}}
	for _, pane := range arriving {
		commands = append(commands, []string{"break-pane", "-d", "-s", pane, "-t", e.Session + ":", "-n", movingWindow})
	}
	for _, pane := range leaving {
		commands = append(commands, []string{"join-pane", "-d", "-h", "-l", "1", "-s", pane, "-t", l.Slot})
	}
	for _, pane := range arriving {
		commands = append(commands, []string{"join-pane", "-d", "-h", "-l", "1", "-s", pane, "-t", in.ID})
	}
	if err := e.TM.Batch(commands...); err != nil {
		return err
	}
	leavingGroup := shown.cell
	shown.replace(parked.cell)
	leavingGroup.Fit(0, 0, parked.box.Width, parked.box.Height)
	if err := e.TM.ApplyLayout(l.Window, shown.window); err != nil {
		return err
	}
	return e.TM.ApplyLayout(in.WindowID, leavingGroup)
}

func besideStage(g group, split, stage string) *tmux.Cell {
	left, stageWidth := SplitWidths(g.box.Width)
	row := &tmux.Cell{Horizontal: true}
	x := g.box.X
	parts := []struct {
		cell  *tmux.Cell
		width int
	}{{g.cell, left}, {&tmux.Cell{Pane: split}, 1}, {&tmux.Cell{Pane: stage}, stageWidth}}
	for _, part := range parts {
		part.cell.Fit(x, g.box.Y, part.width, g.box.Height)
		row.Children = append(row.Children, flatten(part.cell, true)...)
		x += part.width + 1
	}
	return row
}

func widest(c *tmux.Cell) string {
	if c.Leaf() {
		return c.Pane
	}
	best := c.Children[0]
	for _, child := range c.Children {
		if child.Width > best.Width {
			best = child
		}
	}
	return widest(best)
}
