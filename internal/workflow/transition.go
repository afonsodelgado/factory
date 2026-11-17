package workflow

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/afonsodelgado/factory/internal/state"
)

func Next(snapshot state.State, event Event) (Proposal, error) {
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
	if err := apply(&s, event); err != nil {
		return Proposal{}, err
	}
	s.Revision++
	s.ProcessedEvents = append(s.ProcessedEvents, event.ID)
	s.UpdatedAt = event.At
	proposal.Snapshot, proposal.Effect = s, s.Workflow.Pending
	return proposal, nil
}

func apply(s *state.State, e Event) error {
	switch e.Kind {
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
		return result(s, e)
	case "approval", "cap", "handoff", "continue", "design_return", "retry":
		return answer(s, e)
	case "design_comments":
		if e.Receipt == "" || e.Answer == "" {
			return errors.New("design comments require their batch receipt")
		}
		return dispatch(s, e, "design")
	case "commands":
		if e.Recipe == nil || e.Recipe.Digest == "" {
			return errors.New("missing command recipe")
		}
		if s.Recipe.Digest != e.Recipe.Digest {
			invalidate(s)
		}
		s.Recipe = *e.Recipe
		if len(s.Recipe.Unresolved) > 0 {
			if !slices.ContainsFunc(s.OpenAttention(), func(a state.Attention) bool { return a.Kind == "commands" }) {
				attention(s, e, "commands", "Resolve repository commands", strings.Join(s.Recipe.Unresolved, "\n"))
			}
		} else {
			for i := range s.Attention {
				if s.Attention[i].Kind == "commands" && s.Attention[i].Open() {
					s.Attention[i].ResolvedAt = e.At
				}
			}
		}
	case "observe":
		wasDone := s.Done()
		changed := !SameHeads(s.Stack, e.Stack)
		if s.Workflow.ActiveRun != "" && !SameHeads(s.Stack, e.Stack) {
			return errors.New("cannot replace heads while a run is active")
		}
		if !SameHeads(s.Stack, e.Stack) {
			invalidate(s)
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
		if len(Readiness(*s)) == 0 {
			offerHandoff(s, e)
		} else if wasDone && changed {
			return dispatch(s, e, "pr")
		}
	case "comments_preference":
		if s.Workflow.CommentsAsked || e.Receipt == "" || e.Answer == "" {
			return errors.New("comments preference needs its original developer answer, once")
		}
		s.WatchComments, s.Workflow.CommentsAsked = e.WatchComments, true
	case "comments_stop":
		if e.Receipt == "" || e.Answer == "" {
			return errors.New("stopping comments needs a developer answer")
		}
		s.WatchComments, s.Evidence.CommentsStopped = false, true
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

func result(s *state.State, e Event) error {
	r := e.Result
	if r == nil || r.SchemaVersion != 2 || r.RunID == "" || r.RunID != s.Workflow.ActiveRun || len(s.Workflow.Runs) == 0 {
		return errors.New("result does not match active run")
	}
	run := &s.Workflow.Runs[len(s.Workflow.Runs)-1]
	if !SameHeads(r.InputHeads, run.InputHeads) || !SameHeads(e.Stack, r.OutputHeads) {
		return errors.New("result input or observed output heads do not match")
	}
	if !slices.Contains([]string{"done", "blocked", "failed"}, r.Status) {
		return errors.New("invalid result outcome")
	}
	stage := run.Stage
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
			s.Findings = append(s.Findings, f)
		} else {
			s.Findings[i] = f
		}
	}
	if !SameHeads(s.Stack, r.OutputHeads) {
		invalidate(s)
		s.Stack = r.OutputHeads
	}
	run.Outcome = r.Status
	s.Workflow.ActiveRun, s.Workflow.Pending = "", nil
	st := s.Stages[stage]
	st.Status, st.Outputs, st.Artifacts = r.Status, r.Outputs, r.Artifacts
	s.Stages[stage] = st
	if r.Status != "done" {
		attention(s, e, "failure", "Stage needs attention", stage+": "+r.Status)
		return nil
	}
	switch stage {
	case "requirements":
		if !s.Workflow.CommentsAsked {
			return errors.New("record the comments preference before accepting requirements")
		}
		return gate(s, e, "requirements")
	case "design":
		return gate(s, e, "design")
	case "plan":
		if slices.ContainsFunc(s.Findings, func(f state.Finding) bool { return f.Severity == "design" && Gating(f) }) {
			attention(s, e, "design_return", "Return findings to design?", "Accept to revise and approve the design again; decline to record these findings as accepted risks in the plan.")
			return nil
		}
		return gate(s, e, "plan")
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

func gate(s *state.State, e Event, stage string) error {
	for i := range s.Attention {
		if s.Attention[i].Kind == "gate" && s.Attention[i].StageKey == stage && s.Attention[i].Open() {
			s.Attention[i].ResolvedAt = e.At
		}
	}
	attention(s, e, "gate", "Approve "+stage, "Approval applies to this artifact revision only.")
	s.Workflow.Gate = stage
	return nil
}

func attention(s *state.State, e Event, kind, title, body string) {
	s.Attention = append(s.Attention, state.Attention{ID: e.ID + "-" + kind, Revision: s.Revision + 1, Kind: kind, Title: title, Body: body, StageKey: s.Stage, CreatedAt: e.At})
	s.WaitingOn, s.WaitingReason = "user", title
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
		attention(s, e, "cap", "Allow more correction cycles?", "Open findings remain; refusal keeps the factory waiting.")
		a := &s.Attention[len(s.Attention)-1]
		a.Loop, a.LimitKey, a.Used, a.Allowed = c.Loop, c.Loop+"_rounds", s.Loops.Used[c.Loop], *allowed
		for _, f := range s.Findings {
			if Gating(f) {
				a.Body += "\n" + f.ID + ": " + f.Description
			}
		}
		return nil
	}
	s.Loops.Used[c.Loop]++
	s.Loops.Active, s.Workflow.ReturnTo = c.Loop, c.ReturnTo
	s.Workflow.Correction = nil
	invalidate(s)
	return dispatch(s, e, c.Stage)
}

func answer(s *state.State, e Event) error {
	i := slices.IndexFunc(s.Attention, func(a state.Attention) bool { return a.ID == e.ItemID && a.Open() })
	if i < 0 || e.Answer == "" || e.Receipt == "" || e.ArtifactRevision != s.Attention[i].Revision {
		return errors.New("answer needs the current item, artifact revision and developer receipt")
	}
	a := &s.Attention[i]
	if e.Kind == "retry" {
		if a.Kind != "failure" || len(s.Workflow.Runs) == 0 {
			return errors.New("retry needs a failed run")
		}
		last := s.Workflow.Runs[len(s.Workflow.Runs)-1]
		if last.Retries >= s.Limits.StageRetries {
			return errors.New("stage retry allowance exhausted")
		}
		a.ResolvedAt, a.Answer, a.Receipt = e.At, e.Answer, e.Receipt
		if s.Workflow.ActiveRun != "" {
			s.Workflow.ActiveRun = ""
			s.Workflow.Pending = nil
		}
		if err := dispatch(s, e, last.Stage); err != nil {
			return err
		}
		s.Workflow.Runs[len(s.Workflow.Runs)-1].Retries = last.Retries + 1
		return nil
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
		for i := range s.Findings {
			if s.Findings[i].Severity == "design" && Gating(s.Findings[i]) {
				s.Findings[i].Disposition = "accepted_risk"
			}
		}
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
			if len(e.Observations) == 0 || slices.ContainsFunc(e.Observations, func(v state.PRVerification) bool { return v.At.Before(e.At.Time) }) {
				return errors.New("completion needs refreshed PR evidence")
			}
			if reasons := Readiness(*s); len(reasons) > 0 {
				return fmt.Errorf("handoff is not ready: %s", strings.Join(reasons, "; "))
			}
			s.Confirmation = &state.Confirmation{ItemID: e.ItemID, Heads: slices.Clone(s.Stack), At: e.At, Receipt: e.Receipt}
			st := s.Stages["handoff"]
			st.Status = "done"
			s.Stages["handoff"] = st
			a.ResolvedAt, a.Answer, a.Receipt = e.At, e.Answer, e.Receipt
			return nil
		}
		a.ResolvedAt, a.Answer, a.Receipt = e.At, e.Answer, e.Receipt
		invalidate(s)
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
	stage := s.Workflow.Gate
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
		return dispatch(s, e, "implement")
	}
	return errors.New("unknown approval gate")
}

func invalidate(s *state.State) {
	s.Evidence.Local, s.Evidence.Review, s.Evidence.QA = state.Verification{}, state.Verification{}, state.Verification{}
	s.Evidence.PRs, s.Evidence.Comments = nil, state.Verification{}
	s.Confirmation = nil
	st := s.Stages["handoff"]
	st.Status = "pending"
	s.Stages["handoff"] = st
	for i := range s.Attention {
		if s.Attention[i].Kind == "handoff" && s.Attention[i].Open() {
			s.Attention[i].ResolvedAt = s.UpdatedAt
		}
	}
}

func offerHandoff(s *state.State, e Event) {
	if slices.ContainsFunc(s.OpenAttention(), func(a state.Attention) bool { return a.Kind == "handoff" }) {
		return
	}
	s.Stage = "handoff"
	attention(s, e, "handoff", "Work done: mark it done or continue", "Confirm these exact stack heads, or send follow-up work through review, QA and PR verification.")
}
