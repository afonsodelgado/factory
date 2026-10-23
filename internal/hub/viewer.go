package hub

import (
	"bufio"
	"encoding/json"
	"net"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
)

const reconnectEvery = 500 * time.Millisecond

type Viewer struct {
	Kind   string
	Socket string

	conn   net.Conn
	in     chan Message
	frame  string
	width  int
	height int
}

type connectedMsg struct{ conn net.Conn }
type disconnectedMsg struct{}
type retryMsg struct{}
type inboundMsg Message

func (v *Viewer) Init() tea.Cmd {
	return v.dial()
}

func (v *Viewer) dial() tea.Cmd {
	return func() tea.Msg {
		conn, err := net.Dial("unix", v.Socket)
		if err != nil {
			return disconnectedMsg{}
		}
		return connectedMsg{conn}
	}
}

func read(conn net.Conn, in chan<- Message) {
	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		var m Message
		if json.Unmarshal(scanner.Bytes(), &m) == nil {
			in <- m
		}
	}
	in <- Message{T: "disconnected"}
}

func (v *Viewer) wait() tea.Cmd {
	in := v.in
	return func() tea.Msg { return inboundMsg(<-in) }
}

func (v *Viewer) send(m Message) {
	if v.conn == nil {
		return
	}
	data, _ := json.Marshal(m)
	v.conn.Write(append(data, '\n'))
}

func (v *Viewer) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case connectedMsg:
		v.conn = msg.conn
		v.in = make(chan Message, 16)
		go read(msg.conn, v.in)
		v.send(Message{T: "hello", Pane: v.Kind, W: v.width, H: v.height})
		return v, v.wait()
	case disconnectedMsg:
		return v, tea.Tick(reconnectEvery, func(time.Time) tea.Msg { return retryMsg{} })
	case retryMsg:
		return v, v.dial()
	case inboundMsg:
		switch msg.T {
		case "frame":
			reshaped := strings.Count(msg.S, "\n") != strings.Count(v.frame, "\n")
			v.frame = msg.S
			if reshaped {
				return v, tea.Batch(tea.ClearScreen, v.wait())
			}
		case "quit":
			return v, tea.Quit
		case "disconnected":
			v.conn.Close()
			v.conn = nil
			return v, tea.Tick(reconnectEvery, func(time.Time) tea.Msg { return retryMsg{} })
		}
		return v, v.wait()
	case tea.WindowSizeMsg:
		v.width, v.height = msg.Width, msg.Height
		v.send(Message{T: "resize", W: msg.Width, H: msg.Height})
	case tea.KeyPressMsg:
		v.send(Message{T: "key", K: msg.String(), Text: msg.Text})
	case tea.PasteMsg:
		v.send(Message{T: "paste", Text: msg.Content})
	case tea.MouseClickMsg:
		mouse := msg.Mouse()
		v.send(Message{T: "mouse", K: "click", X: mouse.X, Y: mouse.Y})
	case tea.MouseWheelMsg:
		mouse := msg.Mouse()
		v.send(Message{T: "mouse", K: mouse.String(), X: mouse.X, Y: mouse.Y})
	}
	return v, nil
}

func (v *Viewer) View() tea.View {
	view := tea.NewView(v.frame)
	view.AltScreen = true
	if v.Kind != "footer" {
		view.MouseMode = tea.MouseModeCellMotion
	}
	return view
}
