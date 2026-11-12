package state

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"
)

var StageKeys = []string{"requirements", "design", "plan", "implement", "review", "coderabbit", "qa", "qa_fixes", "test_gaps", "walkthrough", "handoff"}

type State struct {
	ID             string                  `json:"id"`
	Title          string                  `json:"title"`
	Repo           string                  `json:"repo"`
	SessionID      string                  `json:"session_id"`
	NeedsDesign    bool                    `json:"needs_design"`
	Pipeline       string                  `json:"pipeline"`
	PipelineStages []string                `json:"pipeline_stages"`
	Agents         map[string]AgentSetting `json:"agents"`
	Supervisor     AgentSetting            `json:"supervisor,omitzero"`
	Worktree       string                  `json:"worktree"`
	Branch         string                  `json:"branch"`
	Stage          string                  `json:"stage"`
	WaitingOn      string                  `json:"waiting_on"`
	WaitingReason  string                  `json:"waiting_reason"`
	UpdatedAt      Time                    `json:"updated_at"`
	Limits         Limits                  `json:"limits"`
	Stages         map[string]Stage        `json:"stages"`
	Attention      []Attention             `json:"attention"`
	PRs            []PR                    `json:"prs"`
	History        []HistoryEntry          `json:"history"`
}

type Limits struct {
	CodeRabbitRounds int `json:"coderabbit_rounds"`
	QARounds         int `json:"qa_rounds"`
	StageRetries     int `json:"stage_retries"`
}

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
	QAFixes bool   `json:"qa_fixes"`
}

type HistoryEntry struct {
	At     Time   `json:"at"`
	Stage  string `json:"stage"`
	Event  string `json:"event"`
	Detail string `json:"detail"`
}

type Attention struct {
	ID          string      `json:"id"`
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

func (a Attention) Stage() string {
	namesItsStage := strings.HasPrefix(a.ID, "gate-") || strings.HasPrefix(a.ID, "cap-") || strings.HasPrefix(a.ID, "fail-")
	i := strings.LastIndex(a.ID, "-")
	if !namesItsStage || i < 0 {
		return a.StageKey
	}
	return a.ID[i+1:]
}

func (s *State) Moot(a Attention) bool {
	if s.Done() {
		return true
	}
	stage := a.Stage()
	if stage == "" {
		return false
	}
	status := s.Stages[stage].Status
	finished := status == "done" || status == "skipped"
	movedOn := slices.Index(StageKeys, s.Stage) > slices.Index(StageKeys, stage)
	return finished && movedOn
}

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

func ReadState(path string) (*State, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return ParseState(data)
}

func ParseState(data []byte) (*State, error) {
	var s struct {
		State
		LegacyTicket string `json:"ticket"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(data), &s); err != nil {
		return nil, err
	}
	if s.ID == "" {
		s.ID = s.LegacyTicket
	}
	return &s.State, nil
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

func ResolveAttention(path, id, waitingOn string, now time.Time) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(data, &doc); err != nil {
		return err
	}
	var items []map[string]json.RawMessage
	if err := json.Unmarshal(doc["attention"], &items); err != nil {
		return fmt.Errorf("%s has no attention list: %w", path, err)
	}
	stamp, _ := json.Marshal(now.UTC().Format(time.RFC3339))
	found := false
	for _, item := range items {
		var itemID string
		json.Unmarshal(item["id"], &itemID)
		if itemID != id {
			continue
		}
		found = true
		if string(item["resolved_at"]) == "" || string(item["resolved_at"]) == "null" {
			item["resolved_at"] = stamp
		}
	}
	if !found {
		return fmt.Errorf("no attention item %q in %s", id, path)
	}
	doc["attention"], _ = json.Marshal(items)
	doc["updated_at"] = stamp
	switch waitingOn {
	case "":
	case "none":
		doc["waiting_on"], doc["waiting_reason"] = json.RawMessage("null"), json.RawMessage("null")
	default:
		doc["waiting_on"], _ = json.Marshal(waitingOn)
	}
	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(doc); err != nil {
		return err
	}
	return WriteFileAtomic(path, out.Bytes())
}
