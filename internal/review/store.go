package review

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/afonsodelgado/factory/internal/state"
)

const (
	App       = "app"
	Prototype = "prototype"
)

func Live(page string) bool {
	return page == App || page == Prototype
}

const (
	Draft    = "draft"
	Sent     = "sent"
	Resolved = "resolved"
)

var intents = []string{"fix", "change", "question"}

type Rect struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
	W float64 `json:"w"`
	H float64 `json:"h"`
}

type Anchor struct {
	URL        string   `json:"url,omitempty"`
	Selector   string   `json:"selector,omitempty"`
	Text       string   `json:"text,omitempty"`
	HTML       string   `json:"html,omitempty"`
	Rect       *Rect    `json:"rect,omitempty"`
	Viewport   *Rect    `json:"viewport,omitempty"`
	Components []string `json:"components,omitempty"`
}

type Message struct {
	By   string    `json:"by"`
	Text string    `json:"text"`
	At   time.Time `json:"at"`
}

type Thread struct {
	ID       string    `json:"id"`
	Page     string    `json:"page"`
	Anchor   Anchor    `json:"anchor"`
	Intent   string    `json:"intent"`
	Status   string    `json:"status"`
	Batch    int       `json:"batch,omitempty"`
	Shot     string    `json:"shot,omitempty"`
	Messages []Message `json:"messages"`
	Op       string    `json:"op,omitempty"`
}

func (t Thread) Open() bool { return t.Status != Resolved }

type threadsFile struct {
	Next    int      `json:"next"`
	Batches int      `json:"batches"`
	Threads []Thread `json:"threads"`
}

type Store struct {
	sending sync.Mutex
	mu      sync.Mutex
	path    string
	now     func() time.Time
	data    threadsFile
	console []string
}

func StorePath(factoryDir string) string {
	return filepath.Join(factoryDir, "review", "threads.json")
}

func OpenStore(factoryDir string, now func() time.Time) (*Store, error) {
	s := &Store{path: StorePath(factoryDir), now: now}
	data, err := os.ReadFile(s.path)
	if errors.Is(err, fs.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, &s.data); err != nil {
		return nil, fmt.Errorf("%s: %w", s.path, err)
	}
	return s, nil
}

type NewThread struct {
	Page      string `json:"page"`
	Anchor    Anchor `json:"anchor"`
	Intent    string `json:"intent"`
	Text      string `json:"text"`
	Operation string `json:"operation,omitempty"`
}

func (s *Store) Create(n NewThread) (Thread, error) {
	text := strings.TrimSpace(n.Text)
	if text == "" {
		return Thread{}, errors.New("a comment needs text")
	}
	if !validIntent(n.Intent) {
		return Thread{}, fmt.Errorf("intent %q is not one of %s", n.Intent, strings.Join(intents, ", "))
	}
	s.sending.Lock()
	defer s.sending.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if t, ok := s.created(n.Operation); ok {
		return t, nil
	}
	s.data.Next++
	t := Thread{
		ID:       fmt.Sprintf("t-%d", s.data.Next),
		Page:     n.Page,
		Anchor:   n.Anchor,
		Intent:   n.Intent,
		Status:   Draft,
		Messages: []Message{{By: "you", Text: text, At: s.now().UTC()}},
		Op:       n.Operation,
	}
	s.data.Threads = append(s.data.Threads, t)
	return t, s.save()
}

func (s *Store) created(operation string) (Thread, bool) {
	for _, t := range s.data.Threads {
		if operation != "" && t.Op == operation {
			return t, true
		}
	}
	return Thread{}, false
}

func validIntent(intent string) bool {
	for _, i := range intents {
		if intent == i {
			return true
		}
	}
	return false
}

type Change struct {
	Text   *string `json:"text,omitempty"`
	Intent *string `json:"intent,omitempty"`
	Status *string `json:"status,omitempty"`
}

func (s *Store) Update(id string, c Change) (Thread, error) {
	s.sending.Lock()
	defer s.sending.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	t, err := s.find(id)
	if err != nil {
		return Thread{}, err
	}
	if c.Text != nil {
		text := strings.TrimSpace(*c.Text)
		if text == "" || t.Status != Draft {
			return Thread{}, errors.New("only an unsent comment's text can change, and it cannot be empty")
		}
		t.Messages[len(t.Messages)-1].Text = text
	}
	if c.Intent != nil {
		if !validIntent(*c.Intent) {
			return Thread{}, fmt.Errorf("intent %q is not one of %s", *c.Intent, strings.Join(intents, ", "))
		}
		t.Intent = *c.Intent
	}
	if c.Status != nil {
		if *c.Status != Resolved && *c.Status != Sent {
			return Thread{}, fmt.Errorf("status %q cannot be set by hand", *c.Status)
		}
		t.Status = *c.Status
	}
	return *t, s.save()
}

func (s *Store) Reply(id, by, text string, resolve bool) (Thread, error) {
	text = strings.TrimSpace(text)
	if text == "" && !resolve {
		return Thread{}, errors.New("a reply needs text")
	}
	s.sending.Lock()
	defer s.sending.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	t, err := s.find(id)
	if err != nil {
		return Thread{}, err
	}
	if text != "" {
		t.Messages = append(t.Messages, Message{By: by, Text: text, At: s.now().UTC()})
	}
	switch {
	case resolve:
		t.Status = Resolved
	case by == "you":
		t.Status = Draft
	}
	return *t, s.save()
}

func (s *Store) Delete(id string) error {
	s.sending.Lock()
	defer s.sending.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, t := range s.data.Threads {
		if t.ID == id {
			s.data.Threads = append(s.data.Threads[:i], s.data.Threads[i+1:]...)
			return s.save()
		}
	}
	return fmt.Errorf("no thread %s", id)
}

func (s *Store) Threads(page string) []Thread {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Thread
	for _, t := range s.data.Threads {
		if page == "" || t.Page == page {
			out = append(out, t)
		}
	}
	return out
}

func (s *Store) OpenCount(page string) int {
	count := 0
	for _, t := range s.Threads(page) {
		if t.Open() {
			count++
		}
	}
	return count
}

func (s *Store) Drafts(page string) []Thread {
	var drafts []Thread
	for _, t := range s.Threads(page) {
		if t.Status == Draft {
			drafts = append(drafts, t)
		}
	}
	return drafts
}

func (s *Store) Send(page string, deliver func(drafts []Thread, batch int) error) ([]Thread, error) {
	s.sending.Lock()
	defer s.sending.Unlock()
	drafts := s.Drafts(page)
	if len(drafts) == 0 {
		return nil, errors.New("there are no unsent comments on this page")
	}
	batch := s.nextBatch()
	if err := deliver(drafts, batch); err != nil {
		return nil, err
	}
	ids := make([]string, len(drafts))
	for i, t := range drafts {
		ids[i] = t.ID
	}
	return drafts, s.markSent(batch, ids)
}

func (s *Store) markSent(batch int, ids []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data.Batches = batch
	for _, id := range ids {
		t, err := s.find(id)
		if err != nil {
			return err
		}
		t.Status, t.Batch = Sent, batch
	}
	return s.save()
}

func (s *Store) SetShot(id, rel string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, err := s.find(id)
	if err != nil {
		return err
	}
	t.Shot = rel
	return s.save()
}

func (s *Store) AddConsole(lines []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.console = append(s.console, lines...)
}

func (s *Store) TakeConsole() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	lines := s.console
	s.console = nil
	return lines
}

func (s *Store) nextBatch() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.data.Batches + 1
}

func (s *Store) find(id string) (*Thread, error) {
	for i := range s.data.Threads {
		if s.data.Threads[i].ID == id {
			return &s.data.Threads[i], nil
		}
	}
	return nil, fmt.Errorf("no thread %s", id)
}

func (s *Store) save() error {
	data, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return err
	}
	return state.WriteFileAtomic(s.path, append(data, '\n'))
}
