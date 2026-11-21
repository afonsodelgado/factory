package workflow

import (
	"bytes"
	"encoding/json"
	"fmt"

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
	NewRequest          bool                   `json:"new_request,omitempty"`
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
	Data          ResultData          `json:"data"`

	inputSHAs, outputSHAs, evidenceSHAs []string
}

func (r *Result) UnmarshalJSON(data []byte) error {
	type plain Result
	type verification state.Verification
	var raw struct {
		*plain
		InputHeads  json.RawMessage `json:"input_heads"`
		OutputHeads json.RawMessage `json:"output_heads"`
		Evidence    struct {
			*verification
			Heads  json.RawMessage   `json:"heads"`
			Checks []json.RawMessage `json:"checks"`
		} `json:"evidence"`
	}
	raw.plain = (*plain)(r)
	raw.Evidence.verification = (*verification)(&r.Evidence)
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	var err error
	if r.InputHeads, r.inputSHAs, err = decodeHeads(raw.InputHeads); err != nil {
		return fmt.Errorf("input_heads: %w", err)
	}
	if r.OutputHeads, r.outputSHAs, err = decodeHeads(raw.OutputHeads); err != nil {
		return fmt.Errorf("output_heads: %w", err)
	}
	if r.Evidence.Heads, r.evidenceSHAs, err = decodeHeads(raw.Evidence.Heads); err != nil {
		return fmt.Errorf("evidence.heads: %w", err)
	}
	r.Evidence.Checks = nil
	for i, check := range raw.Evidence.Checks {
		var note string
		var evidence state.CheckEvidence
		switch trimmed := bytes.TrimSpace(check); {
		case bytes.HasPrefix(trimmed, []byte(`"`)) && json.Unmarshal(trimmed, &note) == nil:
			r.Evidence.Notes = append(r.Evidence.Notes, "check: "+note)
		case bytes.HasPrefix(trimmed, []byte("{")) && json.Unmarshal(trimmed, &evidence) == nil:
			r.Evidence.Checks = append(r.Evidence.Checks, evidence)
		default:
			return fmt.Errorf("evidence.checks[%d] must be an object {name, command, directory, head, exit_status, path} or a note string", i)
		}
	}
	return nil
}

func decodeHeads(raw json.RawMessage) ([]state.StackBranch, []string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil, nil
	}
	var branches []state.StackBranch
	err := json.Unmarshal(raw, &branches)
	if err == nil {
		return branches, nil, nil
	}
	var shas []string
	if json.Unmarshal(raw, &shas) == nil {
		return nil, shas, nil
	}
	return nil, nil, err
}

type ResultData struct {
	Failure string `json:"failure"`
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
