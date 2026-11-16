package host

import (
	"fmt"
	"strings"
)

func QuestionAnswers(id string, answers []string) string {
	lines := []string{id + " answers:"}
	for i, answer := range answers {
		lines = append(lines, fmt.Sprintf("%d) %s", i+1, oneLine(answer)))
	}
	return strings.Join(lines, "\n")
}

func GateApprove(stage, id, notes string) string {
	return strings.TrimSpace(fmt.Sprintf("/factory approve %s %s %s", stage, id, oneLine(notes)))
}

func GateChanges(id, text string) string {
	return id + " changes: " + strings.TrimSpace(text)
}

func CommentReply(id, text string) string {
	return id + " reply: " + strings.TrimSpace(text)
}

func CapContinue(id, limit string) string {
	return fmt.Sprintf("%s continue: raise %s by one for this factory", id, limit)
}

func Skip(stage string) string {
	return "/factory skip " + stage
}

func Retry() string {
	return "/factory resume"
}

func Handled(id string) string {
	return id + " handled: settled outside the panel"
}

func Instruction(id, text string) string {
	return id + " " + strings.TrimSpace(text)
}

func LimitFor(stage string) string {
	if stage == "coderabbit" {
		return "coderabbit_rounds"
	}
	return "qa_rounds"
}

func oneLine(text string) string {
	return strings.Join(strings.Fields(text), " ")
}

const (
	ActAnswer     = "answer"
	ActApprove    = "approve"
	ActChanges    = "changes"
	ActReply      = "reply"
	ActHandled    = "handled"
	ActContinue   = "continue"
	ActRetry      = "retry"
	ActSkip       = "skip"
	ActMessage    = "message"
	ActPause      = "pause"
	ActWake       = "wake"
	ActPermission = "permission"
)

const MaxMessageRunes = 4000

func ActionText(item AttentionItem, action string, answers []string, text string) string {
	switch {
	case action == ActHandled:
		return Handled(item.ID)
	case action == ActAnswer && item.AsksQuestions():
		return QuestionAnswers(item.ID, answers)
	case action == ActApprove:
		return GateApprove(item.Stage, item.ID, text)
	case action == ActChanges:
		return GateChanges(item.ID, text)
	case action == ActReply:
		return CommentReply(item.ID, text)
	case action == ActContinue:
		return CapContinue(item.ID, LimitFor(item.Stage))
	case action == ActSkip:
		return Skip(item.Stage)
	case action == ActRetry:
		return Retry()
	}
	return Instruction(item.ID, text)
}

func Fits(item AttentionItem, action string) bool {
	switch action {
	case ActHandled:
		return item.Answerable()
	case ActAnswer:
		return item.Kind == "question" || item.Kind == "cap" || item.Kind == "failure"
	case ActApprove, ActChanges:
		return item.Kind == "gate"
	case ActReply:
		return item.Kind == "comment"
	case ActContinue:
		return item.Kind == "cap"
	case ActRetry:
		return item.Kind == "failure"
	case ActSkip:
		return item.Kind == "cap" || item.Kind == "failure"
	}
	return false
}
