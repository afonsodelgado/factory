package host

import (
	"context"
	"encoding/json"
	"time"
)

func (m *Runtime) Run(ctx context.Context, events <-chan Event) error {
	if err := m.Recover(); err != nil {
		return err
	}
	m.Tick()
	timer := time.NewTicker(time.Second)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-timer.C:
			m.Tick()
		case e, ok := <-events:
			if !ok {
				return nil
			}
			if e.T == "hello" || e.T == "gone" {
				continue
			}
			m.Tick()
			e.Respond(m.Handle(e.Message))
		}
	}
}

func (m *Runtime) Handle(request Message) Message {
	if request.Op == "" {
		return m.handle(request)
	}
	return m.Execute(request.Op, request.T, request.Data, func() Message { return m.handle(request) })
}

func (m *Runtime) handle(request Message) Message {
	var value any
	var err error
	switch request.T {
	case "open":
		var r OpenRequest
		if err = json.Unmarshal(request.Data, &r); err == nil {
			value, err = m.Open(r)
		}
	case "delete":
		var r DeleteRequest
		if err = json.Unmarshal(request.Data, &r); err == nil {
			value, err = m.Delete(r)
		}
	case "wake", "pause":
		var r struct{ Key string }
		if err = json.Unmarshal(request.Data, &r); err == nil {
			if request.T == "wake" {
				err = m.Wake(r.Key)
			} else {
				err = m.Pause(r.Key)
			}
		}
	case "archive":
		var r ArchiveRequest
		if err = json.Unmarshal(request.Data, &r); err == nil {
			err = m.Archive(r)
		}
	case "unarchive":
		var r struct{ Key string }
		if err = json.Unmarshal(request.Data, &r); err == nil {
			if it, ok := m.snap.Find(r.Key); ok {
				err = m.Unarchive(it)
			}
		}
	case "stage":
		var r StageRequest
		if err = json.Unmarshal(request.Data, &r); err == nil {
			value, err = m.Stage(r)
		}
	case "review":
		var r ReviewRequest
		if err = json.Unmarshal(request.Data, &r); err == nil {
			value, err = m.Review(r)
		}
	case "snapshot":
		value = m.snap
	default:
		return Message{T: "error", Error: "unknown host action: " + request.T}
	}
	if err != nil {
		return Message{T: "error", Error: err.Error()}
	}
	data, err := json.Marshal(value)
	if err != nil {
		return Message{T: "error", Error: err.Error()}
	}
	return Message{T: "done", Data: data}
}
