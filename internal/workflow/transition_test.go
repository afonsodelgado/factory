package workflow

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
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
	p, err := Next("testdata", s, e)
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
	switch s.Workflow.Runs[len(s.Workflow.Runs)-1].Stage {
	case "design":
		e.Result.Artifacts = []state.Artifact{{URL: "design/variant-a.html", Title: "Variant A"}}
	case "plan":
		e.Result.Artifacts = []state.Artifact{{URL: "plan.html", Title: "Plan"}}
	}
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
	review := state.Finding{ID: "review-bug", Severity: "major", Disposition: "open", Description: "missing regression"}
	s = complete(t, s, review)
	if s.Stage != "implement" || s.Loops.Used[QALoop] != 1 || s.Loops.Used[ReviewLoop] != 1 {
		t.Fatal(s)
	}
	review.Disposition = "closed"
	s = complete(t, s, review)
	s = complete(t, s)
	if s.Stage != "qa" || s.Loops.Active != QALoop {
		t.Fatalf("returned to %s in %s", s.Stage, s.Loops.Active)
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

func offeredHandoff(t *testing.T) state.State {
	t.Helper()
	s := workflowState()
	s.PipelineStages = []string{"requirements", "plan", "implement", "review", "pr", "handoff"}
	s.Stages["implement"] = state.Stage{Status: "done", Rounds: 1}
	s.Evidence.Local, s.Evidence.Review = passed(s), passed(s)
	s.PRs = []state.PR{{Number: 1, Branch: "feature", Base: "main", HeadSHA: "a", BaseSHA: "b", URL: "https://example.test/1"}}
	s.Evidence.PRs = []state.PRVerification{{Number: 1, Head: "a", Base: "b", Open: true, CI: "pass", Reason: "no_checks", Policy: "pass", At: state.Time{Time: time.Unix(100, 0)}}}
	if reasons := Readiness(s); len(reasons) != 0 {
		t.Fatal(reasons)
	}
	offerHandoff(&s, eventFor(s, "observe"))
	return s
}

func confirmed(t *testing.T, s state.State) state.State {
	t.Helper()
	e := eventFor(s, "handoff")
	e.At = state.Time{Time: time.Unix(300, 0)}
	e.ItemID, e.ArtifactRevision, e.Answer, e.Receipt, e.Approved = s.Attention[0].ID, s.Attention[0].Revision, "done", "receipt", true
	e.Stack, e.Observations = s.Stack, []state.PRVerification{s.Evidence.PRs[0]}
	e.Observations[0].At = state.Time{Time: time.Unix(200, 0)}
	return step(t, s, e)
}

func TestRecipeChangesAndPartialObservationsNeverStrandAFactory(t *testing.T) {
	recipe := func(s state.State, digest string, unresolved ...string) Event {
		e := eventFor(s, "commands")
		r := s.Recipe
		r.Digest, r.Unresolved = digest, unresolved
		e.Recipe = &r
		return e
	}
	for name, s := range map[string]state.State{"offered": offeredHandoff(t), "done": confirmed(t, offeredHandoff(t))} {
		t.Run(name, func(t *testing.T) {
			next := step(t, s, recipe(s, "new-recipe"))
			if next.Stage != "pr" || next.Workflow.ActiveRun == "" || next.Done() || len(next.OpenAttention()) != 0 {
				t.Fatal("a changed recipe left the factory with nothing dispatched", next.Stage, next.Workflow, next.OpenAttention())
			}
			asked := step(t, s, recipe(s, "new-recipe", "lint: no command found"))
			if asked.Workflow.ActiveRun != "" || len(asked.OpenAttention()) != 1 {
				t.Fatal("an unresolved recipe dispatched before the developer answered", asked.Workflow, asked.OpenAttention())
			}
			answered := step(t, asked, recipe(asked, "new-recipe"))
			if answered.Stage != "pr" || answered.Workflow.ActiveRun == "" {
				t.Fatal("answering the commands question left the factory stranded", answered.Stage, answered.Workflow)
			}
			partial := eventFor(s, "observe")
			partial.Stack, partial.Observations = s.Stack, s.Evidence.PRs
			if _, err := Next("testdata", s, partial); err == nil {
				t.Fatal("an observation without PRs wiped the published records")
			}
			partial.PRs, partial.Stack = s.PRs, nil
			if _, err := Next("testdata", s, partial); err == nil {
				t.Fatal("an observation without a stack wiped the published heads")
			}
		})
	}
	reviewing := workflowState()
	reviewing.Stages["implement"] = state.Stage{Status: "done", Rounds: 1}
	if err := dispatch(&reviewing, eventFor(reviewing, "result"), "review"); err != nil {
		t.Fatal(err)
	}
	if _, err := Next("testdata", reviewing, recipe(reviewing, "new-recipe")); err == nil {
		t.Fatal("a changed recipe during review stranded the review result")
	}
	failed := eventFor(reviewing, "result")
	failed.Stack = reviewing.Stack
	failed.Result = &Result{SchemaVersion: 2, RunID: reviewing.Workflow.ActiveRun, Status: "failed", InputHeads: reviewing.Stack, OutputHeads: reviewing.Stack, Data: ResultData{Failure: "setup"}}
	retrying := step(t, reviewing, failed)
	rerun := step(t, retrying, recipe(retrying, "new-recipe"))
	if rerun.Stage != "implement" || rerun.Workflow.ActiveRun == "" || len(rerun.OpenAttention()) != 0 {
		t.Fatal("rediscovered commands after a review failure did not re-run implementation for fresh evidence", rerun.Stage, rerun.Workflow, rerun.OpenAttention())
	}
	if reviewed := complete(t, rerun); reviewed.Stage != "review" || reviewed.Evidence.Local.RecipeDigest != "new-recipe" {
		t.Fatal("the review after rediscovered commands has no local checks on the new recipe to pass with", reviewed.Stage, reviewed.Evidence.Local.RecipeDigest)
	}
	layered := offeredHandoff(t)
	layered.Stack = append(layered.Stack, state.StackBranch{Branch: "feature-2", Parent: "feature", Head: "c", Base: "a"})
	layered.PRs = append(layered.PRs, state.PR{Number: 2, Branch: "feature-2", Base: "feature", HeadSHA: "c", BaseSHA: "a", URL: "https://example.test/2"})
	someOf := eventFor(layered, "observe")
	someOf.Stack, someOf.PRs, someOf.Observations = layered.Stack, layered.PRs[:1], layered.Evidence.PRs
	if _, err := Next("testdata", layered, someOf); err == nil {
		t.Fatal("an observation with only some published PRs dropped the others")
	}
	removed := eventFor(layered, "observe")
	removed.Stack, removed.PRs, removed.Observations = layered.Stack[:1], layered.PRs[:1], layered.Evidence.PRs
	if _, err := Next("testdata", layered, removed); err != nil {
		t.Fatal("a factory whose top layer was removed could no longer be observed:", err)
	}
	implementing := workflowState()
	if err := dispatch(&implementing, eventFor(implementing, "result"), "implement"); err != nil {
		t.Fatal(err)
	}
	failed = eventFor(implementing, "result")
	failed.Stack = implementing.Stack
	failed.Result = &Result{SchemaVersion: 2, RunID: implementing.Workflow.ActiveRun, Status: "failed", InputHeads: implementing.Stack, OutputHeads: implementing.Stack, Data: ResultData{Failure: "setup"}}
	broken := step(t, implementing, failed)
	corrected := step(t, broken, recipe(broken, "new-recipe"))
	if run := corrected.Workflow.Runs[len(corrected.Workflow.Runs)-1]; run.Stage != "implement" || corrected.Loops.Used[RediscoveryLoop] != 1 || len(corrected.OpenAttention()) != 0 {
		t.Fatal("rediscovered commands after a setup failure did not re-run implementation under the retry cap", run, corrected.OpenAttention())
	}
	if retried := complete(t, corrected); retried.Stage != "review" || retried.Evidence.Local.RecipeDigest != "new-recipe" {
		t.Fatal("the retried implementation did not run on the corrected recipe", retried.Stage, retried.Evidence.Local.RecipeDigest)
	}
}

func TestADiscoveryNoteOnlyAddsOneActivityRow(t *testing.T) {
	s := workflowState()
	if err := dispatch(&s, eventFor(s, "start"), "requirements"); err != nil {
		t.Fatal(err)
	}
	e := eventFor(s, "discovery")
	e.Note = &Note{Event: "commands recorded", Detail: "build, lint, test"}
	next := step(t, s, e)
	if len(next.History) != len(s.History)+1 || next.History[len(next.History)-1].Event != "commands recorded" {
		t.Fatalf("a discovery note left history %+v; want exactly one commands recorded row", next.History)
	}
	next.History, next.Revision, next.ProcessedEvents, next.UpdatedAt = s.History, s.Revision, s.ProcessedEvents, s.UpdatedAt
	before, _ := json.Marshal(s)
	after, _ := json.Marshal(next)
	if string(before) != string(after) {
		t.Fatalf("a discovery note moved the workflow:\n%s\n%s", before, after)
	}
	e.Note = &Note{Event: "dispatch", Detail: "qa"}
	if _, err := Next("testdata", s, e); err == nil {
		t.Fatal("a discovery note named another event")
	}
}

func TestAQASignInChallengeAsksOnceThroughCommandsAndRerunsQAUnderTheRetryCap(t *testing.T) {
	s := workflowState()
	s.Stages["implement"] = state.Stage{Status: "done", Rounds: 1}
	if err := dispatch(&s, eventFor(s, "result"), "qa"); err != nil {
		t.Fatal(err)
	}
	failed := eventFor(s, "result")
	failed.Stack = s.Stack
	failed.Result = &Result{SchemaVersion: 2, RunID: s.Workflow.ActiveRun, Status: "failed", InputHeads: s.Stack, OutputHeads: s.Stack, Data: ResultData{Failure: "interactive_challenge"}}
	challenged := step(t, s, failed)
	if challenged.Workflow.ActiveRun != "" || !slices.ContainsFunc(challenged.OpenAttention(), func(a state.Attention) bool { return a.Kind == "failure" }) {
		t.Fatalf("QA's interactive_challenge was retried automatically (run %q); want a failure item at once, since a retry meets the same challenge", challenged.Workflow.ActiveRun)
	}
	unresolved := eventFor(challenged, "commands")
	r := challenged.Recipe
	r.Digest, r.Unresolved = "no-sign-in", []string{"qa.sign_in: billing emails a one-time code"}
	unresolved.Recipe, unresolved.Detail = &r, "qa.sign_in  stopped QA round 1 at sign-in"
	asking := step(t, challenged, unresolved)
	if open := asking.OpenAttention(); len(open) != 1 || open[0].Kind != "commands" || open[0].Body != unresolved.Detail {
		t.Fatalf("after QA's interactive_challenge the factory asks %+v; want only one commands item with discovery's report", open)
	}
	resolved := eventFor(asking, "commands")
	r.Digest, r.Unresolved = "sign-in-script", nil
	resolved.Recipe = &r
	rerun := step(t, asking, resolved)
	if rerun.Stage != "implement" || rerun.Workflow.ActiveRun == "" || rerun.Loops.Used[RediscoveryLoop] != 1 {
		t.Fatalf("resolving the commands item gave stage %s, run %+v; want implementation, review and QA run again on the new recipe, charged once", rerun.Stage, rerun.Workflow)
	}

	again := complete(t, complete(t, rerun))
	if again.Stage != "qa" {
		t.Fatalf("after the re-run the factory is at %s; want QA again", again.Stage)
	}
	failedAgain := eventFor(again, "result")
	failedAgain.Stack = again.Stack
	failedAgain.Result = &Result{SchemaVersion: 2, RunID: again.Workflow.ActiveRun, Status: "failed", InputHeads: again.Stack, OutputHeads: again.Stack, Data: ResultData{Failure: "interactive_challenge"}}
	spent := step(t, again, failedAgain)
	rediscovered := eventFor(spent, "commands")
	r.Digest = "another-sign-in-script"
	rediscovered.Recipe = &r
	waiting := step(t, spent, rediscovered)
	failure := slices.IndexFunc(waiting.Attention, func(a state.Attention) bool { return a.Kind == "failure" && a.Open() })
	if failure < 0 || waiting.Workflow.ActiveRun != "" || waiting.Loops.Used[RediscoveryLoop] != 1 {
		t.Fatalf("with the rediscovery budget spent, corrected commands left run %q, open %+v, rediscoveries %d; want them saved, nothing re-run by itself and the failure waiting for the developer", waiting.Workflow.ActiveRun, waiting.OpenAttention(), waiting.Loops.Used[RediscoveryLoop])
	}
	retry := eventFor(waiting, "retry")
	item := waiting.Attention[failure]
	retry.ItemID, retry.ArtifactRevision, retry.Answer, retry.Receipt, retry.Increment = item.ID, item.Revision, "retry with the new sign-in script", "conversation:2", 1
	retried := step(t, waiting, retry)
	if retried.Stage != "implement" || retried.Limits.StageRetries != 2 {
		t.Fatalf("the developer's retry with an increment gave stage %s and stage_retries %d; want work re-run from implementation on the corrected commands and the budget raised once", retried.Stage, retried.Limits.StageRetries)
	}
	if reviewed := complete(t, retried); reviewed.Stage != "review" || reviewed.Evidence.Local.RecipeDigest != "another-sign-in-script" {
		t.Fatalf("after the retry the factory is at %s with local checks on %q; want review on the corrected commands", reviewed.Stage, reviewed.Evidence.Local.RecipeDigest)
	}
	if qa := complete(t, complete(t, retried)); qa.Stage != "qa" {
		t.Fatalf("after review the factory is at %s; want QA on the corrected commands", qa.Stage)
	}
}

func TestAFirstStageFailureRetriesOnceByItselfAndTheNextTellsTheDeveloperWhy(t *testing.T) {
	s := workflowState()
	s.Stages["implement"] = state.Stage{Status: "done", Rounds: 1}
	if err := dispatch(&s, eventFor(s, "result"), "qa"); err != nil {
		t.Fatal(err)
	}
	failure := func(s state.State) Event {
		failed := eventFor(s, "result")
		failed.Stack = s.Stack
		failed.Result = &Result{SchemaVersion: 2, RunID: s.Workflow.ActiveRun, Status: "failed", InputHeads: s.Stack, OutputHeads: s.Stack,
			Summary:  "Drafting stays blocked because OpenAI rejects this machine's IP.",
			Evidence: state.Verification{Status: "fail", Notes: []string{"ANTHROPIC_API_KEY is invalid, so live missions cannot run."}},
			Outputs:  []string{".claude/factory/f/qa-report.md"}}
		return failed
	}
	retrying := step(t, s, failure(s))
	if run := retrying.Workflow.Runs[len(retrying.Workflow.Runs)-1]; run.Stage != "qa" || run.Retries != 1 || len(retrying.OpenAttention()) != 0 {
		t.Fatalf("a first QA failure gave run %+v and items %+v; want QA dispatched again with retries 1 and no developer item", run, retrying.OpenAttention())
	}
	failed := failure(retrying)
	waiting := step(t, retrying, failed)
	open := waiting.OpenAttention()
	if len(open) != 1 || open[0].Kind != "failure" {
		t.Fatalf("a failed QA result opened %+v; want one failure item", open)
	}
	for _, why := range []string{failed.Result.Summary, failed.Result.Evidence.Notes[0], failed.Result.Outputs[0]} {
		if !strings.Contains(open[0].Body, why) {
			t.Fatalf("the failure item reads %q; it must show %q so the developer can decide from the panel or phone", open[0].Body, why)
		}
	}
}

func TestStaleHandoffAndPartialChecksCannotFinish(t *testing.T) {
	s := offeredHandoff(t)
	red := eventFor(s, "observe")
	red.Stack, red.PRs = s.Stack, s.PRs
	red.Observations = []state.PRVerification{s.Evidence.PRs[0]}
	red.Observations[0].CI = "fail"
	if redone := step(t, s, red); redone.Stage != "pr" || redone.Workflow.ActiveRun == "" || len(redone.OpenAttention()) != 0 {
		t.Fatal("red CI on unchanged heads left the handoff offer standing", redone.Stage, redone.OpenAttention())
	}
	e := eventFor(s, "handoff")
	e.At = state.Time{Time: time.Unix(300, 0)}
	e.ItemID, e.ArtifactRevision, e.Answer, e.Receipt = s.Attention[0].ID, s.Attention[0].Revision, "done", "receipt"
	e.Approved = true
	e.Stack = []state.StackBranch{{Branch: "feature", Parent: "main", Head: "changed", Base: "b"}}
	e.Observations = s.Evidence.PRs
	if _, err := Next("testdata", s, e); err == nil {
		t.Fatal("stale head accepted")
	}
	e.Stack = s.Stack
	e.Observations[0].CI = "unknown"
	if _, err := Next("testdata", s, e); err == nil {
		t.Fatal("unknown CI accepted")
	}
	e.Observations[0].CI = "pass"
	e.Observations[0].At = state.Time{Time: time.Unix(50, 0)}
	if _, err := Next("testdata", s, e); err == nil {
		t.Fatal("evidence older than the handoff offer accepted")
	}
	e.Observations[0].At = state.Time{Time: time.Unix(200, 0)}
	s = step(t, s, e)
	if !s.Done() || s.Confirmation.Heads[0].Head != "a" {
		t.Fatal("confirmation missing")
	}
	green := eventFor(s, "observe")
	green.Stack, green.PRs, green.Observations = s.Stack, s.PRs, e.Observations
	if s = step(t, s, green); len(s.OpenAttention()) != 0 || !s.Done() {
		t.Fatal("a green observation of a done factory asked to confirm it again", s.OpenAttention())
	}
}

func TestStaleGateItemsCannotApproveAnotherStage(t *testing.T) {
	s := workflowState()
	if err := dispatch(&s, eventFor(s, "start"), "plan"); err != nil {
		t.Fatal(err)
	}
	s = complete(t, s, state.Finding{ID: "nav", Severity: "design", Disposition: "open", Description: "navigation hides the export"})
	back := s.Attention[len(s.Attention)-1]
	if !strings.Contains(back.Body, "nav: navigation hides the export") {
		t.Fatal("the design return item does not name its findings", back.Body)
	}
	e := eventFor(s, "design_return")
	e.ItemID, e.ArtifactRevision, e.Answer, e.Receipt = back.ID, back.Revision, "decline", "conversation:1"
	s = step(t, s, e)
	plan := s.Attention[len(s.Attention)-1]
	quick := s
	quick.PipelineStages = []string{"requirements", "plan", "implement", "review", "pr", "handoff"}
	e = eventFor(s, "design_comments")
	e.Receipt, e.Answer = "batch:1", "tighten the header"
	if _, err := Next("testdata", quick, e); err == nil {
		t.Fatal("design comments dispatched a designer in a pipeline without design")
	}
	s = step(t, s, e)
	s = complete(t, s)
	e = eventFor(s, "approval")
	e.ItemID, e.ArtifactRevision, e.Answer, e.Receipt, e.Approved = plan.ID, plan.Revision, "approved", "conversation:2", true
	if _, err := Next("testdata", s, e); err == nil {
		t.Fatal("the plan item approved while a revised design waits")
	}
	design := s.Attention[len(s.Attention)-1]
	e.ItemID, e.ArtifactRevision = design.ID, design.Revision
	if s = step(t, s, e); s.Stage != "plan" {
		t.Fatal("approving the design item did not recheck the plan", s.Stage)
	}
}

func TestPlanDesignGapsWithoutADesignStageWaitOnThePlanGate(t *testing.T) {
	s := workflowState()
	s.PipelineStages = []string{"requirements", "plan", "implement", "review", "pr", "handoff"}
	if err := dispatch(&s, eventFor(s, "start"), "plan"); err != nil {
		t.Fatal(err)
	}
	gap := state.Finding{ID: "nav", Severity: "design", Disposition: "open", Description: "no frame shows the export"}
	s = complete(t, s, gap)
	plan := s.Attention[len(s.Attention)-1]
	if plan.Kind != "gate" || !strings.Contains(plan.Body, "nav: no frame shows the export") {
		t.Fatal("a plan in a pipeline without design did not open its gate with the design gaps", plan.Kind, plan.Body)
	}
	e := eventFor(s, "changes")
	e.ItemID, e.ArtifactRevision, e.Answer, e.Receipt = plan.ID, plan.Revision, "review/batch-1.md", "batch:1"
	if s = step(t, s, e); s.Stage != "plan" {
		t.Fatal("plan comments did not return to the architect", s.Stage)
	}
	s = complete(t, s, gap)
	plan = s.Attention[len(s.Attention)-1]
	e = eventFor(s, "approval")
	e.ItemID, e.ArtifactRevision, e.Answer, e.Receipt, e.Approved = plan.ID, plan.Revision, "approved", "conversation:1", true
	if s = step(t, s, e); s.Stage != "implement" || slices.ContainsFunc(s.Findings, Gating) {
		t.Fatal("approving the plan left its design gaps blocking the handoff", s.Stage, s.Findings)
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
	failed := eventFor(s, "result")
	failed.Stack = s.Stack
	failed.Result = &Result{SchemaVersion: 2, RunID: s.Workflow.ActiveRun, Status: "failed", InputHeads: s.Stack, OutputHeads: s.Stack}
	broken := step(t, s, failed)
	e = eventFor(broken, "approval")
	e.ItemID, e.ArtifactRevision, e.Answer, e.Receipt, e.Approved = old.ID, old.Revision, "approved", "old-answer", true
	if _, err := Next("testdata", broken, e); err == nil {
		t.Fatal("the stale design gate approved over a failed revision")
	}
	s = complete(t, s)
	e = eventFor(s, "approval")
	e.ItemID, e.ArtifactRevision, e.Answer, e.Receipt, e.Approved = old.ID, old.Revision, "approved", "old-answer", true
	if _, err := Next("testdata", s, e); err == nil {
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
	offered := workflowState()
	offerHandoff(&offered, eventFor(offered, "observe"))
	e = eventFor(offered, "observe")
	e.Stack = []state.StackBranch{{Branch: "feature", Parent: "main", Head: "new", Base: "b"}}
	offered = step(t, offered, e)
	if offered.Stage != "pr" || offered.Workflow.ActiveRun == "" || len(offered.OpenAttention()) != 0 {
		t.Fatal("changed heads under an unconfirmed handoff stranded the factory", offered.Stage, offered.OpenAttention())
	}
	encoded, err := json.Marshal(s)
	var persisted map[string]json.RawMessage
	if err != nil || json.Unmarshal(encoded, &persisted) != nil || string(persisted["confirmation"]) != "null" {
		t.Fatal("commit proposal must explicitly clear the saved confirmation", string(encoded), err)
	}
}

func TestInterviewAnswersDoNotConsumeTransportRetriesOrSkipSelectedDesign(t *testing.T) {
	s := workflowState()
	s = step(t, s, eventFor(s, "start"))
	batches := []string{
		`[{"text":"Which behaviour?","options":["keep","replace"],"recommended":"keep"},"Anything else?"]`,
		`["Which behaviour?"]`,
		`["Which behaviour?","Anything else?"]`,
	}
	for _, batch := range batches {
		e := eventFor(s, "result")
		e.Stack = s.Stack
		e.Result = &Result{SchemaVersion: 2, RunID: s.Workflow.ActiveRun, Status: "blocked", InputHeads: s.Stack, OutputHeads: s.Stack}
		if err := json.Unmarshal([]byte(batch), &e.Result.Questions); err != nil {
			t.Fatal(err)
		}
		s = step(t, s, e)
		var a state.Attention
		if encoded, err := json.Marshal(s.Attention[len(s.Attention)-1]); err != nil || json.Unmarshal(encoded, &a) != nil {
			t.Fatal(string(encoded), err)
		}
		for i, q := range e.Result.Questions {
			if a.Questions[i] != q.Text || strings.Join(a.Options[i], "|") != strings.Join(q.Options, "|") || a.Recommended[i] != q.Recommended {
				t.Fatalf("question %d of %s became %q %q %q", i, batch, a.Questions, a.Options, a.Recommended)
			}
		}
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
	design := false
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
	e = eventFor(s, "result")
	e.Stack = s.Stack
	e.Result = &Result{SchemaVersion: 2, RunID: s.Workflow.ActiveRun, Status: "done", InputHeads: s.Stack, OutputHeads: s.Stack, Evidence: passed(s)}
	for _, missing := range [][]state.Artifact{nil, {{URL: "design/missing.html"}}, {{URL: "file:///tmp/design/variant-a.html"}}} {
		e.Result.Artifacts = missing
		if _, err := Next("testdata", s, e); err == nil {
			t.Fatalf("design gate opened on artifacts %v, so the developer has no page to review", missing)
		}
	}
	s = complete(t, s)
	if got := s.Stages["design"].Artifacts; len(got) != 1 || got[0].URL != "design/variant-a.html" {
		t.Fatal("the design page is missing from stages.design.artifacts", got)
	}
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
	if _, err := Next("testdata", s, e); err == nil {
		t.Fatal("old design approval accepted")
	}
}

func TestExhaustedRetriesResumeOnlyWithTheDevelopersIncrement(t *testing.T) {
	s := workflowState()
	s.Limits.StageRetries = 0
	if err := dispatch(&s, eventFor(s, "start"), "qa"); err != nil {
		t.Fatal(err)
	}
	s.Workflow.Pending = nil
	e := eventFor(s, "lost_run")
	e.Receipt = "pane-gone"
	s = step(t, s, e)
	a := s.Attention[len(s.Attention)-1]
	if a.Kind != "failure" || s.Workflow.ActiveRun == "" {
		t.Fatal("exhausted lost run did not ask the developer", s.Attention)
	}
	e = eventFor(s, "retry")
	e.ItemID, e.ArtifactRevision, e.Answer, e.Receipt = a.ID, a.Revision, "retry", "conversation:1"
	if _, err := Next("testdata", s, e); err == nil {
		t.Fatal("retry past the allowance without an increment accepted")
	}
	e.Increment = 1
	s = step(t, s, e)
	if s.Limits.StageRetries != 1 || s.Stage != "qa" || s.Workflow.Pending == nil || s.Attention[len(s.Attention)-1].Open() {
		t.Fatal("approved retry did not redispatch the stage", s.Limits, s.Workflow)
	}
}

func TestCheckpointRefusesUnpublishedWorkBeforeAnyExternalEffect(t *testing.T) {
	s := workflowState()
	e := eventFor(s, "checkpoint")
	e.Receipt = "node-operation"
	e.Checkpoint = &state.Checkpoint{Operation: "move", Status: state.CheckpointReady}
	if _, err := Next("testdata", s, e); err == nil {
		t.Fatal("unpublished stack checkpointed")
	}
	e.Checkpoint.Status, e.Checkpoint.Reason = state.CheckpointRefused, "unpublished branch"
	s = step(t, s, e)
	if s.Checkpoint.Status != state.CheckpointRefused || s.Workflow.Pending != nil {
		t.Fatal(s)
	}
}

func TestDiscoverySettlesOnlyFailuresOfStagesThatRunCommandsAndKeepsTheRetryOnAnOpenItem(t *testing.T) {
	failing := func(stage string, implemented bool) state.State {
		s := workflowState()
		if implemented {
			s.Stages["implement"] = state.Stage{Status: "done", Rounds: 1}
		}
		if err := dispatch(&s, eventFor(s, "result"), stage); err != nil {
			t.Fatal(err)
		}
		failed := eventFor(s, "result")
		failed.Stack = s.Stack
		failed.Result = &Result{SchemaVersion: 2, RunID: s.Workflow.ActiveRun, Status: "failed", InputHeads: s.Stack, OutputHeads: s.Stack, Data: ResultData{Failure: "setup"}}
		return step(t, s, failed)
	}
	unresolved := func(s state.State, digest string) Event {
		e := eventFor(s, "commands")
		r := s.Recipe
		r.Digest, r.Unresolved = digest, []string{"test: DATABASE_URL is not set"}
		e.Recipe = &r
		return e
	}
	analyst := failing("requirements", false)
	if asked := step(t, analyst, unresolved(analyst, "partial")); !slices.ContainsFunc(asked.OpenAttention(), func(a state.Attention) bool { return a.Kind == "failure" }) {
		t.Fatalf("discovery swallowed a requirements failure into the commands wait: %+v", asked.OpenAttention())
	}

	qa := failing("qa", true)
	qa.Evidence.Local = passed(qa)
	qa.Attention = append(qa.Attention, state.Attention{ID: "earlier-commands", Kind: "commands", Title: "Resolve repository commands", StageKey: "requirements", CreatedAt: qa.UpdatedAt})
	asked := step(t, qa, unresolved(qa, qa.Recipe.Digest))
	resolved := eventFor(asked, "commands")
	r := asked.Recipe
	r.Unresolved = nil
	resolved.Recipe = &r
	if rerun := step(t, asked, resolved); rerun.Workflow.ActiveRun == "" || rerun.Stage != "qa" {
		t.Fatalf("after the developer set up what QA needed (same recipe), resolving the already open commands item left stage %s and run %q; want QA run again", rerun.Stage, rerun.Workflow.ActiveRun)
	}
}

func TestResultShapeSlipsThatLoseNothingAreAcceptedAndRealGapsStillRejected(t *testing.T) {
	submit := func(s state.State, stage, raw string) (state.State, error) {
		t.Helper()
		if err := dispatch(&s, eventFor(s, "start"), stage); err != nil {
			t.Fatal(err)
		}
		e := eventFor(s, "result")
		e.Stack = s.Stack
		if err := json.Unmarshal([]byte(strings.ReplaceAll(raw, "RUN", s.Workflow.ActiveRun)), &e.Result); err != nil {
			t.Fatal(err)
		}
		p, err := Next("testdata", s, e)
		return p.Snapshot, err
	}
	stack := `[{"branch":"feature","parent":"main","head":"a","base":"b"}]`

	qa, err := submit(workflowState(), "qa", `{"schema_version":2,"run_id":"RUN","status":"done","input_heads":`+stack+`,"output_heads":`+stack+`,"findings":[],"evidence":{"status":"pass","heads":`+stack+`,"recipe_digest":"recipe","at":"2026-11-20T19:00:00Z","checks":["flows: create and exclude pass"]}}`)
	if err != nil || qa.Stage != "pr" || !slices.Contains(qa.Evidence.QA.Notes, "check: flows: create and exclude pass") {
		t.Fatalf("QA's string checks were refused (%v) or lost; want them kept as notes and QA passed", err)
	}

	before := workflowState()
	before.Stack, before.StartCommit, before.Workflow.CommentsAsked = nil, "ca0664f1becdebb7dd034aed08bf53c139617c7d", true
	analyst := `{"schema_version":2,"run_id":"RUN","status":"done","input_heads":["SHA"],"output_heads":["SHA"],"findings":[],"evidence":{"status":"pass","heads":["SHA"]}}`
	if s, err := submit(before, "requirements", strings.ReplaceAll(analyst, "SHA", "ca0664f")); err != nil || s.Workflow.Gate != "requirements" {
		t.Fatalf("the analyst's start-commit heads before any stack were refused: %v", err)
	}
	if _, err := submit(before, "requirements", strings.ReplaceAll(analyst, "SHA", "5ed087e")); err == nil || !strings.Contains(err.Error(), "must list stack branches") {
		t.Fatalf("a commit other than the start commit was accepted as heads: %v", err)
	}

	engineer := `{"schema_version":2,"run_id":"RUN","status":"done","input_heads":` + stack + `,"output_heads":` + stack + `,"findings":[],"evidence":{"status":"pass","heads":` + stack + `,"recipe_digest":"recipe","at":"2026-11-20T19:00:00Z","checks":["build","lint","test"]}}`
	if _, err := submit(workflowState(), "implement", engineer); err == nil || err.Error() != "implementation needs current successful build, lint and test evidence" {
		t.Fatalf("an implementation with check names instead of evidence got %v; want its exact rejection", err)
	}
}

func TestWorkTheDeveloperAddsAfterHandoffGetsItsOwnCorrectionBudget(t *testing.T) {
	s := offeredHandoff(t)
	s.PipelineStages = state.StageKeys
	s.Loops.Used[QALoop] = s.Limits.QAFixRounds
	keepGoing := eventFor(s, "continue")
	keepGoing.ItemID, keepGoing.ArtifactRevision, keepGoing.Answer, keepGoing.Receipt = s.Attention[0].ID, s.Attention[0].Revision, "also run a Figma pass", "conversation:7"
	s = complete(t, complete(t, step(t, s, keepGoing)))
	if s.Stage != "qa" {
		t.Fatalf("the follow-up reached %s; want QA", s.Stage)
	}
	s = complete(t, s, state.Finding{ID: "fid-rail", Stage: "qa", Severity: "major", Disposition: "open", Description: "rail is 64 px wide, frame is 240 px"})
	if s.Stage != "implement" || slices.ContainsFunc(s.OpenAttention(), func(a state.Attention) bool { return a.Kind == "cap" }) {
		t.Fatalf("QA findings on the developer's follow-up gave stage %s and items %+v; want the engineer, not the earlier work's tripped qa_fix cap", s.Stage, s.OpenAttention())
	}
	if !slices.ContainsFunc(s.History, func(h state.HistoryEntry) bool {
		return h.Event == "request" && strings.HasPrefix(h.Detail, "request 1:") && strings.Contains(h.Detail, "qa_fix 3/3")
	}) {
		t.Fatalf("history %+v does not show which budget the follow-up started from", s.History)
	}
}
