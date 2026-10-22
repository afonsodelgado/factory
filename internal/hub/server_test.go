package hub

import (
	"bufio"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/exp/teatest/v2"

	"github.com/afonsodelgado/factory/internal/factory"
	"github.com/afonsodelgado/factory/internal/state"
)

func TestViewerReceivesFramesAtItsNewSizeAfterAResize(t *testing.T) {
	dir, err := os.MkdirTemp("", "ft")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	socket := filepath.Join(dir, "hub.sock")
	events := make(chan Event, 16)
	server, err := Listen(socket, func(e Event) { events <- e })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { server.Close() })

	model := NewModel(Model{Config: &state.Config{Repos: []string{"/r"}}, Server: server, Events: events})
	model.SetSnapshot(Snapshot{At: time.Now(), Items: []Item{{Factory: factory.Factory{Repo: "/r", Slug: "eng-1", State: &state.State{Ticket: "ENG-1"}}, Status: factory.Running}}})
	tm := teatest.NewTestModel(t, model, teatest.WithInitialTermSize(34, 20))
	t.Cleanup(func() { tm.Quit() })

	conn, err := net.Dial("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	enc := json.NewEncoder(conn)
	for _, m := range []Message{{T: "hello", Pane: "panel", W: 40, H: 10}, {T: "resize", W: 60, H: 12}, {T: "key", K: "esc"}} {
		if err := enc.Encode(m); err != nil {
			t.Fatal(err)
		}
	}

	frames := make(chan string)
	go func() {
		scanner := bufio.NewScanner(conn)
		scanner.Buffer(make([]byte, 64*1024), 1024*1024)
		for scanner.Scan() {
			var m Message
			if json.Unmarshal(scanner.Bytes(), &m) == nil && m.T == "frame" {
				frames <- m.S
			}
		}
	}()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case frame := <-frames:
			lines := strings.Split(frame, "\n")
			if len(lines) == 12 && lipgloss.Width(lines[0]) == 60 && lipgloss.Width(lines[11]) == 60 {
				return
			}
		case <-deadline:
			t.Fatal("no 60x12 frame after the viewer resized to 60x12")
		}
	}
}
