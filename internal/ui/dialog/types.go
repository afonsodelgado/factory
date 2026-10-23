package dialog

import (
	"encoding/json"

	tea "charm.land/bubbletea/v2"
)

type Existing struct {
	Key      string `json:"key"`
	Worktree string `json:"worktree"`
	Label    string `json:"label"`
	Status   string `json:"status"`
	Stage    string `json:"stage"`
	Archived bool   `json:"archived"`
	Retired  bool   `json:"retired"`
}

const UnarchiveAdvice = "In the factory list, H shows archived factories and a on the row unarchives it."

type RepoOption struct {
	Name       string     `json:"name"`
	Path       string     `json:"path"`
	Display    string     `json:"display"`
	Browser    bool       `json:"browser"`
	CodeRabbit bool       `json:"coderabbit"`
	Existing   []Existing `json:"existing"`
}

type NewContext struct {
	Repos         []RepoOption `json:"repos"`
	Repo          int          `json:"repo"`
	RemoteControl bool         `json:"remote_control"`
}

type NewResult struct {
	Repo   string `json:"repo"`
	Slug   string `json:"slug,omitempty"`
	Prompt string `json:"prompt,omitempty"`
	Select string `json:"select,omitempty"`
	Wake   bool   `json:"wake,omitempty"`
}

type SwitchRow struct {
	Key    string `json:"key"`
	Label  string `json:"label"`
	Title  string `json:"title"`
	Repo   string `json:"repo"`
	Stage  string `json:"stage"`
	Status string `json:"status"`
	Needs  bool   `json:"needs"`
}

type SwitchContext struct {
	Rows []SwitchRow `json:"rows"`
}

type KeyResult struct {
	Key string `json:"key"`
}

type KillContext struct {
	Key        string `json:"key"`
	Label      string `json:"label"`
	Title      string `json:"title"`
	FactoryDir string `json:"factory_dir"`
}

type ArchiveContext struct {
	Key        string   `json:"key"`
	Label      string   `json:"label"`
	Worktree   string   `json:"worktree"`
	Branch     string   `json:"branch"`
	FactoryDir string   `json:"factory_dir"`
	Removable  bool     `json:"removable"`
	Dirty      []string `json:"dirty"`
	Unpushed   []string `json:"unpushed"`
	NoUpstream bool     `json:"no_upstream"`
	Problem    string   `json:"problem"`

	FactoryDirMovesTo string `json:"factory_dir_moves_to"`
}

type ArchiveResult struct {
	Key    string `json:"key"`
	Remove bool   `json:"remove"`
}

type Dialog interface {
	tea.Model
	Result() (any, bool)
}

func New(kind string, context json.RawMessage, width, height int) (Dialog, bool) {
	switch kind {
	case "new":
		var c NewContext
		json.Unmarshal(context, &c)
		return NewFactory(c), true
	case "switch":
		var c SwitchContext
		json.Unmarshal(context, &c)
		return &Switch{Context: c}, true
	case "kill":
		var c KillContext
		json.Unmarshal(context, &c)
		return &Kill{Context: c}, true
	case "archive":
		var c ArchiveContext
		json.Unmarshal(context, &c)
		return &Archive{Context: c}, true
	case "keys":
		return &Keys{}, true
	}
	return nil, false
}
