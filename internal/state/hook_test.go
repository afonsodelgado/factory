package state

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRecordHookWritesOnlyForFactories(t *testing.T) {
	stateDir := t.TempDir()
	plain := t.TempDir()
	worktree := filepath.Join(t.TempDir(), "repo", ".claude", "worktrees", "eng-1-x")
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)

	record := func(payload string, now time.Time) {
		t.Helper()
		if err := RecordHook(stateDir, []byte(payload), "", now); err != nil {
			t.Fatal(err)
		}
	}

	record(`{"session_id":"plain","cwd":"`+plain+`","hook_event_name":"Stop"}`, at)
	if _, err := os.Stat(filepath.Join(SessionsDir(stateDir), "plain.json")); err == nil {
		t.Error("a session outside any factory got a hook file")
	}
	deleted := filepath.Join(filepath.Dir(worktree), "deleted-factory")
	record(`{"session_id":"dying","cwd":"`+deleted+`","hook_event_name":"SessionEnd"}`, at)
	if _, err := os.Stat(filepath.Join(SessionsDir(stateDir), "dying.json")); err == nil {
		t.Error("a session ending after its factory was deleted wrote its hook file back")
	}

	record(`{"session_id":"s1","cwd":"`+worktree+`","hook_event_name":"Stop","transcript_path":"/t.jsonl"}`, at)
	record(`{"session_id":"s1","cwd":"`+worktree+`","hook_event_name":"PreToolUse","tool_name":"Bash","agent_id":"a1","agent_type":"factory-engineer"}`, at.Add(time.Minute))

	file, err := ReadHookFile(filepath.Join(SessionsDir(stateDir), "s1.json"))
	if err != nil {
		t.Fatalf("factory session has no hook file: %v", err)
	}
	if file.MainEvent().Event != "Stop" {
		t.Errorf("main slot = %q, want Stop: a stage agent's tool call must not make the supervisor look busy", file.MainEvent().Event)
	}
	if file.Last.Event != "PreToolUse" || file.Last.Matcher != "Bash" || !file.Last.At.Equal(at.Add(time.Minute)) {
		t.Errorf("last slot = %+v, want the subagent's PreToolUse", file.Last)
	}

	steps := []struct {
		event      string
		pending    bool
		regression string
	}{
		{`"hook_event_name":"Notification","notification_type":"permission_prompt"`, true, "a permission prompt does not need the user"},
		{`"hook_event_name":"PostToolUse","tool_name":"Bash"`, false, "the badge stays stuck after an approved prompt, as on cowork security-2-4"},
		{`"hook_event_name":"PermissionRequest","tool_name":"Bash","agent_id":"a1"`, true, "a stage agent's permission prompt does not need the user"},
		{`"hook_event_name":"Notification","notification_type":"permission_prompt"`, true, "the notification for a stage agent's prompt drops it"},
		{`"hook_event_name":"PostToolUse","tool_name":"Bash","agent_id":"a2"`, true, "another agent's tool clears a prompt the user has not answered"},
		{`"hook_event_name":"PostToolUseFailure","tool_name":"Bash","agent_id":"a1"`, false, "the asking agent's own tool result leaves the prompt open"},
		{`"hook_event_name":"Notification","notification_type":"permission_prompt"`, true, "a second prompt does not need the user"},
		{`"hook_event_name":"PostToolUse","tool_name":"Read","agent_id":"a2"`, false, "a prompt with no thread id is never cleared"},
	}
	for i, step := range steps {
		record(`{"session_id":"s1","cwd":"`+worktree+`",`+step.event+`}`, at.Add(time.Duration(i+2)*time.Minute))
		file, err := ReadHookFile(filepath.Join(SessionsDir(stateDir), "s1.json"))
		if err != nil {
			t.Fatal(err)
		}
		if pending := file.Prompt != nil; pending != step.pending {
			t.Errorf("after step %d (%s) a prompt is pending = %v, want %v: %s", i+1, step.event, pending, step.pending, step.regression)
		}
	}

	pasted := `{"session_id":"s1","cwd":"` + worktree + `","hook_event_name":"UserPromptSubmit","prompt":"<pasted_content id=\"1\">\nq-1-1 answers:\n1) recommended\n</pasted_content id=\"1\">"}`
	record(pasted, at.Add(time.Hour))
	file, err = ReadHookFile(filepath.Join(SessionsDir(stateDir), "s1.json"))
	if err != nil {
		t.Fatal(err)
	}
	if file.Submit == nil || file.Submit.PromptHead != "q-1-1 answers:" {
		t.Errorf("submit = %+v; want the head of the pasted answer, or the panel never sees the supervisor receive it", file.Submit)
	}
}

func TestFactoriesInReposWithTheSameNameKeepSeparateFiles(t *testing.T) {
	if FileStem("/home/a/dev/cowork", "eng-1") == FileStem("/home/a/work/cowork", "eng-1") {
		t.Error("two repos named cowork share one activity log and one set of drafts")
	}
}
