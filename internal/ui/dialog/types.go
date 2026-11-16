package dialog

import (
	"encoding/json"

	tea "charm.land/bubbletea/v2"

	"github.com/afonsodelgado/factory/internal/catalog"
	"github.com/afonsodelgado/factory/internal/state"
)

type Existing struct {
	Key      string `json:"key"`
	Worktree string `json:"worktree"`
	Label    string `json:"label"`
	Status   string `json:"status"`
	Stage    string `json:"stage"`
	Archived bool   `json:"archived"`
	Retired  bool   `json:"retired"`
	Remote   string `json:"remote,omitempty"`
}

const UnarchiveAdvice = "In the factory list, H shows archived factories and a on the row unarchives it."

type RepoOption struct {
	Name       string     `json:"name"`
	Path       string     `json:"path"`
	Display    string     `json:"display"`
	Browser    bool       `json:"browser"`
	CodeRabbit bool       `json:"coderabbit"`
	Existing   []Existing `json:"existing"`

	Pipelines       []state.Pipeline `json:"pipelines"`
	DefaultPipeline string           `json:"default_pipeline"`
	PipelineProblem string           `json:"pipeline_problem,omitempty"`
	Agents          []AgentOption    `json:"agents"`

	Supervisor  state.AgentSetting `json:"supervisor,omitzero"`
	GitURL      string             `json:"git_url,omitempty"`
	NoGitRemote string             `json:"no_git_remote,omitempty"`
}

type AgentOption struct {
	Role string `json:"role"`
	state.AgentSetting
}

type NewContext struct {
	StateDir  string       `json:"state_dir"`
	Notice    string       `json:"notice,omitempty"`
	Repos     []RepoOption `json:"repos"`
	Repo      int          `json:"repo"`
	Here      bool         `json:"here,omitempty"`
	Tools     []ToolOption `json:"tools,omitempty"`
	Remotes   []string     `json:"remotes,omitempty"`
	Default   string       `json:"default,omitempty"`
	Elsewhere []Existing   `json:"elsewhere,omitempty"`
}

type ToolOption struct {
	Name    string             `json:"name"`
	Models  []catalog.Model    `json:"models,omitempty"`
	Default state.AgentSetting `json:"default,omitzero"`
	Problem string             `json:"problem,omitempty"`
}

type NewResult struct {
	Settings   *SettingsContext              `json:"settings,omitempty"`
	Repo       string                        `json:"repo"`
	Slug       string                        `json:"slug,omitempty"`
	Prompt     string                        `json:"prompt,omitempty"`
	Pipeline   string                        `json:"pipeline,omitempty"`
	Stages     []string                      `json:"stages,omitempty"`
	Agents     map[string]state.AgentSetting `json:"agents,omitempty"`
	Supervisor state.AgentSetting            `json:"supervisor,omitzero"`
	Select     string                        `json:"select,omitempty"`
	Wake       bool                          `json:"wake,omitempty"`
	Remote     string                        `json:"remote,omitempty"`
}

type SettingsContext struct {
	Repo   string `json:"repo"`
	Banner string `json:"banner,omitempty"`
}

type SettingsResult struct {
	Repo  string `json:"repo"`
	Saved bool   `json:"saved"`
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

type PauseContext struct {
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

type DeleteContext struct {
	Key          string              `json:"key"`
	Label        string              `json:"label"`
	Title        string              `json:"title"`
	Worktree     string              `json:"worktree"`
	WorktreeGone bool                `json:"worktree_gone"`
	Branches     []string            `json:"branches"`
	Dirty        []string            `json:"dirty"`
	Unpushed     map[string][]string `json:"unpushed"`
	Remote       string              `json:"remote,omitempty"`
	Repo         string              `json:"repo,omitempty"`
	Unreachable  bool                `json:"unreachable,omitempty"`
}

func (c DeleteContext) HasWork() bool {
	return len(c.Dirty) > 0 || len(c.Unpushed) > 0
}

type DeleteResult struct {
	Key       string `json:"key"`
	Confirmed bool   `json:"confirmed"`
	Forget    bool   `json:"forget,omitempty"`
}

type RemotesContext struct {
	Remotes []RemoteRow `json:"remotes"`
	Default string      `json:"default"`
	Local   int         `json:"local"`
	Push    state.Push  `json:"push"`
}

type RemoteRow struct {
	Name        string `json:"name"`
	Endpoint    string `json:"endpoint"`
	Link        string `json:"link"`
	Factories   int    `json:"factories"`
	NeedsYou    int    `json:"needs_you"`
	PushPending bool   `json:"push_pending,omitempty"`
}

type CheckRow struct {
	Name   string `json:"name"`
	Mark   string `json:"mark"`
	Detail string `json:"detail"`
	Fix    string `json:"fix"`
}

type RemoteActions interface {
	Add(name, target, identity string) error
	Check(name string) []CheckRow
	SetDefault(name string) error
	Remove(name string) error
	SetPush(p state.Push) error
	NewTopic() string
	TestPush() string
}

type KeysContext struct {
	Remotes bool `json:"remotes"`
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
	case "settings":
		var c SettingsContext
		json.Unmarshal(context, &c)
		return NewSettings(c), true
	case "switch":
		var c SwitchContext
		json.Unmarshal(context, &c)
		return &Switch{Context: c}, true
	case "pause":
		var c PauseContext
		json.Unmarshal(context, &c)
		return &Pause{Context: c}, true
	case "archive":
		var c ArchiveContext
		json.Unmarshal(context, &c)
		return &Archive{Context: c}, true
	case "delete":
		var c DeleteContext
		json.Unmarshal(context, &c)
		return &Delete{Context: c}, true
	case "keys":
		var c KeysContext
		json.Unmarshal(context, &c)
		return &Keys{Context: c}, true
	}
	return nil, false
}

func (r RepoOption) Support() state.RepoSupport {
	return state.RepoSupport{CodeRabbit: r.CodeRabbit, BrowserQA: r.Browser}
}
