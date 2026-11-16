package host

import "testing"

func TestPanelMessagesMatchTheSupervisorContract(t *testing.T) {
	batch := QuestionAnswers("q-7452-1", []string{"10 per minute per IP", "429 with Retry-After.\nLog the key prefix only.", "recommended"})
	want := "q-7452-1 answers:\n1) 10 per minute per IP\n2) 429 with Retry-After. Log the key prefix only.\n3) recommended"
	if batch != want {
		t.Errorf("question batch =\n%s\nwant\n%s", batch, want)
	}

	approve := GateApprove("plan", "gate-ENG-1-plan", "Go with B,\nbut keep A's per-row retry.")
	if approve != "/factory approve plan gate-ENG-1-plan Go with B, but keep A's per-row retry." {
		t.Errorf("gate approval = %q, want one line with the stage, the item id and the notes", approve)
	}
}
