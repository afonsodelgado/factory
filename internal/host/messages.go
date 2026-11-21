package host

import (
	"fmt"
	"strconv"
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

const MaxCapRounds = 2

func CapRounds(option string) (int, bool) {
	if option == "" {
		return 1, true
	}
	n, err := strconv.Atoi(option)
	return n, err == nil && n >= 1 && n <= MaxCapRounds
}

func CapContinue(id, limit string, rounds int, text string) string {
	message := fmt.Sprintf("%s continue: raise %s by %d for this factory", id, limit, rounds)
	if instruction := oneLine(text); instruction != "" {
		message += ". " + instruction
	}
	return message
}

func Retry() string {
	return "/factory resume"
}

func Handled(id string) string {
	return id + " handled: settled outside the panel"
}

func MarkDone(id string) string {
	return id + " done: the developer marked this factory done"
}

func KeepWorking(id, text string) string {
	return id + " continue: " + strings.TrimSpace(text)
}

func Instruction(id, text string) string {
	return id + " " + strings.TrimSpace(text)
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
	ActMessage    = "message"
	ActPause      = "pause"
	ActWake       = "wake"
	ActPermission = "permission"
	ActPrompt     = "prompt"
	ActDismiss    = "dismiss"
	ActComments   = "comments"
)

func CommentsCommand(option string) string {
	return "/factory comments " + option
}

func CanToggleComments(it Item) bool {
	f := it.Factory
	return f.State != nil && !f.Archived() && !f.State.Done() && it.Pane != nil && !it.Pane.Dead
}

const MaxMessageRunes = 4000

func ActionText(item AttentionItem, action, option string, answers []string, text string) string {
	switch {
	case action == ActHandled && item.Kind == "handoff":
		return MarkDone(item.ID)
	case action == ActContinue && item.Kind == "handoff":
		return KeepWorking(item.ID, text)
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
		rounds, _ := CapRounds(option)
		return CapContinue(item.ID, item.LimitKey, rounds, text)
	case action == ActRetry:
		return Retry()
	}
	return Instruction(item.ID, text)
}

func Fits(item AttentionItem, action string) bool {
	switch action {
	case ActHandled, ActDismiss:
		return item.Answerable()
	case ActAnswer:
		return item.Kind == "question" || item.Kind == "commands" || item.Kind == "design_return" || item.Kind == "cap" || item.Kind == "failure"
	case ActApprove, ActChanges:
		return item.Kind == "gate"
	case ActReply:
		return item.Kind == "comment"
	case ActContinue:
		return item.Kind == "cap" || item.Kind == "handoff"
	case ActRetry:
		return item.Kind == "failure"
	}
	return false
}
