package node

import (
	"context"
	"sync"

	"github.com/afonsodelgado/factory/internal/host"
)

type Caller interface {
	Call(ctx context.Context, m host.Message) (host.Message, error)
}

type Queue struct{ events chan host.Event }

func NewQueue() Queue { return Queue{events: make(chan host.Event, 64)} }

func (q Queue) Events() <-chan host.Event { return q.events }

func (q Queue) Sink() chan<- host.Event { return q.events }

func (q Queue) Call(ctx context.Context, m host.Message) (host.Message, error) {
	reply := make(chan host.Message, 1)
	select {
	case q.events <- host.Event{Message: m, Reply: reply}:
	case <-ctx.Done():
		return host.Message{}, ctx.Err()
	}
	select {
	case answer := <-reply:
		return answer, nil
	case <-ctx.Done():
		return host.Message{}, ctx.Err()
	}
}

func (q Queue) post(ctx context.Context, m host.Message) {
	select {
	case q.events <- host.Event{Message: m}:
	case <-ctx.Done():
	}
}

type Jobs struct {
	Queue Queue
	Ctx   context.Context

	mu      sync.Mutex
	running map[string]*job
}

type job struct{ cancel context.CancelFunc }

func (j *Jobs) Start(key string, check func(context.Context) host.Message) {
	ctx, cancel := context.WithCancel(j.Ctx)
	current := &job{cancel: cancel}
	j.mu.Lock()
	if j.running == nil {
		j.running = map[string]*job{}
	}
	if previous := j.running[key]; previous != nil {
		previous.cancel()
	}
	j.running[key] = current
	j.mu.Unlock()
	go func() {
		defer cancel()
		done := check(ctx)
		if j.finish(key, current) && ctx.Err() == nil {
			j.Queue.post(j.Ctx, done)
		}
	}()
}

func (j *Jobs) Cancel(key string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if running := j.running[key]; running != nil {
		running.cancel()
		delete(j.running, key)
	}
}

func (j *Jobs) finish(key string, done *job) bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.running[key] != done {
		return false
	}
	delete(j.running, key)
	return true
}
