package hub

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"sort"

	"github.com/afonsodelgado/factory/internal/state"
)

type inboxMessage struct {
	key  string
	text string
}

func (m *Model) queueInbox() {
	if m.Config.StateDir == "" {
		return
	}
	for _, it := range m.snap.Items {
		if it.Factory.Supervisor().OnClaude() || it.Pane == nil || it.Factory.Dir == "" {
			continue
		}
		record := deliveredPath(m.Config.StateDir, it.Factory.Repo, it.Factory.Slug)
		for _, msg := range inboxMessages(m.Config.StateDir, it, readDelivered(record)) {
			if m.inboxQueued[msg.key] {
				continue
			}
			m.inboxQueued[msg.key] = true
			m.typeQueue = append(m.typeQueue, queuedType{Pane: it.Pane.ID, Token: it.Pane.Token, Label: it.Factory.Label(), Text: msg.text, Delivers: msg.key, Record: record})
		}
	}
}

func inboxMessages(stateDir string, it Item, delivered []string) []inboxMessage {
	var messages []inboxMessage
	runs, _ := state.LoadStageRuns(state.StageRunsPath(stateDir, it.Factory.Repo, it.Factory.Slug))
	for _, run := range runs.Runs {
		if run.Finished() && !run.Review && !slices.Contains(delivered, run.Result) {
			messages = append(messages, inboxMessage{key: run.Result, text: "stage " + run.Role + " done: " + run.Result})
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
		messages = append(messages, inboxMessage{key: file, text: note.Text})
	}
	return messages
}

func deliveredPath(stateDir, repo, slug string) string {
	return filepath.Join(stateDir, "delivered", state.FileStem(repo, slug)+".json")
}

func readDelivered(path string) []string {
	var keys []string
	if data, err := os.ReadFile(path); err == nil {
		json.Unmarshal(data, &keys)
	}
	return keys
}

func markDelivered(path, key string) {
	keys := append(readDelivered(path), key)
	data, err := json.Marshal(keys)
	if err != nil || os.MkdirAll(filepath.Dir(path), 0o755) != nil {
		return
	}
	state.WriteFileAtomic(path, data)
}
