package workflow

import (
	"encoding/json"

	"github.com/afonsodelgado/factory/internal/state"
)

const (
	DesignLoop = "design_plan"
	ReviewLoop = "review_fix"
	QALoop     = "qa_fix"
	CILoop     = "ci_fix"
)

type Event struct {
	Checkpoint          *state.Checkpoint      `json:"checkpoint,omitempty"`
	ID                  string                 `json:"id"`
	Kind                string                 `json:"kind"`
	At                  state.Time             `json:"at"`
	Revision            uint64                 `json:"revision"`
	Incarnation         string                 `json:"incarnation"`
	HolderGeneration    uint64                 `json:"holder_generation"`
	ItemID              string                 `json:"item_id"`
	ArtifactRevision    uint64                 `json:"artifact_revision"`
	Answer              string                 `json:"answer"`
	Receipt             string                 `json:"receipt"`
	Approved            bool                   `json:"approved"`
	Increment           int                    `json:"increment"`
	EffectID            string                 `json:"effect_id"`
	WatchComments       bool                   `json:"watch_comments"`
	Result              *Result                `json:"result,omitempty"`
	Recipe              *state.Recipe          `json:"recipe,omitempty"`
	Stack               []state.StackBranch    `json:"stack,omitempty"`
	PRs                 []state.PR             `json:"prs,omitempty"`
	Observations        []state.PRVerification `json:"observations,omitempty"`
	CommentDispositions map[string]string      `json:"comment_dispositions,omitempty"`
	Comments            *state.Verification    `json:"comments,omitempty"`
	Detail              string                 `json:"detail,omitempty"`
	Note                *Note                  `json:"note,omitempty"`
}

type Note struct {
	Event  string `json:"event"`
	Detail string `json:"detail"`
}

var DiscoveryNotes = []string{"discovery started", "discovery started again", "test noted", "commands recorded"}

type Result struct {
	Questions     []Question          `json:"questions,omitempty"`
	NeedsDesign   *bool               `json:"needs_design,omitempty"`
	Artifacts     []state.Artifact    `json:"artifacts,omitempty"`
	SchemaVersion int                 `json:"schema_version"`
	RunID         string              `json:"run_id"`
	InputHeads    []state.StackBranch `json:"input_heads"`
	OutputHeads   []state.StackBranch `json:"output_heads"`
	Status        string              `json:"status"`
	Findings      []state.Finding     `json:"findings"`
	Evidence      state.Verification  `json:"evidence"`
	Outputs       []string            `json:"outputs"`
	Summary       string              `json:"summary,omitempty"`
}

type Question struct {
	Text        string   `json:"text"`
	Options     []string `json:"options,omitempty"`
	Recommended string   `json:"recommended,omitempty"`
}

func (q *Question) UnmarshalJSON(data []byte) error {
	if json.Unmarshal(data, &q.Text) == nil {
		return nil
	}
	type plain Question
	return json.Unmarshal(data, (*plain)(q))
}

type Proposal struct {
	ExpectedRevision uint64        `json:"expected_revision"`
	Incarnation      string        `json:"incarnation"`
	HolderGeneration uint64        `json:"holder_generation"`
	Snapshot         state.State   `json:"snapshot"`
	Effect           *state.Effect `json:"effect,omitempty"`
}
