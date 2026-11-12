package remote

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"

	"github.com/afonsodelgado/factory/internal/state"
)

const notifiedKept = 500

type Watch struct {
	Server   string   `json:"server"`
	Cursor   int64    `json:"cursor"`
	Notified []string `json:"notified,omitempty"`
}

func watchPath(stateDir, name string) string { return CachePath(stateDir, name+".watch") }

func LoadWatch(stateDir, name string) (Watch, bool) {
	var w Watch
	data, err := os.ReadFile(watchPath(stateDir, name))
	if err != nil || json.Unmarshal(data, &w) != nil {
		return Watch{}, false
	}
	return w, true
}

func SaveWatch(stateDir, name string, w Watch) error {
	path := watchPath(stateDir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(w)
	if err != nil {
		return err
	}
	return state.WriteFileAtomic(path, data)
}

func (w Watch) Take(server string, batch Events) (Watch, []Attention) {
	seeding := w.Server == "" || w.Server != server || batch.Seq < w.Cursor
	if seeding {
		w = Watch{Server: server}
	}
	resolved := map[string]int64{}
	for _, e := range batch.Events {
		if e.Resolved {
			resolved[e.Key()] = e.Seq
		}
	}
	var fresh []Attention
	for _, e := range batch.Events {
		key := e.Key()
		if e.Resolved || resolved[key] > e.Seq || slices.Contains(w.Notified, key) {
			continue
		}
		w.Notified = append(w.Notified, key)
		if !seeding {
			fresh = append(fresh, e)
		}
	}
	w.Notified = w.Notified[max(0, len(w.Notified)-notifiedKept):]
	w.Cursor = batch.Seq
	return w, fresh
}

func (c Client) Events(ctx context.Context, cursor int64) (Events, error) {
	var events Events
	return events, c.Read(ctx, "events", cursor, &events)
}

type pushRevision struct {
	Revision int        `json:"revision"`
	Push     state.Push `json:"push"`
}

func pushRevisionPath(stateDir string) string {
	return filepath.Join(stateDir, "remotes", ".push.json")
}

func loadPushRevision(stateDir string) pushRevision {
	var r pushRevision
	if data, err := os.ReadFile(pushRevisionPath(stateDir)); err == nil {
		json.Unmarshal(data, &r)
	}
	return r
}

func PushRevision(stateDir string, push state.Push) (int, error) {
	r := loadPushRevision(stateDir)
	if r.Revision > 0 && r.Push == push {
		return r.Revision, nil
	}
	r = pushRevision{Revision: r.Revision + 1, Push: push}
	path := pushRevisionPath(stateDir)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return 0, err
	}
	data, err := json.Marshal(r)
	if err != nil {
		return 0, err
	}
	return r.Revision, state.WriteFileAtomic(path, data)
}

func (c Client) SyncPush(ctx context.Context, push state.Push) error {
	revision, err := PushRevision(c.StateDir, push)
	if err != nil {
		return err
	}
	cache, err := LoadCache(c.StateDir, c.Name)
	if err != nil || cache.Push >= revision {
		return err
	}
	var acked int
	settings := PushSettings{Server: push.ServerURL(), Topic: push.Topic, Label: c.Name, Revision: revision}
	if err := c.Read(ctx, "push", settings, &acked); err != nil {
		return err
	}
	return UpdateCache(c.StateDir, c.Name, func(cache *Cache) error {
		cache.Push = acked
		return nil
	})
}

func PushPending(stateDir, name string, push state.Push) bool {
	r := loadPushRevision(stateDir)
	cache, _ := LoadCache(stateDir, name)
	return r.Push != push || cache.Push < r.Revision
}
