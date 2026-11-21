package workflow

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/afonsodelgado/factory/internal/state"
)

func Next(dir string, snapshot state.State, event Event) (Proposal, error) {
	if err := state.ValidateState(snapshot); err != nil {
		return Proposal{}, err
	}
	if !regexp.MustCompile(`^[A-Za-z0-9_.-]+$`).MatchString(event.ID) || event.Incarnation == "" || event.Incarnation != snapshot.Incarnation || event.HolderGeneration != snapshot.HolderGeneration {
		return Proposal{}, errors.New("event identity does not match the factory holder")
	}
	proposal := Proposal{ExpectedRevision: snapshot.Revision, Incarnation: snapshot.Incarnation, HolderGeneration: snapshot.HolderGeneration, Snapshot: snapshot}
	if slices.Contains(snapshot.ProcessedEvents, event.ID) {
		return proposal, nil
	}
	if event.Revision != snapshot.Revision || event.At.IsZero() {
		return Proposal{}, errors.New("stale revision or missing event time")
	}
	data, err := json.Marshal(snapshot)
	if err != nil {
		return Proposal{}, err
	}
	var s state.State
	if err := json.Unmarshal(data, &s); err != nil {
		return Proposal{}, err
	}
	if s.Stages == nil {
		s.Stages = map[string]state.Stage{}
	}
	if s.Loops.Used == nil {
		s.Loops.Used = map[string]int{}
	}
	if err := apply(&s, event, dir); err != nil {
		return Proposal{}, err
	}
	s.Revision++
	s.ProcessedEvents = append(s.ProcessedEvents, event.ID)
	s.UpdatedAt = event.At
	proposal.Snapshot, proposal.Effect = s, s.Workflow.Pending
	return proposal, nil
}

func apply(s *state.State, e Event, dir string) error {
	switch e.Kind {
	case "checkpoint":
		if e.Checkpoint == nil || e.Checkpoint.Operation == "" || e.Receipt == "" || !slices.Contains([]string{state.CheckpointReady, state.CheckpointRefused}, e.Checkpoint.Status) {
			return errors.New("checkpoint needs a reconciled operation and outcome")
		}
		if e.Checkpoint.Status == state.CheckpointReady {
			if s.Workflow.ActiveRun != "" || s.Workflow.Pending != nil {
				return errors.New("checkpoint requires completed dispatch")
			}
			for _, branch := range s.Stack {
				if !slices.ContainsFunc(s.PRs, func(p state.PR) bool { return p.Branch == branch.Branch && p.Number > 0 }) {
					return errors.New("checkpoint cannot publish local branches")
				}
			}
		}
		checkpoint := *e.Checkpoint
		checkpoint.At = e.At
		s.Checkpoint = &checkpoint
	case "start":
		if len(s.Workflow.Runs) != 0 {
			return errors.New("workflow already started")
		}
		return dispatch(s, e, "requirements")
	case "effect_received":
		if s.Workflow.Pending == nil || s.Workflow.Pending.ID != e.EffectID || e.Receipt == "" {
			return errors.New("receipt does not match pending effect")
		}
		s.Workflow.Pending = nil
	case "result":
		return result(s, e, dir)
	case "approval", "cap", "handoff", "continue", "design_return", "retry", "answers", "changes":
		return answer(s, e)
	case "design_comments":
		if e.Receipt == "" || e.Answer == "" {
			return errors.New("design comments require their batch receipt")
		}
		if !slices.Contains(s.PipelineStages, "design") {
			return errors.New("this pipeline has no design stage; send plan-page comments as a changes answer on the plan gate")
		}
		if !designRevisable(s) {
			return errors.New("design comments apply only while the design or plan awaits approval")
		}
		resolveGate(s, "design", e.At)
		resolveGate(s, "plan", e.At)
		s.Workflow.Gate = ""
		return dispatch(s, e, "design")
	case "commands":
		return commands(s, e)
	case "discovery":
		if e.Note == nil || !slices.Contains(DiscoveryNotes, e.Note.Event) || e.Note.Detail == "" {
			return fmt.Errorf("a discovery note needs one of %s and a detail", strings.Join(DiscoveryNotes, ", "))
		}
		s.History = append(s.History, state.HistoryEntry{At: e.At, Stage: s.Stage, Event: e.Note.Event, Detail: e.Note.Detail})
	case "observe":
		wasDone := s.Done()
		offered := slices.ContainsFunc(s.OpenAttention(), func(a state.Attention) bool { return a.Kind == "handoff" })
		changed := !SameHeads(s.Stack, e.Stack)
		if len(e.Stack) == 0 || len(s.PRs) > 0 && len(e.PRs) == 0 || dropsPublishedPR(s.PRs, e) {
			return errors.New("observation must carry the stack and every published PR still in it")
		}
		if s.Workflow.ActiveRun != "" && !SameHeads(s.Stack, e.Stack) {
			return errors.New("cannot replace heads while a run is active")
		}
		if !SameHeads(s.Stack, e.Stack) {
			invalidate(s, e.At)
			s.Stack = e.Stack
		}
		s.PRs, s.Evidence.PRs = e.PRs, e.Observations
		s.Evidence.Pending = slices.ContainsFunc(e.Observations, func(v state.PRVerification) bool { return v.CI == "unknown" })
		if e.Comments != nil {
			if !s.WatchComments {
				return errors.New("comment watching is disabled")
			}
			s.Evidence.Comments = *e.Comments
			s.Evidence.CommentDispositions = e.CommentDispositions
		}
		ready := len(Readiness(*s)) == 0
		if ready && !s.Done() {
			offerHandoff(s, e)
		} else if !ready && (changed && wasDone || offered) {
			withdrawHandoff(s, e.At)
			return dispatch(s, e, "pr")
		}
	case "comments_preference":
		if s.Workflow.CommentsAsked || e.Receipt == "" || e.Answer == "" {
			return errors.New("comments preference needs its original developer answer, once")
		}
		s.WatchComments, s.Workflow.CommentsAsked = e.WatchComments, true
		s.History = append(s.History, state.HistoryEntry{At: e.At, Stage: s.Stage, Event: e.Kind, Detail: e.Answer + " (receipt: " + e.Receipt + ")"})
	case "comments_stop":
		if e.Receipt == "" || e.Answer == "" {
			return errors.New("stopping comments needs a developer answer")
		}
		s.WatchComments, s.Evidence.CommentsStopped = false, true
		s.History = append(s.History, state.HistoryEntry{At: e.At, Stage: s.Stage, Event: e.Kind, Detail: e.Answer + " (receipt: " + e.Receipt + ")"})
	case "lost_run":
		if s.Workflow.ActiveRun == "" || e.Receipt == "" {
			return errors.New("lost run needs reconciliation evidence")
		}
		run := &s.Workflow.Runs[len(s.Workflow.Runs)-1]
		if run.Retries >= s.Limits.StageRetries {
			attention(s, e, "failure", "Stage retries exhausted", run.ID)
			return nil
		}
		run.Retries++
		s.Workflow.Pending = &state.Effect{ID: run.ID, Kind: "stage", RunID: run.ID, Stage: run.Stage, Role: run.Role}
	default:
		return fmt.Errorf("unknown workflow event %q", e.Kind)
	}
	return nil
}

const RediscoveryLoop = "rediscovery"

func commands(s *state.State, e Event) error {
	if e.Recipe == nil || e.Recipe.Digest == "" {
		return errors.New("missing command recipe")
	}
	changed := s.Recipe.Digest != e.Recipe.Digest
	if changed {
		if !recipeChangeable(s) {
			return errors.New("changed commands wait until no run is active and no developer item resumes a stage other than implementation")
		}
		invalidate(s, e.At)
	}
	s.Recipe = *e.Recipe
	failure := settleable(s)
	if len(s.Recipe.Unresolved) > 0 {
		if failure != nil {
			settle(s, failure, e.At)
		}
		if openCommands(s) == nil {
			attention(s, e, "commands", "Resolve repository commands", cmp.Or(e.Detail, strings.Join(s.Recipe.Unresolved, "\n")))
		}
		if failure != nil {
			openCommands(s).LimitKey = RediscoveryLoop
		}
		return nil
	}
	rerun := false
	for i := range s.Attention {
		if s.Attention[i].Kind == "commands" && s.Attention[i].Open() {
			s.Attention[i].ResolvedAt = e.At
			rerun = rerun || s.Attention[i].LimitKey == RediscoveryLoop
		}
	}
	if changed && failure != nil {
		settle(s, failure, e.At)
		rerun = true
	}
	switch {
	case rerun:
		s.Workflow.ActiveRun, s.Workflow.Pending = "", nil
		return dispatch(s, e, rerunStage(s))
	case stranded(s) && len(s.PRs) > 0:
		return dispatch(s, e, "pr")
	case stranded(s):
		return dispatch(s, e, "implement")
	}
	return nil
}

var recipeStages = []string{"implement", "review", "qa", "pr"}

func openCommands(s *state.State) *state.Attention {
	for i := range s.Attention {
		if s.Attention[i].Kind == "commands" && s.Attention[i].Open() {
			return &s.Attention[i]
		}
	}
	return nil
}

func openFailure(s *state.State) *state.Attention {
	if len(s.Workflow.Runs) == 0 || s.Workflow.ActiveRun != "" {
		return nil
	}
	stage := s.Workflow.Runs[len(s.Workflow.Runs)-1].Stage
	if !slices.Contains(recipeStages, stage) {
		return nil
	}
	for i := range s.Attention {
		if s.Attention[i].Kind == "failure" && s.Attention[i].StageKey == stage && s.Attention[i].Open() {
			return &s.Attention[i]
		}
	}
	return nil
}

func settleable(s *state.State) *state.Attention {
	if s.Loops.Used[RediscoveryLoop] >= s.Limits.StageRetries {
		return nil
	}
	return openFailure(s)
}

func settle(s *state.State, failure *state.Attention, at state.Time) {
	failure.ResolvedAt, failure.Answer = at, "commands rediscovered"
	s.Loops.Used[RediscoveryLoop]++
}

var afterImplementation = []string{"review", "qa", "pr"}

func rerunStage(s *state.State) string {
	stage := s.Workflow.Runs[len(s.Workflow.Runs)-1].Stage
	switch {
	case !slices.Contains(afterImplementation, stage) || LocalChecks(*s, s.Evidence.Local):
		return stage
	case len(s.PRs) > 0:
		return "pr"
	}
	return "implement"
}

func retry(s *state.State, e Event, stage string) error {
	last := s.Workflow.Runs[len(s.Workflow.Runs)-1]
	s.Workflow.ActiveRun, s.Workflow.Pending = "", nil
	if err := dispatch(s, e, stage); err != nil {
		return err
	}
	s.Workflow.Runs[len(s.Workflow.Runs)-1].Retries = last.Retries + 1
	return nil
}

func dispatch(s *state.State, e Event, stage string) error {
	if s.Workflow.ActiveRun != "" || s.Workflow.Pending != nil {
		return errors.New("reconcile the existing run before dispatch")
	}
	role := state.RoleOf(stage)
	if role == "" {
		return fmt.Errorf("no agent for stage %q", stage)
	}
	if stage == "implement" && (s.Recipe.Digest == "" || len(s.Recipe.Unresolved) > 0) {
		return errors.New("resolve repository commands before implementation")
	}
	st := s.Stages[stage]
	st.Rounds++
	st.Status = "running"
	s.Stages[stage] = st
	id := fmt.Sprintf("%s-%s-%d", e.ID, role, st.Rounds)
	run := state.WorkflowRun{ID: id, Stage: stage, Role: role, Round: st.Rounds, Loop: s.Loops.Active, InputHeads: slices.Clone(s.Stack), ReturnTo: s.Workflow.ReturnTo, Prompt: "stages/" + id + ".prompt.md", Result: "stages/" + id + ".result.json"}
	s.Workflow.Runs = append(s.Workflow.Runs, run)
	s.Workflow.ActiveRun = id
	s.Workflow.Pending = &state.Effect{ID: id, Kind: "stage", RunID: id, Stage: stage, Role: role}
	s.Stage, s.WaitingOn, s.WaitingReason = stage, "agent", ""
	s.History = append(s.History, state.HistoryEntry{At: e.At, Stage: stage, Event: "dispatch", RunID: id, Role: role, Round: st.Rounds, Loop: s.Loops.Active})
	return nil
}

func result(s *state.State, e Event, dir string) error {
	r := e.Result
	if r == nil || r.SchemaVersion != 2 || r.RunID == "" || r.RunID != s.Workflow.ActiveRun || len(s.Workflow.Runs) == 0 {
		return errors.New("result does not match active run")
	}
	run := &s.Workflow.Runs[len(s.Workflow.Runs)-1]
	if err := resolveCommitHeads(s, *run, r); err != nil {
		return err
	}
	if !SameHeads(r.InputHeads, run.InputHeads) || !SameHeads(e.Stack, r.OutputHeads) {
		return errors.New("result input or observed output heads do not match")
	}
	if !slices.Contains([]string{"done", "blocked", "failed"}, r.Status) {
		return errors.New("invalid result outcome")
	}
	stage := run.Stage
	if r.Status == "done" && (stage == "design" || stage == "plan") {
		if err := reviewPages(dir, stage, r.Artifacts); err != nil {
			return err
		}
	}
	readOnly := stage == "review" || stage == "qa"
	if readOnly && !SameHeads(r.InputHeads, r.OutputHeads) {
		return errors.New("read-only stage changed the stack")
	}
	for _, f := range r.Findings {
		if f.ID == "" || f.Description == "" || !slices.Contains([]string{"minor", "major", "blocker", "regression", "design"}, f.Severity) || !slices.Contains([]string{"open", "closed"}, f.Disposition) {
			return errors.New("invalid finding or agent-granted risk acceptance")
		}
		i := slices.IndexFunc(s.Findings, func(old state.Finding) bool { return old.ID == f.ID })
		if i < 0 {
			if f.Stage == "" {
				f.Stage = stage
			}
			s.Findings = append(s.Findings, f)
		} else {
			if f.Stage == "" {
				f.Stage = s.Findings[i].Stage
			}
			s.Findings[i] = f
		}
	}
	if !SameHeads(s.Stack, r.OutputHeads) {
		invalidate(s, e.At)
		s.Stack = r.OutputHeads
	}
	run.Outcome = r.Status
	s.Workflow.ActiveRun, s.Workflow.Pending = "", nil
	st := s.Stages[stage]
	st.Status, st.Outputs, st.Artifacts = r.Status, r.Outputs, r.Artifacts
	s.Stages[stage] = st
	if r.Status == "blocked" && len(r.Questions) > 0 {
		attention(s, e, "question", "Stage needs answers", "Answer this batch to resume the owning stage.")
		ask(&s.Attention[len(s.Attention)-1], r.Questions)
		return nil
	}
	if r.Status != "done" {
		attention(s, e, "failure", "Stage needs attention", failureBody(stage, r))
		return nil
	}
	switch stage {
	case "requirements":
		s.NeedsDesign = slices.Contains(s.PipelineStages, "design")
		if !s.Workflow.CommentsAsked {
			return errors.New("record the comments preference before accepting requirements")
		}
		return gate(s, e, "requirements")
	case "design":
		return gate(s, e, "design")
	case "plan":
		if !slices.ContainsFunc(s.Findings, gatingDesign) {
			return gate(s, e, "plan")
		}
		if slices.Contains(s.PipelineStages, "design") {
			attention(s, e, "design_return", "Revise the design or keep the plan?", withFindings("The plan found gaps in the approved design. Revise the design: the designer reworks it for these findings, you approve it again, then the architect plans again. Keep the plan: the findings are recorded as accepted risks and the plan comes to you for approval.", s.Findings, gatingDesign))
			return nil
		}
		if err := gate(s, e, "plan"); err != nil {
			return err
		}
		item := &s.Attention[len(s.Attention)-1]
		item.Body = withFindings(item.Body+" The plan found gaps in the design it was given; comment on them like the rest of the plan, and approving it records them as accepted risks.", s.Findings, gatingDesign)
		return nil
	case "implement":
		if !LocalChecks(*s, r.Evidence) {
			return errors.New("implementation needs current successful build, lint and test evidence")
		}
		s.Evidence.Local = r.Evidence
		return dispatch(s, e, "review")
	case "review":
		if slices.ContainsFunc(s.Findings, func(f state.Finding) bool { return f.Stage == "review" && Gating(f) }) {
			return correct(s, e, state.Correction{Loop: ReviewLoop, Stage: "implement", ReturnTo: s.Workflow.ReturnTo})
		}
		if !Current(*s, r.Evidence) || !LocalChecks(*s, s.Evidence.Local) {
			return errors.New("partial review or stale local checks cannot pass")
		}
		s.Evidence.Review = r.Evidence
		if s.Loops.Active == ReviewLoop {
			s.Loops.Active, s.Workflow.ReturnLoop = s.Workflow.ReturnLoop, ""
		}
		next := s.Workflow.ReturnTo
		s.Workflow.ReturnTo = ""
		if next == "" {
			next = "pr"
			if slices.Contains(s.PipelineStages, "qa") {
				next = "qa"
			}
		}
		return dispatch(s, e, next)
	case "qa":
		if slices.ContainsFunc(s.Findings, Gating) {
			return correct(s, e, state.Correction{Loop: QALoop, Stage: "implement", ReturnTo: "qa"})
		}
		if !Current(*s, r.Evidence) {
			return errors.New("QA needs complete current evidence")
		}
		s.Evidence.QA = r.Evidence
		if s.Loops.Active == QALoop {
			s.Loops.Active = ""
		}
		return dispatch(s, e, "pr")
	case "pr":
		if slices.ContainsFunc(s.Findings, Gating) {
			return correct(s, e, state.Correction{Loop: CILoop, Stage: "pr", ReturnTo: "pr"})
		}
		if !SameHeads(r.InputHeads, r.OutputHeads) || !Current(*s, s.Evidence.Review) {
			if !LocalChecks(*s, r.Evidence) {
				return errors.New("CI fixes need current local checks")
			}
			s.Evidence.Local = r.Evidence
			s.Workflow.ReturnTo = "pr"
			if slices.Contains(s.PipelineStages, "qa") {
				s.Workflow.ReturnTo = "qa"
			}
			return dispatch(s, e, "review")
		}
		if reasons := Readiness(*s); len(reasons) > 0 {
			return fmt.Errorf("handoff is not ready: %s", strings.Join(reasons, "; "))
		}
		offerHandoff(s, e)
	}
	return nil
}

func resolveCommitHeads(s *state.State, run state.WorkflowRun, r *Result) error {
	fields := []struct {
		name  string
		shas  []string
		heads *[]state.StackBranch
	}{
		{"input_heads", r.inputSHAs, &r.InputHeads},
		{"output_heads", r.outputSHAs, &r.OutputHeads},
		{"evidence.heads", r.evidenceSHAs, &r.Evidence.Heads},
	}
	noStackYet := len(s.Stack) == 0 && len(run.InputHeads) == 0
	for _, field := range fields {
		if field.shas == nil {
			continue
		}
		if !noStackYet || slices.ContainsFunc(field.shas, func(sha string) bool { return !startCommit(s, sha) }) {
			return fmt.Errorf("%s must list stack branches {branch,parent,head,base}; a commit SHA is accepted only before the stack exists, for the start commit", field.name)
		}
		*field.heads = []state.StackBranch{}
		r.Evidence.Notes = append(r.Evidence.Notes, fmt.Sprintf("heads: %s named the start commit %s", field.name, s.StartCommit))
	}
	return nil
}

func startCommit(s *state.State, sha string) bool {
	return len(sha) >= 7 && strings.HasPrefix(s.StartCommit, sha)
}

func reviewPages(dir, stage string, artifacts []state.Artifact) error {
	const form = `{"url":"design/variant-a.html","title":"..."}`
	if len(artifacts) == 0 {
		return fmt.Errorf("a done %s result needs an \"artifacts\" field listing every page the developer reviews, each as %s with its url relative to the factory directory", stage, form)
	}
	for _, a := range artifacts {
		if !state.Portable(a.URL) || !strings.HasSuffix(a.URL, ".html") {
			return fmt.Errorf("%s artifact url %q must be an .html path relative to the factory directory, as %s", stage, a.URL, form)
		}
		info, err := os.Stat(filepath.Join(dir, filepath.FromSlash(a.URL)))
		if err != nil || info.IsDir() {
			return fmt.Errorf("%s artifact %q is not a file in the factory directory %s; list each page as %s", stage, a.URL, dir, form)
		}
	}
	return nil
}

func gate(s *state.State, e Event, stage string) error {
	resolveGate(s, stage, e.At)
	attention(s, e, "gate", "Approve "+stage, "Approval applies to this artifact revision only.")
	s.Attention[len(s.Attention)-1].StageKey = stage
	s.Workflow.Gate = stage
	return nil
}

func resolveGate(s *state.State, stage string, at state.Time) {
	for i := range s.Attention {
		if s.Attention[i].Kind == "gate" && s.Attention[i].StageKey == stage && s.Attention[i].Open() {
			s.Attention[i].ResolvedAt = at
		}
	}
}

func designRevisable(s *state.State) bool {
	return slices.ContainsFunc(s.OpenAttention(), func(a state.Attention) bool {
		return a.Kind == "gate" && (a.StageKey == "design" || a.StageKey == "plan")
	})
}

func recipeChangeable(s *state.State) bool {
	if s.Stages["implement"].Rounds == 0 {
		return true
	}
	running := s.Workflow.ActiveRun != "" || s.Workflow.Pending != nil
	failure := openFailure(s)
	resumesAnotherStage := slices.ContainsFunc(s.OpenAttention(), func(a state.Attention) bool {
		settled := failure != nil && a.ID == failure.ID
		return a.Kind != "handoff" && a.Kind != "commands" && !resumesImplementation(a) && !settled
	})
	return !running && !resumesAnotherStage
}

func resumesImplementation(a state.Attention) bool {
	return (a.Kind == "failure" || a.Kind == "question") && a.StageKey == "implement"
}

func stranded(s *state.State) bool {
	implemented := s.Stages["implement"].Rounds > 0
	idle := s.Workflow.ActiveRun == "" && s.Workflow.Pending == nil && len(s.OpenAttention()) == 0
	return implemented && idle && !LocalChecks(*s, s.Evidence.Local)
}

func dropsPublishedPR(published []state.PR, e Event) bool {
	return slices.ContainsFunc(published, func(p state.PR) bool {
		stillStacked := slices.ContainsFunc(e.Stack, func(b state.StackBranch) bool { return b.Branch == p.Branch })
		observed := slices.ContainsFunc(e.PRs, func(o state.PR) bool { return o.Branch == p.Branch })
		return stillStacked && !observed
	})
}

func gatingDesign(f state.Finding) bool {
	return f.Severity == "design" && Gating(f)
}

func acceptDesignGaps(s *state.State) {
	for i := range s.Findings {
		if gatingDesign(s.Findings[i]) {
			s.Findings[i].Disposition = "accepted_risk"
		}
	}
}

func withFindings(body string, findings []state.Finding, include func(state.Finding) bool) string {
	for _, f := range findings {
		if include(f) {
			body += "\n" + f.ID + ": " + f.Description
		}
	}
	return body
}

func failureBody(stage string, r *Result) string {
	lines := []string{stage + ": " + r.Status}
	for _, said := range []string{r.Summary, r.Evidence.Reason} {
		if said = strings.TrimSpace(said); said != "" {
			lines = append(lines, said)
		}
	}
	for _, note := range r.Evidence.Notes {
		lines = append(lines, "- "+note)
	}
	if len(r.Outputs) > 0 {
		lines = append(lines, "Outputs: "+strings.Join(r.Outputs, ", "))
	}
	return strings.Join(lines, "\n")
}

func attention(s *state.State, e Event, kind, title, body string) {
	s.Attention = append(s.Attention, state.Attention{ID: e.ID + "-" + kind, Revision: s.Revision + 1, Kind: kind, Title: title, Body: body, StageKey: s.Stage, CreatedAt: e.At})
	s.WaitingOn, s.WaitingReason = "user", title
}

func ask(item *state.Attention, questions []Question) {
	for _, q := range questions {
		item.Questions = append(item.Questions, q.Text)
		item.Options = append(item.Options, append([]string{}, q.Options...))
		item.Recommended = append(item.Recommended, q.Recommended)
	}
}

func allowance(s *state.State, loop string) *int {
	switch loop {
	case DesignLoop:
		return &s.Limits.DesignPlanRounds
	case ReviewLoop:
		return &s.Limits.ReviewFixRounds
	case QALoop:
		return &s.Limits.QAFixRounds
	case CILoop:
		return &s.Limits.CIFixRounds
	}
	return nil
}

func correct(s *state.State, e Event, c state.Correction) error {
	allowed := allowance(s, c.Loop)
	if allowed == nil {
		return errors.New("unknown correction loop")
	}
	if s.Loops.Used[c.Loop] >= *allowed {
		s.Workflow.Correction = &c
		attention(s, e, "cap", "Allow more correction cycles?", withFindings("Open findings remain; refusal keeps the factory waiting.", s.Findings, Gating))
		a := &s.Attention[len(s.Attention)-1]
		a.Loop, a.LimitKey, a.Used, a.Allowed = c.Loop, c.Loop+"_rounds", s.Loops.Used[c.Loop], *allowed
		return nil
	}
	if c.Loop == ReviewLoop && s.Loops.Active != ReviewLoop {
		s.Workflow.ReturnLoop = s.Loops.Active
	}
	s.Loops.Used[c.Loop]++
	s.Loops.Active, s.Workflow.ReturnTo = c.Loop, c.ReturnTo
	s.Workflow.Correction = nil
	invalidate(s, e.At)
	return dispatch(s, e, c.Stage)
}

func answer(s *state.State, e Event) error {
	i := slices.IndexFunc(s.Attention, func(a state.Attention) bool { return a.ID == e.ItemID && a.Open() })
	if i < 0 || e.Answer == "" || e.Receipt == "" || e.ArtifactRevision != s.Attention[i].Revision {
		return errors.New("answer needs the current item, artifact revision and developer receipt")
	}
	a := &s.Attention[i]
	if e.Kind == "answers" || e.Kind == "changes" {
		if (e.Kind == "answers" && a.Kind != "question") || (e.Kind == "changes" && a.Kind != "gate") {
			return errors.New("answer does not match the question or gate")
		}
		stage := a.StageKey
		a.ResolvedAt, a.Answer, a.Receipt = e.At, e.Answer, e.Receipt
		if e.Kind == "changes" {
			s.Workflow.Gate = ""
			invalidate(s, e.At)
			if stage == "design" {
				return correct(s, e, state.Correction{Loop: DesignLoop, Stage: "design", ReturnTo: "plan"})
			}
		}
		return dispatch(s, e, stage)
	}
	if e.Kind == "retry" {
		if a.Kind != "failure" || len(s.Workflow.Runs) == 0 {
			return errors.New("retry needs a failed run")
		}
		last := s.Workflow.Runs[len(s.Workflow.Runs)-1]
		if e.Increment != 0 || last.Retries >= s.Limits.StageRetries {
			if e.Increment <= 0 || e.Increment > int(^uint(0)>>1)-s.Limits.StageRetries {
				return errors.New("stage retries are exhausted: retry needs the developer's positive increment")
			}
			s.Limits.StageRetries += e.Increment
		}
		a.ResolvedAt, a.Answer, a.Receipt = e.At, e.Answer, e.Receipt
		stage := last.Stage
		if s.Workflow.ActiveRun == "" {
			stage = rerunStage(s)
		}
		return retry(s, e, stage)
	}
	if e.Kind == "cap" {
		if a.Kind != "cap" || s.Workflow.Correction == nil {
			return errors.New("not a pending cap")
		}
		a.Answer, a.Receipt = e.Answer, e.Receipt
		if !e.Approved {
			return nil
		}
		if e.Increment <= 0 {
			return errors.New("cap approval must specify a positive increment")
		}
		allowed := allowance(s, a.Loop)
		if allowed == nil || e.Increment > int(^uint(0)>>1)-*allowed {
			return errors.New("invalid cap increment")
		}
		*allowed += e.Increment
		a.ResolvedAt = e.At
		return correct(s, e, *s.Workflow.Correction)
	}
	if e.Kind == "design_return" {
		if a.Kind != "design_return" {
			return errors.New("not a design return question")
		}
		a.ResolvedAt, a.Answer, a.Receipt = e.At, e.Answer, e.Receipt
		if e.Approved {
			return correct(s, e, state.Correction{Loop: DesignLoop, Stage: "design", ReturnTo: "plan"})
		}
		acceptDesignGaps(s)
		return gate(s, e, "plan")
	}
	if e.Kind == "handoff" || e.Kind == "continue" {
		if a.Kind != "handoff" {
			return errors.New("not a handoff item")
		}
		if e.Kind == "handoff" {
			if !e.Approved || !SameHeads(s.Stack, e.Stack) {
				return errors.New("stale or unapproved completion")
			}
			s.Evidence.PRs = e.Observations
			if len(e.Observations) == 0 || slices.ContainsFunc(e.Observations, func(v state.PRVerification) bool { return v.At.Before(a.CreatedAt.Time) }) {
				return errors.New("completion needs refreshed PR evidence")
			}
			if reasons := Readiness(*s); len(reasons) > 0 {
				return fmt.Errorf("handoff is not ready: %s", strings.Join(reasons, "; "))
			}
			s.Confirmation = &state.Confirmation{ItemID: e.ItemID, Heads: slices.Clone(s.Stack), At: e.At, Receipt: e.Receipt}
			s.WaitingOn, s.WaitingReason = "", ""
			st := s.Stages["handoff"]
			st.Status = "done"
			s.Stages["handoff"] = st
			a.ResolvedAt, a.Answer, a.Receipt = e.At, e.Answer, e.Receipt
			return nil
		}
		a.ResolvedAt, a.Answer, a.Receipt = e.At, e.Answer, e.Receipt
		invalidate(s, e.At)
		return dispatch(s, e, "implement")
	}
	if a.Kind != "gate" || e.Kind != "approval" {
		return errors.New("answer cannot bypass a blocking item")
	}
	if !e.Approved {
		a.Answer, a.Receipt = e.Answer, e.Receipt
		return nil
	}
	a.ResolvedAt, a.Answer, a.Receipt = e.At, e.Answer, e.Receipt
	stage := a.StageKey
	s.Workflow.Gate = ""
	switch stage {
	case "requirements":
		if s.NeedsDesign && slices.Contains(s.PipelineStages, "design") {
			return dispatch(s, e, "design")
		}
		return dispatch(s, e, "plan")
	case "design":
		s.Workflow.ReturnTo = ""
		return dispatch(s, e, "plan")
	case "plan":
		s.Loops.Active = ""
		acceptDesignGaps(s)
		return dispatch(s, e, "implement")
	}
	return errors.New("unknown approval gate")
}

func invalidate(s *state.State, at state.Time) {
	s.Evidence.Local, s.Evidence.Review, s.Evidence.QA = state.Verification{}, state.Verification{}, state.Verification{}
	s.Evidence.PRs, s.Evidence.Comments = nil, state.Verification{}
	s.Confirmation = nil
	st := s.Stages["handoff"]
	st.Status = "pending"
	s.Stages["handoff"] = st
	withdrawHandoff(s, at)
}

func withdrawHandoff(s *state.State, at state.Time) {
	for i := range s.Attention {
		if s.Attention[i].Kind == "handoff" && s.Attention[i].Open() {
			s.Attention[i].ResolvedAt = at
		}
	}
}

func offerHandoff(s *state.State, e Event) {
	if slices.ContainsFunc(s.OpenAttention(), func(a state.Attention) bool { return a.Kind == "handoff" }) {
		return
	}
	s.Loops.Active = ""
	s.Stage = "handoff"
	attention(s, e, "handoff", "Work done: mark it done or continue", "Confirm these exact stack heads, or send follow-up work through review, QA and PR verification.")
}
