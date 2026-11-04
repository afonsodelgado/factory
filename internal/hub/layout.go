package hub

import (
	"fmt"
	"math"
	"slices"
	"strings"

	"github.com/afonsodelgado/factory/internal/tmux"
	"github.com/afonsodelgado/factory/internal/ui/frame"
)

const (
	HubWindow           = "hub"
	SlotParking         = "hub-slot"
	KeepAlive           = "hub-keep"
	StageParking        = "s-"
	SelectorWidth       = 34
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

func ParkName(slug string) string {
	return "p-" + slug
}

type Engine struct {
	TM          tmux.Client
	Session     string
	Env         []string
	ViewCommand func(kind string) string
	home        tmux.Pane
	borrowed    map[string]string
}

func (e *Engine) InPlace() bool {
	return e.home.ID != "" && e.home.Session != e.Session
}

func (e *Engine) Owns(p tmux.Pane) bool {
	return p.Session == e.Session || (e.home.WindowID != "" && p.WindowID == e.home.WindowID)
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
	if e.InPlace() {
		if err := e.retireStaleHubs(panes, self); err != nil {
			return Layout{}, err
		}
		if err := e.ensureParking(); err != nil {
			return Layout{}, err
		}
		if panes, err = e.TM.Panes(); err != nil {
			return Layout{}, err
		}
	}
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
		if l.Footer, err = e.uiPane("footer", tmux.Split{Target: self, Full: !e.InPlace(), Size: 1}); err != nil {
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
	if e.InPlace() {
		return l, nil
	}
	return l, e.styleSession()
}

func (e *Engine) Measure(self string) (int, int, error) {
	panes, err := e.TM.Panes()
	if err != nil {
		return 0, 0, err
	}
	me, ok := byID(panes, self)
	if !ok {
		return 0, 0, fmt.Errorf("pane %s not found", self)
	}
	if me.Session == e.Session {
		return e.TM.WindowSize(self)
	}
	for _, p := range panes {
		if p.WindowID == me.WindowID && p.UI == "footer" {
			return p.Width, me.Height + 1 + p.Height, nil
		}
	}
	return me.Width, me.Height, nil
}

func (e *Engine) ensureParking() error {
	if e.TM.HasSession(e.Session) {
		return nil
	}
	_, err := e.TM.NewDetachedSession(tmux.Window{Session: e.Session, Name: KeepAlive, Env: e.Env, Command: "exec tail -f /dev/null"})
	return err
}

func (e *Engine) retireStaleHubs(panes []tmux.Pane, self string) error {
	for _, p := range panes {
		if p.UI != "selector" || p.ID == self {
			continue
		}
		if p.Session != e.Session {
			e.releaseWindow(p.WindowID, p.ID)
			continue
		}
		if err := e.park(panes, p.WindowID); err != nil {
			return err
		}
		if err := e.TM.KillWindow(p.WindowID); err != nil {
			return err
		}
	}
	return nil
}

func (e *Engine) park(panes []tmux.Pane, window string) error {
	for _, p := range panes {
		if p.WindowID != window {
			continue
		}
		name := ""
		switch {
		case p.Slug != "":
			name = ParkName(p.Slug)
		case p.UI == "stage":
			name = StageParkName(p.StageOf, p.Stage)
		}
		if name == "" {
			continue
		}
		if err := e.TM.Break(p.ID, e.Session, name); err != nil {
			return err
		}
	}
	return nil
}

func (e *Engine) Release(l Layout) {
	if !e.InPlace() {
		return
	}
	e.releaseWindow(l.Window, l.Selector)
	for name, saved := range e.borrowed {
		if saved == "" {
			e.TM.UnsetWindowOption(l.Window, name)
			continue
		}
		e.TM.SetWindowOption(l.Window, name, saved)
	}
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
	if e.InPlace() {
		return
	}
	e.TM.SetSessionOption(e.Session, "status-left", left)
	e.TM.SetSessionOption(e.Session, "status-right", right)
}

func (e *Engine) claimWindow(window string) error {
	if e.InPlace() {
		if err := e.borrow(window, "pane-border-lines", "pane-border-status"); err != nil {
			return err
		}
	} else if err := e.TM.RenameWindow(window, HubWindow); err != nil {
		return err
	}
	if e.TM.SetWindowOption(window, "pane-border-lines", "spaces") != nil {
		if err := e.TM.SetWindowOption(window, "pane-border-lines", "single"); err != nil {
			return err
		}
	}
	options := [][2]string{{"pane-border-status", "off"}}
	if !e.InPlace() {
		options = append(options, [2]string{"window-status-format", ""}, [2]string{"window-status-current-format", ""})
	}
	for _, o := range options {
		if err := e.TM.SetWindowOption(window, o[0], o[1]); err != nil {
			return err
		}
	}
	return nil
}

func (e *Engine) releaseWindow(window, selector string) {
	if panes, err := e.TM.Panes(); err == nil {
		e.park(panes, window)
	}
	if panes, err := e.TM.Panes(); err == nil {
		for _, p := range panes {
			if p.WindowID == window && p.UI != "" && p.ID != selector {
				e.TM.KillPane(p.ID)
			}
		}
	}
	e.TM.UnsetPaneOption(selector, "@factory_ui")
}

func (e *Engine) borrow(window string, names ...string) error {
	if e.borrowed != nil {
		return nil
	}
	e.borrowed = map[string]string{}
	for _, name := range names {
		saved, err := e.TM.LocalWindowOption(window, name)
		if err != nil {
			return err
		}
		e.borrowed[name] = saved
	}
	return nil
}

func (e *Engine) BindReturn(key string, l Layout) func() {
	table, binding, fallback := "root", key, "send-keys "+key
	if key == "prefix" {
		table, binding, fallback = "prefix", "f", `command-prompt "find-window -Z -- '%%'"`
	}
	saved := e.binding(table, binding)
	guard := fmt.Sprintf("#{==:#{session_name},%s}", e.Session)
	if e.InPlace() {
		guard = fmt.Sprintf("#{==:#{window_id},%s}", l.Window)
	}
	if strings.Contains(saved, "session_name},"+e.Session) || strings.Contains(saved, "#{window_id},@") {
		saved = ""
	}
	e.TM.Run("bind-key", "-T", table, binding, "if-shell", "-F", guard, "select-pane -t "+l.Selector, fallback)
	return func() {
		e.TM.Run("unbind-key", "-T", table, binding)
		if saved != "" {
			e.TM.RunInput(saved+"\n", "source-file", "-")
		}
	}
}

func (e *Engine) binding(table, key string) string {
	out, err := e.TM.Run("list-keys", "-T", table)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		for i := 0; i+2 < len(fields); i++ {
			if fields[i] == "-T" && fields[i+1] == table && fields[i+2] == key {
				return line
			}
		}
	}
	return ""
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
	panes, err := e.TM.Panes()
	if err != nil {
		return 0, 0, err
	}
	slot, ok := byID(panes, l.Slot)
	if !ok {
		return 0, 0, fmt.Errorf("slot pane %s not found", l.Slot)
	}
	return slot.Width, slot.Height, nil
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
	width, _, err := e.SlotSize(*l)
	if err != nil {
		return err
	}
	_, stageWidth := SplitWidths(width)
	if err := e.TM.Join(stage, tmux.Split{Target: l.Slot, Horizontal: true, Size: stageWidth}); err != nil {
		return err
	}
	l.Stage = stage
	if l.Split, err = e.uiPane("frame-split", tmux.Split{Target: l.Slot, Horizontal: true, Size: 1}); err != nil {
		return err
	}
	if err := e.TM.DisableInput(l.Split); err != nil {
		return err
	}
	return e.FitParking(*l)
}

func (e *Engine) HideStage(l *Layout) error {
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
	if err := e.TM.Break(p.ID, e.Session, StageParkName(p.StageOf, p.Stage)); err != nil {
		return err
	}
	return e.FitParking(*l)
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
	return p.WindowName == SlotParking || p.WindowName == KeepAlive || strings.HasPrefix(p.WindowName, "p-") || strings.HasPrefix(p.WindowName, StageParking)
}

func byID(panes []tmux.Pane, id string) (tmux.Pane, bool) {
	for _, p := range panes {
		if p.ID == id {
			return p, true
		}
	}
	return tmux.Pane{}, false
}
