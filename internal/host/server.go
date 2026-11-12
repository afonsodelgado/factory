package host

import (
	"bufio"
	"encoding/json"
	"errors"
	"net"
	"os"
	"strings"
	"sync"

	"github.com/afonsodelgado/factory/internal/remote"
)

const MaxMessage = remote.QATotalLimit/3*4 + 1<<20

type Message struct {
	T     string          `json:"t"`
	Op    string          `json:"op,omitempty"`
	Pane  string          `json:"pane,omitempty"`
	S     string          `json:"s,omitempty"`
	K     string          `json:"k,omitempty"`
	Text  string          `json:"text,omitempty"`
	W     int             `json:"w,omitempty"`
	H     int             `json:"h,omitempty"`
	X     int             `json:"x,omitempty"`
	Y     int             `json:"y,omitempty"`
	Data  json.RawMessage `json:"data,omitempty"`
	Error string          `json:"error,omitempty"`
}

type Event struct {
	Message
	From  *Conn
	Reply chan Message
}

func (e Event) Respond(m Message) {
	switch {
	case e.Reply != nil:
		e.Reply <- m
	case e.From != nil:
		e.From.Send(m)
	}
}

type Conn struct {
	pane string
	c    net.Conn
	mu   sync.Mutex
	enc  *json.Encoder
}

func (c *Conn) Send(m Message) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.enc.Encode(m)
}

func (c *Conn) Pane() string {
	return c.pane
}

type Server struct {
	ln      net.Listener
	deliver func(Event)
	mu      sync.Mutex
	viewers map[string]*Conn
}

func Listen(path string, deliver func(Event)) (*Server, error) {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	s := &Server{ln: ln, deliver: deliver, viewers: map[string]*Conn{}}
	go s.accept()
	return s, nil
}

func (s *Server) accept() {
	for {
		c, err := s.ln.Accept()
		if err != nil {
			return
		}
		go s.serve(&Conn{c: c, enc: json.NewEncoder(c)})
	}
}

func (s *Server) serve(c *Conn) {
	defer func() {
		c.c.Close()
		s.mu.Lock()
		if s.viewers[c.pane] == c {
			delete(s.viewers, c.pane)
		}
		s.mu.Unlock()
		if c.pane != "" {
			s.deliver(Event{Message: Message{T: "gone", Pane: c.pane}, From: c})
		}
	}()
	scanner := bufio.NewScanner(c.c)
	scanner.Buffer(make([]byte, 64*1024), MaxMessage)
	for scanner.Scan() {
		var m Message
		if json.Unmarshal(scanner.Bytes(), &m) != nil {
			continue
		}
		if m.T == "hello" {
			c.pane = m.Pane
			s.mu.Lock()
			if old := s.viewers[m.Pane]; old != nil && old != c && isViewer(m.Pane) {
				old.c.Close()
			}
			if isViewer(m.Pane) {
				s.viewers[m.Pane] = c
			}
			s.mu.Unlock()
		}
		m.Pane = c.pane
		s.deliver(Event{Message: m, From: c})
	}
}

func isViewer(pane string) bool {
	return pane == "panel" || pane == "footer" || pane == "slot" || strings.HasPrefix(pane, "frame-")
}

func (s *Server) Send(pane string, m Message) {
	s.mu.Lock()
	c := s.viewers[pane]
	s.mu.Unlock()
	if c != nil {
		c.Send(m)
	}
}

func (s *Server) Broadcast(m Message) {
	s.mu.Lock()
	conns := make([]*Conn, 0, len(s.viewers))
	for _, c := range s.viewers {
		conns = append(conns, c)
	}
	s.mu.Unlock()
	for _, c := range conns {
		c.Send(m)
	}
}

func (s *Server) Close() error {
	return s.ln.Close()
}
