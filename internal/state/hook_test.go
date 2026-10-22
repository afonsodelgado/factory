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
		if err := RecordHook(stateDir, []byte(payload), now); err != nil {
			t.Fatal(err)
		}
	}

	record(`{"session_id":"plain","cwd":"`+plain+`","hook_event_name":"Stop"}`, at)
	if _, err := os.Stat(filepath.Join(SessionsDir(stateDir), "plain.json")); err == nil {
		t.Error("a session outside any factory got a hook file")
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
}
