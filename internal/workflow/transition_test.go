package workflow

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/afonsodelgado/factory/internal/state"
)

func workflowState() state.State {
	return state.State{SchemaVersion: 2, Incarnation: "factory-1", PipelineStages: state.StageKeys, Limits: state.DefaultLimits(), Stages: map[string]state.Stage{}, Loops: state.Loops{Used: map[string]int{}}, Recipe: state.Recipe{Digest: "recipe", Commands: map[string]state.Command{"build": {Command: "build", Directory: "repo", Source: "Makefile"}, "lint": {Command: "lint", Directory: "repo", Source: "Makefile"}, "test": {Command: "test", Directory: "repo", Source: "Makefile"}}}, Stack: []state.StackBranch{{Branch: "feature", Parent: "main", Head: "a", Base: "b"}}}
}

func eventFor(s state.State, kind string) Event {
	return Event{ID: fmt.Sprint("event-", s.Revision), Kind: kind, Revision: s.Revision, Incarnation: s.Incarnation, At: state.Time{Time: time.Unix(int64(s.Revision)+100, 0)}}
}

func passed(s state.State) state.Verification {
	v := state.Verification{Status: "pass", Heads: s.Stack, RecipeDigest: s.Recipe.Digest, At: state.Time{Time: time.Unix(100, 0)}}
	for name, c := range s.Recipe.Commands {
		v.Checks = append(v.Checks, state.CheckEvidence{Name: name, Command: c.Command, Directory: c.Directory, Head: s.Stack[0].Head, Path: name + ".log"})
	}
	return v
}

func step(t *testing.T, s state.State, e Event) state.State {
	t.Helper()
	p, err := Next(s, e)
	if err != nil {
		t.Fatal(err)
	}
	return p.Snapshot
}

func complete(t *testing.T, s state.State, findings ...state.Finding) state.State {
	t.Helper()
	e := eventFor(s, "result")
	e.Stack = s.Stack
	e.Result = &Result{SchemaVersion: 2, RunID: s.Workflow.ActiveRun, Status: "done", InputHeads: s.Stack, OutputHeads: s.Stack, Findings: findings, Evidence: passed(s)}
	return step(t, s, e)
}

func TestNestedQACorrectionsReturnThroughReviewWithoutChargingQATwice(t *testing.T) {
	s := workflowState()
	if err := dispatch(&s, eventFor(s, "start"), "qa"); err != nil {
		t.Fatal(err)
	}
	qa := state.Finding{ID: "qa-bug", Stage: "qa", Severity: "major", Disposition: "open", Description: "broken flow"}
	s = complete(t, s, qa)
	if s.Stage != "implement" || s.Loops.Used[QALoop] != 1 {
		t.Fatal(s)
	}
	s = complete(t, s)
	review := state.Finding{ID: "review-bug", Stage: "review", Severity: "major", Disposition: "open", Description: "missing regression"}
	s = complete(t, s, review)
	if s.Stage != "implement" || s.Loops.Used[QALoop] != 1 || s.Loops.Used[ReviewLoop] != 1 {
		t.Fatal(s)
	}
	review.Disposition = "closed"
	s = complete(t, s, review)
	s = complete(t, s)
	if s.Stage != "qa" {
		t.Fatalf("returned to %s", s.Stage)
	}
	s = complete(t, s)
	if s.Stage != "implement" {
		t.Fatal("QA passed an old open finding")
	}
	qa.Disposition = "closed"
	s = complete(t, s, qa)
	s = complete(t, s)
	s = complete(t, s)
	if s.Stage != "pr" {
		t.Fatal(s.Stage)
	}
}

func TestCapRefusalAndDuplicateApprovalNeverDispatchOrChargeTwice(t *testing.T) {
	s := workflowState()
	s.Limits.ReviewFixRounds = 0
	if err := correct(&s, eventFor(s, "result"), state.Correction{Loop: ReviewLoop, Stage: "implement", ReturnTo: "qa"}); err != nil {
		t.Fatal(err)
	}
	a := s.Attention[0]
	e := eventFor(s, "cap")
	e.ItemID, e.ArtifactRevision, e.Answer, e.Receipt = a.ID, a.Revision, "no", "conversation:1"
	s = step(t, s, e)
	if s.Workflow.ActiveRun != "" || !s.Attention[0].Open() {
		t.Fatal("refusal advanced workflow")
	}
	e = eventFor(s, "cap")
	e.ItemID, e.ArtifactRevision, e.Answer, e.Receipt = a.ID, a.Revision, "two more", "conversation:2"
	e.Approved, e.Increment = true, 2
	s = step(t, s, e)
	before, _ := json.Marshal(s)
	s = step(t, s, e)
	after, _ := json.Marshal(s)
	if string(before) != string(after) || s.Limits.ReviewFixRounds != 2 || s.Loops.Used[ReviewLoop] != 1 {
		t.Fatal("duplicate allowance or dispatch")
	}
}

func TestStaleHandoffAndPartialChecksCannotFinish(t *testing.T) {
	s := workflowState()
	s.PipelineStages = []string{"requirements", "plan", "implement", "review", "pr", "handoff"}
	s.Evidence.Local, s.Evidence.Review = passed(s), passed(s)
	s.PRs = []state.PR{{Number: 1, Branch: "feature", Base: "main", HeadSHA: "a", BaseSHA: "b", URL: "https://example.test/1"}}
	s.Evidence.PRs = []state.PRVerification{{Number: 1, Head: "a", Base: "b", Open: true, CI: "pass", Reason: "no_checks", Policy: "pass", At: state.Time{Time: time.Unix(100, 0)}}}
	if reasons := Readiness(s); len(reasons) != 0 {
		t.Fatal(reasons)
	}
	offerHandoff(&s, eventFor(s, "observe"))
	e := eventFor(s, "handoff")
	e.ItemID, e.ArtifactRevision, e.Answer, e.Receipt = s.Attention[0].ID, s.Attention[0].Revision, "done", "receipt"
	e.Approved = true
	e.Stack = []state.StackBranch{{Branch: "feature", Parent: "main", Head: "changed", Base: "b"}}
	e.Observations = s.Evidence.PRs
	if _, err := Next(s, e); err == nil {
		t.Fatal("stale head accepted")
	}
	e.Stack = s.Stack
	e.Observations[0].CI = "unknown"
	if _, err := Next(s, e); err == nil {
		t.Fatal("unknown CI accepted")
	}
	e.Observations[0].CI = "pass"
	s = step(t, s, e)
	if !s.Done() || s.Confirmation.Heads[0].Head != "a" {
		t.Fatal("confirmation missing")
	}
}

func TestRevisedDesignRejectsThePreviousApprovalAndChangedDoneHeadsRestartVerification(t *testing.T) {
	s := workflowState()
	s.Stage = "design"
	gate(&s, eventFor(s, "result"), "design")
	old := s.Attention[0]
	e := eventFor(s, "design_comments")
	e.Receipt, e.Answer = "batch:1", "revise spacing"
	s = step(t, s, e)
	s = complete(t, s)
	e = eventFor(s, "approval")
	e.ItemID, e.ArtifactRevision, e.Answer, e.Receipt, e.Approved = old.ID, old.Revision, "approved", "old-answer", true
	if _, err := Next(s, e); err == nil {
		t.Fatal("old design approval accepted")
	}
	s = workflowState()
	s.Stages["handoff"] = state.Stage{Status: "done"}
	s.Confirmation = &state.Confirmation{Heads: s.Stack}
	e = eventFor(s, "observe")
	e.Stack = []state.StackBranch{{Branch: "feature", Parent: "main", Head: "new", Base: "b"}}
	s = step(t, s, e)
	if s.Done() || s.Confirmation != nil || s.Stage != "pr" {
		t.Fatal("changed completed factory did not restart verification")
	}
	encoded, err := json.Marshal(s)
	var persisted map[string]json.RawMessage
	if err != nil || json.Unmarshal(encoded, &persisted) != nil || string(persisted["confirmation"]) != "null" {
		t.Fatal("commit proposal must explicitly clear the saved confirmation", string(encoded), err)
	}
}

func TestInterviewAnswersDoNotConsumeTransportRetriesAndDesignRemainsConditional(t *testing.T) {
	s := workflowState()
	s = step(t, s, eventFor(s, "start"))
	for range 3 {
		e := eventFor(s, "result")
		e.Stack = s.Stack
		e.Result = &Result{SchemaVersion: 2, RunID: s.Workflow.ActiveRun, Status: "blocked", InputHeads: s.Stack, OutputHeads: s.Stack, Questions: []string{"Which behaviour?"}}
		s = step(t, s, e)
		a := s.Attention[len(s.Attention)-1]
		e = eventFor(s, "answers")
		e.ItemID, e.ArtifactRevision, e.Answer, e.Receipt = a.ID, a.Revision, "chosen behaviour", "conversation-answer"
		s = step(t, s, e)
		if s.Workflow.Runs[len(s.Workflow.Runs)-1].Retries != 0 || s.Stage != "requirements" {
			t.Fatal(s.Workflow)
		}
	}
	e := eventFor(s, "comments_preference")
	e.Answer, e.Receipt = "off", "conversation-preference"
	s = step(t, s, e)
	design := true
	e = eventFor(s, "result")
	e.Stack = s.Stack
	e.Result = &Result{SchemaVersion: 2, RunID: s.Workflow.ActiveRun, Status: "done", InputHeads: s.Stack, OutputHeads: s.Stack, NeedsDesign: &design}
	s = step(t, s, e)
	a := s.Attention[len(s.Attention)-1]
	e = eventFor(s, "approval")
	e.ItemID, e.ArtifactRevision, e.Answer, e.Receipt, e.Approved = a.ID, a.Revision, "approved", "approval-receipt", true
	s = step(t, s, e)
	if s.Stage != "design" {
		t.Fatal(s.Stage)
	}
	s = complete(t, s)
	a = s.Attention[len(s.Attention)-1]
	e = eventFor(s, "changes")
	e.ItemID, e.ArtifactRevision, e.Answer, e.Receipt = a.ID, a.Revision, "revise navigation", "changes-receipt"
	s = step(t, s, e)
	if s.Stage != "design" || s.Loops.Used[DesignLoop] != 1 {
		t.Fatal(s.Stage, s.Loops)
	}
	s = complete(t, s)
	e = eventFor(s, "approval")
	e.ItemID, e.ArtifactRevision, e.Answer, e.Receipt, e.Approved = a.ID, a.Revision, "old approval", "stale-receipt", true
	if _, err := Next(s, e); err == nil {
		t.Fatal("old design approval accepted")
	}
}

func TestCheckpointRefusesUnpublishedWorkBeforeAnyExternalEffect(t *testing.T) {
	s := workflowState()
	e := eventFor(s, "checkpoint")
	e.Receipt = "node-operation"
	e.Checkpoint = &state.Checkpoint{Operation: "move", Status: state.CheckpointReady}
	if _, err := Next(s, e); err == nil {
		t.Fatal("unpublished stack checkpointed")
	}
	e.Checkpoint.Status, e.Checkpoint.Reason = state.CheckpointRefused, "unpublished branch"
	s = step(t, s, e)
	if s.Checkpoint.Status != state.CheckpointRefused || s.Workflow.Pending != nil {
		t.Fatal(s)
	}
}
