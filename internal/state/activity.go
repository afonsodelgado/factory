package state

import (
	"bufio"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

type ActivityEntry struct {
	At     time.Time `json:"at"`
	Source string    `json:"source"`
	Stage  string    `json:"stage,omitempty"`
	Text   string    `json:"text"`
}

func ActivityPath(activityDir, repo, slug string) string {
	return filepath.Join(activityDir, filepath.Base(repo)+"-"+slug+".jsonl")
}

func AppendActivity(path string, entry ActivityEntry) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	data, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	_, err = f.Write(append(data, '\n'))
	return err
}

func ReadActivity(path string) ([]ActivityEntry, error) {
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var entries []ActivityEntry
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var entry ActivityEntry
		if json.Unmarshal(scanner.Bytes(), &entry) == nil {
			entries = append(entries, entry)
		}
	}
	return entries, scanner.Err()
}
