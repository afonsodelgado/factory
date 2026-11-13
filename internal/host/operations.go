package host

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/google/uuid"

	"github.com/afonsodelgado/factory/internal/state"
)

type Operation struct {
	ID     string          `json:"id"`
	Action string          `json:"action"`
	Data   json.RawMessage `json:"data"`
	Status string          `json:"status"`
	Reply  Message         `json:"reply"`
}

func (m *Runtime) Execute(id, action string, data json.RawMessage, effect func() Message) Message {
	return Journal(m.Config.StateDir, id, action, data, false, effect)
}

func Journal(stateDir, id, action string, data json.RawMessage, resumable bool, effect func() Message) Message {
	if _, err := uuid.Parse(id); err != nil {
		return Message{T: "error", Error: "operation id must be a UUID"}
	}
	path := filepath.Join(stateDir, "operations", id+".json")
	var op Operation
	previous, err := os.ReadFile(path)
	switch {
	case err == nil:
		if err := json.Unmarshal(previous, &op); err != nil {
			return Message{T: "error", Error: err.Error()}
		}
		if op.Action != action || !sameJSON(op.Data, data) {
			return Message{T: "error", Error: "operation id already belongs to a different request"}
		}
		if op.Status == "accepted" && !resumable {
			return Message{T: "uncertain", Error: "outcome unknown; inspect the accepted operation before retrying"}
		}
		if op.Status != "accepted" {
			return op.Reply
		}
	case !errors.Is(err, os.ErrNotExist):
		return Message{T: "error", Error: err.Error()}
	}
	op = Operation{ID: id, Action: action, Data: data, Status: "accepted"}
	if err := savePrivate(path, op); err != nil {
		return Message{T: "error", Error: err.Error()}
	}
	op.Reply = effect()
	op.Status = "completed"
	if op.Reply.T == "error" {
		op.Status = "failed"
	}
	if err := savePrivate(path, op); err != nil {
		return Message{T: "uncertain", Error: fmt.Sprintf("outcome unknown; could not save receipt: %v", err)}
	}
	return op.Reply
}

func sameJSON(a, b json.RawMessage) bool {
	var x, y bytes.Buffer
	json.Compact(&x, a)
	json.Compact(&y, b)
	return bytes.Equal(x.Bytes(), y.Bytes())
}

func saveOrReport(path string, value any) {
	if err := savePrivate(path, value); err != nil {
		fmt.Fprintf(os.Stderr, "factory host: could not save %s, so a push or an alert may be lost: %v\n", filepath.Base(path), err)
	}
}

func savePrivate(path string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if err := state.WriteFileAtomic(path, data); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}

func (m *Runtime) queuePath() string {
	return filepath.Join(m.Config.StateDir, "host", "delivery.json")
}

func (m *Runtime) saveQueue(queue []QueuedType) bool {
	if m.Config.StateDir == "" {
		return true
	}
	if err := savePrivate(m.queuePath(), queue); err != nil {
		m.notice = err.Error()
		return false
	}
	return true
}

func (m *Runtime) Recover() error {
	m.allowRememberedTunnels()
	data, err := os.ReadFile(m.queuePath())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, &m.typeQueue); err != nil {
		return err
	}
	for _, q := range m.typeQueue {
		if q.Answers != nil && m.Queued != nil {
			m.Queued(*q.Answers)
		}
		if q.Delivers != "" {
			m.inboxQueued[q.Delivers] = true
		}
	}
	return nil
}
