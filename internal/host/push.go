package host

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/afonsodelgado/factory/internal/remote"
)

const (
	pushTimeout  = 10 * time.Second
	pushEvery    = 2 * time.Second
	pushFirstGap = 5 * time.Second
	pushMaxGap   = 10 * time.Minute
)

var pushKinds = map[string]string{
	"question":   "question",
	"gate":       "gate",
	"comment":    "comment",
	"cap":        "cap tripped",
	"failure":    "failure",
	"permission": "permission",
	"test":       "test",
}

type Pusher struct {
	StateDir string
	Now      func() time.Time
	mu       sync.Mutex
}

type pushFile struct {
	Settings remote.PushSettings `json:"settings"`
	Queue    []pushJob           `json:"queue,omitempty"`
}

type pushJob struct {
	ID    string    `json:"id"`
	Text  string    `json:"text"`
	Tries int       `json:"tries,omitempty"`
	Next  time.Time `json:"next,omitzero"`
}

func (p *Pusher) path() string { return filepath.Join(p.StateDir, "push.json") }

func (p *Pusher) load() pushFile {
	var f pushFile
	if data, err := os.ReadFile(p.path()); err == nil {
		json.Unmarshal(data, &f)
	}
	return f
}

func (p *Pusher) Configure(s remote.PushSettings) (remote.PushSettings, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	f := p.load()
	if s.Revision <= f.Settings.Revision {
		return f.Settings, nil
	}
	f.Settings = s
	if s.Topic == "" {
		f.Queue = nil
	}
	return s, savePrivate(p.path(), f)
}

func (p *Pusher) Enqueue(a remote.Attention) {
	p.mu.Lock()
	defer p.mu.Unlock()
	f := p.load()
	if f.Settings.Topic == "" {
		return
	}
	f.Queue = append(f.Queue, pushJob{ID: uuid.NewString(), Text: pushText(a.Factory, f.Settings.Label, a.Kind)})
	savePrivate(p.path(), f)
}

func pushText(id, label, kind string) string {
	text, known := pushKinds[kind]
	if !known {
		text = "needs you"
	}
	return id + " on " + label + ": " + text
}

func (p *Pusher) Flush() {
	p.mu.Lock()
	f := p.load()
	p.mu.Unlock()
	now := p.Now()
	delivered := map[string]bool{}
	for _, job := range f.Queue {
		if !job.Next.After(now) {
			delivered[job.ID] = send(f.Settings, job.Text) == nil
		}
	}
	if len(delivered) == 0 {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	f = p.load()
	var kept []pushJob
	for _, job := range f.Queue {
		ok, tried := delivered[job.ID]
		if tried && ok {
			continue
		}
		if tried {
			job.Tries++
			job.Next = now.Add(min(pushFirstGap<<min(job.Tries-1, 10), pushMaxGap))
		}
		kept = append(kept, job)
	}
	f.Queue = kept
	savePrivate(p.path(), f)
}

func (p *Pusher) Test() error {
	p.mu.Lock()
	settings := p.load().Settings
	p.mu.Unlock()
	if settings.Topic == "" {
		return fmt.Errorf("phone push is off on this server; turn it on with factory remote push")
	}
	return send(settings, pushText("factory", settings.Label, "test"))
}

func (p *Pusher) Run(ctx context.Context) {
	tick := time.NewTicker(pushEvery)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			p.Flush()
		}
	}
}

func send(s remote.PushSettings, text string) error {
	ctx, cancel := context.WithTimeout(context.Background(), pushTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(s.Server, "/")+"/"+s.Topic, strings.NewReader(text))
	if err != nil {
		return err
	}
	req.Header.Set("Title", "factory")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("the push server answered %d", resp.StatusCode)
	}
	return nil
}

func (m *Runtime) configurePush(s remote.PushSettings) (remote.PushSettings, error) {
	if m.Push == nil {
		return remote.PushSettings{}, fmt.Errorf("phone push runs only in a server's host")
	}
	return m.Push.Configure(s)
}

func (m *Runtime) testPush() error {
	if m.Push == nil {
		return fmt.Errorf("phone push runs only in a server's host")
	}
	return m.Push.Test()
}
