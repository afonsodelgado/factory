package host

import (
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"slices"

	"github.com/afonsodelgado/factory/internal/factory"
	"github.com/afonsodelgado/factory/internal/remote"
)

const keptBeforeAnyAck = 1000

type EventLog struct {
	Seq    int64                       `json:"seq"`
	Acked  int64                       `json:"acked"`
	Open   map[string]remote.Attention `json:"open"`
	Events []remote.Attention          `json:"events"`
}

func EventLogPath(stateDir string) string { return filepath.Join(stateDir, "events.json") }

func LoadEventLog(path string) *EventLog {
	j := &EventLog{}
	if data, err := os.ReadFile(path); err == nil {
		json.Unmarshal(data, j)
	}
	if j.Open == nil {
		j.Open = map[string]remote.Attention{}
	}
	return j
}

func attentions(it Item) []remote.Attention {
	f := it.Factory
	base := remote.Attention{Factory: f.Label(), Incarnation: Incarnation(f)}
	var open []remote.Attention
	if f.State != nil && !f.Archived() {
		for _, a := range f.State.OpenAttention() {
			if f.State.Moot(a) {
				continue
			}
			e := base
			e.Item, e.Kind, e.Since = a.ID, a.Kind, a.CreatedAt.Time
			open = append(open, e)
		}
	}
	if it.QA != nil && it.QA.Phase == remote.QAHeld {
		e := base
		e.Item, e.Kind, e.Since = it.QA.ID, "qa", it.QA.CreatedAt
		open = append(open, e)
	}
	if factory.PermissionPrompt(it.Hook) {
		e := base
		e.Item, e.Kind, e.Since = "permission", "permission", it.Hook.Prompt.At
		open = append(open, e)
	}
	return open
}

func (j *EventLog) Record(items []Item) []remote.Attention {
	current := map[string]remote.Attention{}
	unread := map[string]bool{}
	for _, it := range items {
		if it.Factory.State == nil || it.Factory.StateErr != nil {
			unread[Incarnation(it.Factory)] = true
		}
		for _, a := range attentions(it) {
			current[a.Key()] = a
		}
	}
	var opened []remote.Attention
	for _, key := range slices.Sorted(maps.Keys(current)) {
		if _, known := j.Open[key]; known {
			continue
		}
		a := j.append(current[key])
		j.Open[key] = a
		opened = append(opened, a)
	}
	for _, key := range slices.Sorted(maps.Keys(j.Open)) {
		if _, still := current[key]; still || unread[j.Open[key].Incarnation] {
			continue
		}
		resolved := j.Open[key]
		resolved.Resolved = true
		j.append(resolved)
		delete(j.Open, key)
	}
	return opened
}

func (j *EventLog) append(a remote.Attention) remote.Attention {
	j.Seq++
	a.Seq = j.Seq
	j.Events = append(j.Events, a)
	if j.Acked == 0 && len(j.Events) > keptBeforeAnyAck {
		j.Events = j.Events[len(j.Events)-keptBeforeAnyAck:]
	}
	return a
}

func (j *EventLog) After(cursor int64) remote.Events {
	if cursor > j.Acked && cursor <= j.Seq {
		j.Acked = cursor
		j.Events = slices.DeleteFunc(j.Events, func(a remote.Attention) bool { return a.Seq <= cursor })
	}
	later := slices.DeleteFunc(slices.Clone(j.Events), func(a remote.Attention) bool { return a.Seq <= cursor })
	return remote.Events{Seq: j.Seq, Events: later}
}

func (m *Runtime) watch() {
	if m.Log == nil {
		return
	}
	before := m.Log.Seq
	for _, a := range m.Log.Record(m.snap.Items) {
		if m.Push != nil {
			m.Push.Enqueue(a)
		}
	}
	if m.Log.Seq != before {
		saveOrReport(EventLogPath(m.Config.StateDir), m.Log)
	}
}

func (m *Runtime) Events(cursor int64) remote.Events {
	if m.Log == nil {
		return remote.Events{}
	}
	acked := m.Log.Acked
	events := m.Log.After(cursor)
	if m.Log.Acked != acked {
		saveOrReport(EventLogPath(m.Config.StateDir), m.Log)
	}
	return events
}
