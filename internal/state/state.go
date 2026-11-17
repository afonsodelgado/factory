package state

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"time"
)

var StageKeys = []string{"requirements", "design", "plan", "implement", "review", "qa", "pr", "handoff"}

type State struct {
	SchemaVersion    int                     `json:"schema_version"`
	Revision         uint64                  `json:"revision"`
	Incarnation      string                  `json:"incarnation"`
	HolderGeneration uint64                  `json:"holder_generation"`
	Authorized       bool                    `json:"authorized"`
	StartCommit      string                  `json:"start_commit"`
	ProcessedEvents  []string                `json:"processed_events"`
	Workflow         Workflow                `json:"workflow"`
	Stack            []StackBranch           `json:"stack"`
	Loops            Loops                   `json:"loops"`
	WatchComments    bool                    `json:"watch_comments"`
	Recipe           Recipe                  `json:"recipe"`
	Findings         []Finding               `json:"findings"`
	Evidence         Evidence                `json:"evidence"`
	Confirmation     *Confirmation           `json:"confirmation,omitempty"`
	ID               string                  `json:"id"`
	Title            string                  `json:"title"`
	Repo             string                  `json:"repo"`
	SessionID        string                  `json:"session_id"`
	NeedsDesign      bool                    `json:"needs_design"`
	Pipeline         string                  `json:"pipeline"`
	PipelineStages   []string                `json:"pipeline_stages"`
	Agents           map[string]AgentSetting `json:"agents"`
	Supervisor       AgentSetting            `json:"supervisor,omitzero"`
	Worktree         string                  `json:"worktree"`
	Branch           string                  `json:"branch"`
	Stage            string                  `json:"stage"`
	WaitingOn        string                  `json:"waiting_on"`
	WaitingReason    string                  `json:"waiting_reason"`
	UpdatedAt        Time                    `json:"updated_at"`
	Limits           Limits                  `json:"limits"`
	Stages           map[string]Stage        `json:"stages"`
	Attention        []Attention             `json:"attention"`
	PRs              []PR                    `json:"prs"`
	History          []HistoryEntry          `json:"history"`
	Checkpoint       *Checkpoint             `json:"checkpoint,omitempty"`
}

const (
	CheckpointReady   = "ready"
	CheckpointRefused = "refused"
)

type Checkpoint struct {
	Operation string `json:"operation"`
	Status    string `json:"status"`
	Reason    string `json:"reason,omitempty"`
	At        Time   `json:"at"`
}

type Limits struct {
	DesignPlanRounds int `json:"design_plan_rounds"`
	ReviewFixRounds  int `json:"review_fix_rounds"`
	QAFixRounds      int `json:"qa_fix_rounds"`
	CIFixRounds      int `json:"ci_fix_rounds"`
	StageRetries     int `json:"stage_retries"`
}

func DefaultLimits() Limits { return Limits{3, 5, 3, 3, 1} }

type Stage struct {
	Status    string     `json:"status"`
	AgentID   string     `json:"agent_id"`
	Rounds    int        `json:"rounds"`
	Outputs   []string   `json:"outputs"`
	Artifacts []Artifact `json:"artifacts"`
	Notes     string     `json:"notes"`
}

type Artifact struct {
	URL              string `json:"url"`
	Title            string `json:"title"`
	OpenThreads      *int   `json:"open_threads"`
	ThreadsCheckedAt Time   `json:"threads_checked_at"`
}

func (a *Artifact) UnmarshalJSON(data []byte) error {
	var url string
	if json.Unmarshal(data, &url) == nil {
		*a = Artifact{URL: url}
		return nil
	}
	type plain Artifact
	return json.Unmarshal(data, (*plain)(a))
}

type PR struct {
	Number  int    `json:"number"`
	Branch  string `json:"branch"`
	Title   string `json:"title"`
	URL     string `json:"url"`
	Base    string `json:"base"`
	HeadSHA string `json:"head_sha"`
	BaseSHA string `json:"base_sha"`
}

type HistoryEntry struct {
	At     Time   `json:"at"`
	Stage  string `json:"stage"`
	Event  string `json:"event"`
	Detail string `json:"detail"`
	RunID  string `json:"run_id,omitempty"`
	Role   string `json:"role,omitempty"`
	Round  int    `json:"round,omitempty"`
	Loop   string `json:"loop,omitempty"`
}

type Attention struct {
	ID          string      `json:"id"`
	Revision    uint64      `json:"revision"`
	Loop        string      `json:"loop,omitempty"`
	LimitKey    string      `json:"limit_key,omitempty"`
	Used        int         `json:"used,omitempty"`
	Allowed     int         `json:"allowed,omitempty"`
	Answer      string      `json:"answer,omitempty"`
	Receipt     string      `json:"receipt,omitempty"`
	Kind        string      `json:"kind"`
	Title       string      `json:"title"`
	Body        string      `json:"body"`
	Questions   []string    `json:"questions"`
	Options     OptionLists `json:"options"`
	Recommended Strings     `json:"recommended"`
	ArtifactURL string      `json:"artifact_url"`
	ThreadID    string      `json:"thread_id"`
	StageKey    string      `json:"stage"`
	CreatedAt   Time        `json:"created_at"`
	ResolvedAt  Time        `json:"resolved_at"`
}

func (a Attention) Open() bool {
	return a.ResolvedAt.IsZero()
}

func (a Attention) Stage() string { return a.StageKey }

func (s *State) Moot(a Attention) bool { return false }

func (s *State) HasAttentionField() bool {
	return s.Attention != nil
}

func (s *State) OpenAttention() []Attention {
	var open []Attention
	for _, item := range s.Attention {
		if item.Open() && !s.Moot(item) {
			open = append(open, item)
		}
	}
	return open
}

func (s *State) Done() bool {
	return s.Stages["handoff"].Status == "done"
}

func (s *State) Artifacts() []Artifact {
	var all []Artifact
	for _, key := range StageKeys {
		all = append(all, s.Stages[key].Artifacts...)
	}
	return all
}

func (s *State) Anchor(dir string) {
	for _, stage := range s.Stages {
		for i, a := range stage.Artifacts {
			stage.Artifacts[i].URL = Anchored(dir, a.URL)
		}
	}
}

func Anchored(dir, ref string) string {
	u, err := url.Parse(ref)
	portable := err == nil && u.Scheme == "" && ref != "" && filepath.IsLocal(filepath.FromSlash(ref))
	if !portable {
		return ref
	}
	return "file://" + filepath.Join(dir, filepath.FromSlash(ref))
}

func ReadState(path string) (*State, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return ParseState(data)
}

func ParseState(data []byte) (*State, error) {
	var s State
	if err := json.Unmarshal(bytes.TrimSpace(data), &s); err != nil {
		return nil, err
	}
	if err := ValidateState(s); err != nil {
		return nil, err
	}
	return &s, nil
}

func ValidateState(s State) error {
	stages := append([]string{}, s.PipelineStages...)
	if s.Stage != "" {
		stages = append(stages, s.Stage)
	}
	for key := range s.Stages {
		stages = append(stages, key)
	}
	for _, entry := range s.History {
		if entry.Stage != "" {
			stages = append(stages, entry.Stage)
		}
	}
	for _, item := range s.Attention {
		if item.StageKey != "" {
			stages = append(stages, item.StageKey)
		}
	}
	for _, run := range s.Workflow.Runs {
		stages = append(stages, run.Stage)
	}
	if err := checkStages("state.json", stages); err != nil {
		return err
	}
	if err := ValidateAgents("state.json", s.Agents); err != nil {
		return err
	}
	if s.SchemaVersion != 2 {
		return fmt.Errorf("unsupported state schema %d; finish or restart the factory with schema 2", s.SchemaVersion)
	}
	return nil
}

type Strings []string

func (s *Strings) UnmarshalJSON(data []byte) error {
	var one string
	if json.Unmarshal(data, &one) == nil {
		*s = Strings{one}
		return nil
	}
	var many []string
	if err := json.Unmarshal(data, &many); err != nil {
		return err
	}
	*s = many
	return nil
}

type OptionLists [][]string

func (o *OptionLists) UnmarshalJSON(data []byte) error {
	var flat []string
	if json.Unmarshal(data, &flat) == nil {
		*o = OptionLists{flat}
		return nil
	}
	var nested [][]string
	if err := json.Unmarshal(data, &nested); err != nil {
		return err
	}
	*o = nested
	return nil
}

type Time struct {
	time.Time
}

func (t *Time) UnmarshalJSON(data []byte) error {
	var text string
	if json.Unmarshal(data, &text) != nil || text == "" {
		*t = Time{}
		return nil
	}
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05", "2006-01-02 15:04:05", "2006-01-02"} {
		if parsed, err := time.Parse(layout, text); err == nil {
			*t = Time{parsed}
			return nil
		}
	}
	*t = Time{}
	return nil
}

func (t Time) MarshalJSON() ([]byte, error) {
	if t.IsZero() {
		return []byte("null"), nil
	}
	return json.Marshal(t.Time.UTC().Format(time.RFC3339))
}
