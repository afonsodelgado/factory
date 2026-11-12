package host

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"sort"

	"github.com/afonsodelgado/factory/internal/state"
)

type InboxMessage struct {
	Key  string
	Text string
}

func (m *Runtime) QueueInbox() {
	if m.Config.StateDir == "" {
		return
	}
	queued := len(m.typeQueue)
	for _, it := range m.snap.Items {
		if it.Factory.Supervisor().OnClaude() || it.Pane == nil || it.Factory.Dir == "" {
			continue
		}
		record := DeliveredPath(m.Config.StateDir, it.Factory.Repo, it.Factory.Slug)
		for _, msg := range InboxMessages(m.Config.StateDir, it, ReadDelivered(record)) {
			if m.inboxQueued[msg.Key] {
				continue
			}
			m.inboxQueued[msg.Key] = true
			m.typeQueue = append(m.typeQueue, QueuedType{Pane: it.Pane.ID, Token: it.Pane.Token, Label: it.Factory.Label(), Text: msg.Text, Delivers: msg.Key, Record: record})
		}
	}
	if len(m.typeQueue) > queued {
		m.saveQueue(m.typeQueue)
	}
}

func InboxMessages(stateDir string, it Item, delivered []string) []InboxMessage {
	var messages []InboxMessage
	runs, _ := state.LoadStageRuns(state.StageRunsPath(stateDir, it.Factory.Repo, it.Factory.Slug))
	for _, run := range runs.Runs {
		if run.Finished() && !run.Review && !slices.Contains(delivered, run.Result) {
			messages = append(messages, InboxMessage{Key: run.Result, Text: "stage " + run.Role + " done: " + run.Result})
		}
	}
	files, _ := filepath.Glob(filepath.Join(it.Factory.Dir, "inbox", "*.json"))
	sort.Strings(files)
	for _, file := range files {
		var note struct {
			Text string `json:"text"`
		}
		data, err := os.ReadFile(file)
		if err != nil || json.Unmarshal(data, &note) != nil || note.Text == "" || slices.Contains(delivered, file) {
			continue
		}
		messages = append(messages, InboxMessage{Key: file, Text: note.Text})
	}
	return messages
}

func DeliveredPath(stateDir, repo, slug string) string {
	return filepath.Join(stateDir, "delivered", state.FileStem(repo, slug)+".json")
}

func ReadDelivered(path string) []string {
	var keys []string
	if data, err := os.ReadFile(path); err == nil {
		json.Unmarshal(data, &keys)
	}
	return keys
}

func MarkDelivered(path, key string) {
	keys := append(ReadDelivered(path), key)
	data, err := json.Marshal(keys)
	if err != nil || os.MkdirAll(filepath.Dir(path), 0o755) != nil {
		return
	}
	state.WriteFileAtomic(path, data)
}
