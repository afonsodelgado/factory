package workflow

import "github.com/afonsodelgado/factory/internal/state"

const (
	DesignLoop = "design_plan"
	ReviewLoop = "review_fix"
	QALoop     = "qa_fix"
	CILoop     = "ci_fix"
)

type Event struct {
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
}

type Result struct {
	Artifacts     []state.Artifact    `json:"artifacts,omitempty"`
	SchemaVersion int                 `json:"schema_version"`
	RunID         string              `json:"run_id"`
	InputHeads    []state.StackBranch `json:"input_heads"`
	OutputHeads   []state.StackBranch `json:"output_heads"`
	Status        string              `json:"status"`
	Findings      []state.Finding     `json:"findings"`
	Evidence      state.Verification  `json:"evidence"`
	Outputs       []string            `json:"outputs"`
}

type Proposal struct {
	ExpectedRevision uint64        `json:"expected_revision"`
	Incarnation      string        `json:"incarnation"`
	HolderGeneration uint64        `json:"holder_generation"`
	Snapshot         state.State   `json:"snapshot"`
	Effect           *state.Effect `json:"effect,omitempty"`
}
