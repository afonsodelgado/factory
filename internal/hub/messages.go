package hub

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
	return fmt.Sprintf("%s continue: raise %s by one for this ticket", id, limit)
}

func Skip(stage string) string {
	return "/factory skip " + stage
}

func Retry() string {
	return "/factory resume"
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
