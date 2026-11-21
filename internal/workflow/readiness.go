package workflow

import (
	"fmt"
	"slices"

	"github.com/afonsodelgado/factory/internal/state"
)

const commentsUnhandled = "comments have not been handled"

func SameHeads(a, b []state.StackBranch) bool {
	return slices.EqualFunc(a, b, func(x, y state.StackBranch) bool {
		return x.Branch == y.Branch && x.Parent == y.Parent && x.Head == y.Head && x.Base == y.Base
	})
}

func Current(s state.State, v state.Verification) bool {
	return v.Status == "pass" && !v.At.IsZero() && v.RecipeDigest == s.Recipe.Digest && SameHeads(v.Heads, s.Stack)
}

func LocalChecks(s state.State, v state.Verification) bool {
	if !Current(s, v) || len(s.Stack) == 0 || s.Recipe.Digest == "" || len(s.Recipe.Unresolved) != 0 {
		return false
	}
	head := s.Stack[len(s.Stack)-1].Head
	for _, name := range []string{"build", "lint", "test"} {
		command, ok := s.Recipe.Commands[name]
		if !ok || command.Command == "" || command.Directory == "" || command.Source == "" {
			return false
		}
		found := slices.ContainsFunc(v.Checks, func(c state.CheckEvidence) bool {
			return c.Name == name && c.Command == command.Command && c.Directory == command.Directory && c.Head == head && c.ExitStatus == 0 && c.Path != ""
		})
		if !found {
			return false
		}
	}
	return true
}

func Gating(f state.Finding) bool {
	return f.Disposition != "closed" && f.Disposition != "accepted_risk" && (f.Severity == "major" || f.Severity == "blocker" || f.Severity == "regression" || f.Severity == "design")
}

func Readiness(s state.State) []string {
	var reasons []string
	if len(s.Stack) == 0 {
		reasons = append(reasons, "implementation stack is empty")
	}
	if !LocalChecks(s, s.Evidence.Local) {
		reasons = append(reasons, "local checks are not current")
	}
	if !Current(s, s.Evidence.Review) {
		reasons = append(reasons, "independent review is not current")
	}
	if slices.Contains(s.PipelineStages, "qa") && !Current(s, s.Evidence.QA) {
		reasons = append(reasons, "QA is not current")
	}
	for _, f := range s.Findings {
		if Gating(f) {
			reasons = append(reasons, "open finding: "+f.ID)
		}
	}
	for _, a := range s.OpenAttention() {
		if a.Kind != "handoff" {
			reasons = append(reasons, "open attention: "+a.ID)
		}
	}
	if s.Workflow.ActiveRun != "" || s.Workflow.Pending != nil {
		reasons = append(reasons, "work is still running")
	}
	if s.Evidence.Pending {
		reasons = append(reasons, "verification pending")
	}
	if len(s.PRs) != len(s.Stack) || len(s.Evidence.PRs) != len(s.Stack) {
		reasons = append(reasons, "publication evidence is incomplete")
	}
	for _, branch := range s.Stack {
		i := slices.IndexFunc(s.PRs, func(p state.PR) bool { return p.Branch == branch.Branch })
		if i < 0 {
			reasons = append(reasons, "unpublished branch: "+branch.Branch)
			continue
		}
		p := s.PRs[i]
		j := slices.IndexFunc(s.Evidence.PRs, func(v state.PRVerification) bool { return v.Number == p.Number })
		if j < 0 {
			reasons = append(reasons, fmt.Sprintf("PR %d has no verification", p.Number))
			continue
		}
		v := s.Evidence.PRs[j]
		valid := p.URL != "" && p.Base == branch.Parent && p.HeadSHA == branch.Head && p.BaseSHA == branch.Base && v.Head == branch.Head && v.Base == branch.Base && v.Open && !v.Draft && v.CI == "pass" && v.Policy == "pass" && !v.At.IsZero()
		if !valid {
			reasons = append(reasons, fmt.Sprintf("PR %d is not ready at its current head/base", p.Number))
		}
	}
	if s.WatchComments && !s.Evidence.CommentsStopped && !Current(s, s.Evidence.Comments) {
		reasons = append(reasons, commentsUnhandled)
	}
	return reasons
}
