package remote

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/google/uuid"

	"github.com/afonsodelgado/factory/internal/state"
)

const Protocol = 1

type Status string

const (
	Rejected  Status = "rejected"
	Completed Status = "completed"
	Failed    Status = "failed"
	Uncertain Status = "uncertain"
)

type Request struct {
	Protocol int             `json:"protocol"`
	Op       string          `json:"op,omitempty"`
	Server   string          `json:"server,omitempty"`
	Action   string          `json:"action"`
	Data     json.RawMessage `json:"data,omitempty"`
}

type Reply struct {
	Protocol int             `json:"protocol"`
	Server   string          `json:"server"`
	Status   Status          `json:"status"`
	Error    string          `json:"error,omitempty"`
	Data     json.RawMessage `json:"data,omitempty"`
}

type Hello struct {
	Server   string `json:"server"`
	Release  string `json:"release"`
	Protocol int    `json:"protocol"`
	Platform string `json:"platform"`
}

type Create struct {
	ID         string                        `json:"id"`
	GitURL     string                        `json:"git_url"`
	Prompt     string                        `json:"prompt,omitempty"`
	Pipeline   string                        `json:"pipeline,omitempty"`
	Stages     []string                      `json:"stages,omitempty"`
	Agents     map[string]state.AgentSetting `json:"agents,omitempty"`
	Supervisor state.AgentSetting            `json:"supervisor,omitzero"`
	Profile    []byte                        `json:"profile,omitempty"`
	Rules      []byte                        `json:"rules,omitempty"`
}

type Listing struct {
	ID          string `json:"id"`
	GitURL      string `json:"git_url"`
	Incarnation string `json:"incarnation"`
}

type Target struct {
	ID string `json:"id"`
}

type Removal struct {
	What string `json:"what"`
	Risk string `json:"risk,omitempty"`
}

func ServerID(stateDir string) (string, error) {
	path := filepath.Join(stateDir, "server-id")
	data, err := os.ReadFile(path)
	if err == nil {
		return string(data), nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(stateDir, ".server-id.*")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	id := uuid.NewString()
	_, err = tmp.WriteString(id)
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return "", err
	}
	if err := os.Link(tmp.Name(), path); errors.Is(err, fs.ErrExist) {
		return ServerID(stateDir)
	} else if err != nil {
		return "", err
	}
	return id, nil
}
