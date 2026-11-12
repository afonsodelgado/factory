package remote

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"

	"github.com/afonsodelgado/factory/internal/factory"
	"github.com/afonsodelgado/factory/internal/state"
)

type View struct {
	Home      string    `json:"home"`
	Factories []Factory `json:"factories"`
}

type Factory struct {
	ID          string                `json:"id"`
	Slug        string                `json:"slug"`
	Incarnation string                `json:"incarnation"`
	Repo        string                `json:"repo"`
	RepoName    string                `json:"repo_name,omitempty"`
	Worktree    string                `json:"worktree,omitempty"`
	State       *state.State          `json:"state,omitempty"`
	StateError  string                `json:"state_error,omitempty"`
	StateMod    time.Time             `json:"state_mod,omitzero"`
	Entry       *state.Entry          `json:"entry,omitempty"`
	Status      factory.Status        `json:"status"`
	Unblocked   factory.Status        `json:"unblocked"`
	Hook        *state.HookFile       `json:"hook,omitempty"`
	Pane        *Pane                 `json:"pane,omitempty"`
	Stage       *Stage                `json:"stage,omitempty"`
	Activity    []state.ActivityEntry `json:"activity,omitempty"`
}

type Pane struct {
	ID    string `json:"id"`
	Token string `json:"token"`
	Path  string `json:"path,omitempty"`
}

type Stage struct {
	Run  state.StageRun `json:"run"`
	Pane *Pane          `json:"pane,omitempty"`
}

type PaneTarget struct {
	Target
	Role  string `json:"role,omitempty"`
	Token string `json:"token"`
}

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
	ID          string `json:"id"`
	Incarnation string `json:"incarnation,omitempty"`
}

type Removal struct {
	What         string              `json:"what"`
	Risk         string              `json:"risk,omitempty"`
	Worktree     string              `json:"worktree,omitempty"`
	WorktreeGone bool                `json:"worktree_gone,omitempty"`
	Branches     []string            `json:"branches,omitempty"`
	Dirty        []string            `json:"dirty,omitempty"`
	Unpushed     map[string][]string `json:"unpushed,omitempty"`
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
