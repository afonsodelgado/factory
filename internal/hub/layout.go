package hub

import (
	"fmt"
	"math"
	"slices"
	"strings"

	"github.com/afonsodelgado/factory/internal/host"
	"github.com/afonsodelgado/factory/internal/tmux"
	"github.com/afonsodelgado/factory/internal/ui/frame"
)

const (
	HubWindow           = "hub"
	SlotParking         = "hub-slot"
	StageParking        = "s-"
	SelectorWidth       = 35
	NarrowSelectorWidth = 5
	NarrowAt            = 100
	PanelShare          = 1.0 / 3
	AnsweringShare      = 2.0 / 3
	MinWidth            = 60
	MinHeight           = 24
	frameRows           = 5
	frameColumns        = 4
	splitGutter         = 3
)

var FrameSides = []string{"left", "right", "top", "bottom"}

type Engine struct {
	TM          tmux.Client
	Session     string
	Env         []string
	ViewCommand func(kind string) string
	home        tmux.Pane
}

func (e *Engine) Owns(p tmux.Pane) bool {
	return p.Session == e.Session
}

type Layout struct {
	Window   string
	Selector string
	Slot     string
	Stage    string
	Split    string
	Panel    string
	Footer   string
	Frame    Frame
}

type Frame struct {
	Top, Bottom, Left, Right string
}

func (f *Frame) pane(side string) *string {
	switch side {
	case "left":
		return &f.Left
	case "right":
		return &f.Right
	case "top":
		return &f.Top
	}
	return &f.Bottom
}

func (f Frame) Panes() []string {
	return []string{f.Left, f.Right, f.Top, f.Bottom}
}

func (f Frame) Complete() bool {
	return f.Left != "" && f.Right != "" && f.Top != "" && f.Bottom != ""
}

func (f Frame) Has(pane string) bool {
	return pane != "" && slices.Contains(f.Panes(), pane)
}

type Shape struct {
	Width     int
	Height    int
	Share     float64
	Collapsed bool
}

func (s Shape) SelectorWidth() int {
	if s.Width <= NarrowAt {
		return NarrowSelectorWidth
	}
	return SelectorWidth
}

func (s Shape) RightWidth() int {
	return max(s.Width-s.SelectorWidth()-1, 1)
}

func (s Shape) rightRows() int {
	return s.Height - 2
}

func (s Shape) PanelHeight() int {
	if s.Collapsed {
		return 1
	}
	share := s.Share
	if share == 0 {
		share = PanelShare
	}
	return max(int(math.Round(float64(s.rightRows()-frameRows)*share)), 3)
}

func (s Shape) FramedHeight() int {
	return max(s.rightRows()-1-s.PanelHeight(), 1)
}

func (s Shape) fits() error {
	if s.Width < MinWidth || s.Height < MinHeight {
		return fmt.Errorf("the hub has %dx%d cells here; it needs at least %dx%d", s.Width, s.Height, MinWidth, MinHeight)
	}
	return nil
}

func (e *Engine) Build(self string, shape Shape) (Layout, error) {
	panes, err := e.TM.Panes()
	if err != nil {
		return Layout{}, err
	}
	me, ok := byID(panes, self)
	if !ok {
		return Layout{}, fmt.Errorf("pane %s not found", self)
	}
	if err := shape.fits(); err != nil {
		return Layout{}, err
	}
	e.home = me
	l := Layout{Window: me.WindowID, Selector: self}
	for _, p := range panes {
		if p.WindowID != me.WindowID || p.ID == self {
			continue
		}
		side, isFrame := strings.CutPrefix(p.UI, "frame-")
		switch {
		case p.UI == "frame-split":
			l.Split = p.ID
		case isFrame:
			*l.Frame.pane(side) = p.ID
		case p.UI == "panel":
			l.Panel = p.ID
		case p.UI == "footer":
			l.Footer = p.ID
		case p.UI == "stage":
			l.Stage = p.ID
		case p.UI == "slot" || p.Slug != "":
			l.Slot = p.ID
		}
	}
	rebuildFrame := l.Slot == "" || l.Panel == "" || !l.Frame.Complete()
	unpairedSplit := (l.Stage == "") != (l.Split == "")
	if rebuildFrame || unpairedSplit {
		if err := e.HideStage(&l); err != nil {
			return l, err
		}
	}
	if rebuildFrame {
		if err := e.removeFrame(&l); err != nil {
			return l, err
		}
	}
	if err := e.claimWindow(l.Window); err != nil {
		return l, err
	}
	if err := e.TM.SetPaneOption(self, "@factory_ui", "selector"); err != nil {
		return l, err
	}
	if l.Footer == "" {
		if l.Footer, err = e.uiPane("footer", tmux.Split{Target: self, Full: true, Size: 1}); err != nil {
			return l, err
		}
	}
	if l.Slot == "" {
		if l.Slot, err = e.slotPane(panes, self, l.Panel, shape); err != nil {
			return l, err
		}
	}
	if l.Panel == "" {
		if l.Panel, err = e.uiPane("panel", tmux.Split{Target: l.Slot, Size: shape.PanelHeight()}); err != nil {
			return l, err
		}
	}
	if rebuildFrame {
		if err := e.frameSlot(&l); err != nil {
			return l, err
		}
	}
	if err := e.ensureSlotViewer(); err != nil {
		return l, err
	}
	for _, input := range append([]string{l.Footer}, l.Frame.Panes()...) {
		if err := e.TM.DisableInput(input); err != nil {
			return l, err
		}
	}
	return l, e.styleSession()
}

func (e *Engine) Measure(self string) (int, int, error) {
	return e.TM.WindowSize(self)
}

func (e *Engine) removeFrame(l *Layout) error {
	for _, pane := range l.Frame.Panes() {
		if pane == "" {
			continue
		}
		if err := e.TM.KillPane(pane); err != nil {
			return err
		}
	}
	l.Frame = Frame{}
	return nil
}

func (e *Engine) frameSlot(l *Layout) error {
	splits := map[string]tmux.Split{
		"left":   {Horizontal: true, Before: true, Size: 1},
		"right":  {Horizontal: true, Size: 1},
		"top":    {Before: true, Size: 1},
		"bottom": {Size: 1},
	}
	for _, side := range FrameSides {
		split := splits[side]
		split.Target = l.Slot
		id, err := e.uiPane("frame-"+side, split)
		if err != nil {
			return err
		}
		*l.Frame.pane(side) = id
	}
	return nil
}

func (e *Engine) slotPane(panes []tmux.Pane, self, panel string, shape Shape) (string, error) {
	place := tmux.Split{Target: self, Horizontal: true, Size: shape.RightWidth()}
	if panel != "" {
		place = tmux.Split{Target: panel, Before: true, Size: shape.FramedHeight()}
	}
	for _, p := range panes {
		if p.UI == "slot" && p.Session == e.Session {
			return p.ID, e.TM.Join(p.ID, place)
		}
	}
	return e.uiPane("slot", place)
}

func (e *Engine) ensureSlotViewer() error {
	panes, err := e.TM.Panes()
	if err != nil {
		return err
	}
	for _, p := range panes {
		if p.UI == "slot" && e.Owns(p) {
			return nil
		}
	}
	id, err := e.TM.NewWindow(tmux.Window{Session: e.Session, Name: SlotParking, Command: e.ViewCommand("slot")})
	if err != nil {
		return err
	}
	return e.TM.SetPaneOption(id, "@factory_ui", "slot")
}

func (e *Engine) uiPane(kind string, split tmux.Split) (string, error) {
	split.Command = e.ViewCommand(kind)
	id, err := e.TM.Split(split)
	if err != nil {
		return "", err
	}
	return id, e.TM.SetPaneOption(id, "@factory_ui", kind)
}

func (e *Engine) styleSession() error {
	options := [][2]string{
		{"status-position", "top"},
		{"popup-border-lines", "rounded"},
		{"status-left-length", "120"},
		{"status-right-length", "80"},
	}
	for _, o := range options {
		if err := e.TM.SetSessionOption(e.Session, o[0], o[1]); err != nil {
			return err
		}
	}
	return nil
}

func (e *Engine) SetHeader(left, right string) {
	e.TM.SetSessionOption(e.Session, "status-left", left)
	e.TM.SetSessionOption(e.Session, "status-right", right)
}

func (e *Engine) claimWindow(window string) error {
	if err := e.TM.RenameWindow(window, HubWindow); err != nil {
		return err
	}
	if e.TM.SetWindowOption(window, "pane-border-lines", "spaces") != nil {
		if err := e.TM.SetWindowOption(window, "pane-border-lines", "single"); err != nil {
			return err
		}
	}
	options := [][2]string{{"pane-border-status", "off"}, {"window-status-format", ""}, {"window-status-current-format", ""}}
	for _, o := range options {
		if err := e.TM.SetWindowOption(window, o[0], o[1]); err != nil {
			return err
		}
	}
	return nil
}

func (e *Engine) BindReturn(key string, l Layout) func() {
	guard := fmt.Sprintf("#{==:#{session_name},%s}", e.Session)
	e.TM.Run("bind-key", "-T", "root", key, "if-shell", "-F", guard, "select-pane -t "+l.Selector, "send-keys "+key)
	return func() { e.TM.Run("unbind-key", "-T", "root", key) }
}

func (e *Engine) Arrange(l Layout, shape Shape) error {
	if err := shape.fits(); err != nil {
		return err
	}
	sizes := []struct {
		pane          string
		width, height int
	}{
		{l.Footer, 0, 1},
		{l.Selector, shape.SelectorWidth(), 0},
		{l.Frame.Left, 1, 0},
		{l.Frame.Right, 1, 0},
		{l.Panel, 0, shape.PanelHeight()},
		{l.Frame.Top, 0, 1},
		{l.Frame.Bottom, 0, 1},
		{l.Split, 1, 0},
	}
	for _, s := range sizes {
		if s.pane == "" {
			continue
		}
		if err := e.TM.ResizePane(s.pane, s.width, s.height); err != nil {
			return err
		}
	}
	return e.FitParking(l)
}

func (e *Engine) SlotSize(l Layout) (int, int, error) {
	g, err := e.groupOf(l.Window, l.Slot)
	if err != nil {
		return 0, 0, err
	}
	return g.cell.Width, g.cell.Height, nil
}

func (e *Engine) FitParking(l Layout) error {
	width, height, err := e.SlotSize(l)
	if err != nil {
		return err
	}
	panes, err := e.TM.Panes()
	if err != nil {
		return err
	}
	if stage, shown := byID(panes, l.Stage); shown && l.Stage != "" {
		width += splitGutter + stage.Width
	}
	_, stageWidth := SplitWidths(width)
	fitted := map[string]bool{}
	for _, p := range panes {
		if p.Session != e.Session || fitted[p.WindowID] || !parked(p) {
			continue
		}
		fitted[p.WindowID] = true
		windowWidth := width
		if strings.HasPrefix(p.WindowName, StageParking) {
			windowWidth = stageWidth
		}
		if err := e.fit(p.WindowID, windowWidth, height); err != nil {
			return err
		}
	}
	return nil
}

func SplitWidths(framed int) (int, int) {
	left := frame.SplitColumn(framed) - 1
	return left, framed - left - splitGutter
}

func (e *Engine) ShowStage(l *Layout, stage string) error {
	if l.Stage == stage {
		return nil
	}
	if err := e.HideStage(l); err != nil {
		return err
	}
	g, err := e.groupOf(l.Window, l.Slot)
	if err != nil {
		return err
	}
	if g.cell.Leaf() {
		err = e.joinStage(l, stage, g.cell.Width)
	} else {
		err = e.joinStageBesideGroup(l, stage, g)
	}
	if err != nil {
		return err
	}
	if err := e.TM.DisableInput(l.Split); err != nil {
		return err
	}
	return e.FitParking(*l)
}

func (e *Engine) joinStage(l *Layout, stage string, width int) error {
	_, stageWidth := SplitWidths(width)
	if err := e.TM.Join(stage, tmux.Split{Target: l.Slot, Horizontal: true, Size: stageWidth}); err != nil {
		return err
	}
	l.Stage = stage
	var err error
	l.Split, err = e.uiPane("frame-split", tmux.Split{Target: l.Slot, Horizontal: true, Size: 1})
	return err
}

func (e *Engine) joinStageBesideGroup(l *Layout, stage string, g group) error {
	if err := e.TM.Join(stage, tmux.Split{Target: widest(g.cell), Horizontal: true}); err != nil {
		return err
	}
	l.Stage = stage
	var err error
	if l.Split, err = e.uiPane("frame-split", tmux.Split{Target: stage, Horizontal: true, Before: true, Size: 1}); err != nil {
		return err
	}
	g.replace(besideStage(g, l.Split, stage))
	return e.TM.ApplyLayout(l.Window, g.window)
}

func (e *Engine) HideStage(l *Layout) error {
	beside, groupErr := e.groupOf(l.Window, l.Slot)
	if err := e.removeSplit(l); err != nil {
		return err
	}
	if l.Stage == "" {
		return nil
	}
	panes, err := e.TM.Panes()
	if err != nil {
		return err
	}
	p, found := byID(panes, l.Stage)
	l.Stage = ""
	if !found || p.WindowID != l.Window {
		return nil
	}
	if err := e.TM.Break(p.ID, e.Session, host.StageParkName(p.StageOf, p.Stage)); err != nil {
		return err
	}
	if groupErr == nil && !beside.cell.Leaf() {
		if err := e.restoreGroup(*l, beside.cell); err != nil {
			return err
		}
	}
	return e.FitParking(*l)
}

func (e *Engine) restoreGroup(l Layout, cell *tmux.Cell) error {
	widened, err := e.groupOf(l.Window, l.Slot)
	if err != nil {
		return err
	}
	widened.replace(cell)
	return e.TM.ApplyLayout(l.Window, widened.window)
}

func (e *Engine) removeSplit(l *Layout) error {
	if l.Split == "" {
		return nil
	}
	split := l.Split
	l.Split = ""
	return e.TM.KillPane(split)
}

func (e *Engine) fit(window string, width, height int) error {
	for name, value := range map[string]string{"window-size": "manual", "pane-border-status": "off", "window-status-format": "", "window-status-current-format": ""} {
		if err := e.TM.SetWindowOption(window, name, value); err != nil {
			return err
		}
	}
	return e.TM.ResizeWindow(window, width, height)
}

func parked(p tmux.Pane) bool {
	return p.WindowName == SlotParking || strings.HasPrefix(p.WindowName, "p-") || strings.HasPrefix(p.WindowName, StageParking)
}

func byID(panes []tmux.Pane, id string) (tmux.Pane, bool) {
	for _, p := range panes {
		if p.ID == id {
			return p, true
		}
	}
	return tmux.Pane{}, false
}
