package hub

import (
	"fmt"
	"math"
	"strings"

	"github.com/afonsodelgado/factory/internal/tmux"
)

const (
	HubWindow           = "hub"
	SlotParking         = "hub-slot"
	SelectorWidth       = 34
	NarrowSelectorWidth = 5
	NarrowAt            = 100
	PanelShare          = 1.0 / 3
	AnsweringShare      = 2.0 / 3
)

func ParkName(slug string) string {
	return "p-" + slug
}

type Engine struct {
	TM          tmux.Client
	Session     string
	ViewCommand func(kind string) string
}

type Layout struct {
	Window   string
	Selector string
	Slot     string
	Panel    string
	Footer   string
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

func (s Shape) PanelHeight() int {
	above := s.Height - 4
	if s.Collapsed {
		return 1
	}
	share := s.Share
	if share == 0 {
		share = PanelShare
	}
	return max(int(math.Round(float64(above)*share)), 3)
}

const borderFormat = `#{?#{@factory_title},#{?pane_active,#[fg=#7aa2f7 bold],#[fg=#737aa2 bold]} ✻ #{@factory_title}#[default] #[align=right]#[fg=#565f89]#{?#{@factory_path},#{@factory_path},#{pane_current_path}} ,}`

func (e *Engine) Build(self string, shape Shape) (Layout, error) {
	panes, err := e.TM.Panes()
	if err != nil {
		return Layout{}, err
	}
	me, ok := byID(panes, self)
	if !ok {
		return Layout{}, fmt.Errorf("pane %s not found", self)
	}
	if me.Session != e.Session {
		return Layout{}, fmt.Errorf("the hub runs in the %q tmux session, not %q: start it with factory open", e.Session, me.Session)
	}
	l := Layout{Window: me.WindowID, Selector: self}
	for _, p := range panes {
		if p.WindowID != me.WindowID || p.ID == self {
			continue
		}
		switch {
		case p.UI == "panel":
			l.Panel = p.ID
		case p.UI == "footer":
			l.Footer = p.ID
		case p.UI == "slot" || p.Slug != "":
			l.Slot = p.ID
		}
	}
	if err := e.TM.RenameWindow(me.WindowID, HubWindow); err != nil {
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
	if err := e.ensureSlotViewer(); err != nil {
		return l, err
	}
	if err := e.TM.DisableInput(l.Footer); err != nil {
		return l, err
	}
	if err := e.styleSession(); err != nil {
		return l, err
	}
	return l, e.styleWindow(l.Window)
}

func (e *Engine) slotPane(panes []tmux.Pane, self, panel string, shape Shape) (string, error) {
	place := tmux.Split{Target: self, Horizontal: true, Size: shape.RightWidth()}
	if panel != "" {
		place = tmux.Split{Target: panel, Before: true, Size: max(shape.Height-4-shape.PanelHeight(), 1)}
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
		if p.UI == "slot" && p.Session == e.Session {
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

func (e *Engine) styleWindow(window string) error {
	if e.TM.SetWindowOption(window, "pane-border-lines", "spaces") != nil {
		if err := e.TM.SetWindowOption(window, "pane-border-lines", "single"); err != nil {
			return err
		}
	}
	options := [][2]string{
		{"pane-border-status", "top"},
		{"pane-border-format", borderFormat},
		{"window-status-format", ""},
		{"window-status-current-format", ""},
	}
	for _, o := range options {
		if err := e.TM.SetWindowOption(window, o[0], o[1]); err != nil {
			return err
		}
	}
	return nil
}

func (e *Engine) BindReturn(key, selector string) func() {
	table, binding, fallback := "root", key, "send-keys "+key
	if key == "prefix" {
		table, binding, fallback = "prefix", "f", `command-prompt "find-window -Z -- '%%'"`
	}
	saved := e.binding(table, binding)
	guard := fmt.Sprintf("#{==:#{session_name},%s}", e.Session)
	if strings.Contains(saved, "session_name},"+e.Session) {
		saved = ""
	}
	e.TM.Run("bind-key", "-T", table, binding, "if-shell", "-F", guard, "select-pane -t "+selector, fallback)
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
	if err := e.TM.ResizePane(l.Footer, 0, 1); err != nil {
		return err
	}
	if err := e.TM.ResizePane(l.Selector, shape.SelectorWidth(), 0); err != nil {
		return err
	}
	if err := e.TM.ResizePane(l.Panel, 0, shape.PanelHeight()); err != nil {
		return err
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
	fitted := map[string]bool{}
	for _, p := range panes {
		if p.Session != e.Session || fitted[p.WindowID] || !parked(p) {
			continue
		}
		fitted[p.WindowID] = true
		if err := e.fit(p.WindowID, width, height); err != nil {
			return err
		}
	}
	return nil
}

func (e *Engine) fit(window string, width, height int) error {
	if err := e.TM.SetWindowOption(window, "window-size", "manual"); err != nil {
		return err
	}
	if err := e.styleWindow(window); err != nil {
		return err
	}
	return e.TM.ResizeWindow(window, width, height+1)
}

func parked(p tmux.Pane) bool {
	return p.WindowName == SlotParking || strings.HasPrefix(p.WindowName, "p-")
}

func byID(panes []tmux.Pane, id string) (tmux.Pane, bool) {
	for _, p := range panes {
		if p.ID == id {
			return p, true
		}
	}
	return tmux.Pane{}, false
}
