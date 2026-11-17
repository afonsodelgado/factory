package host

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestChecksNeverTurnUnknownRequiredOrSkippedWorkIntoAnEmptyPass(t *testing.T) {
	cases := []struct {
		name           string
		checks         []apiCheck
		required       []requiredCheck
		elapsed        time.Duration
		status, reason string
	}{
		{name: "discovery", status: "pending", reason: "discovering_checks"},
		{name: "confirmed empty", elapsed: CheckDiscovery, status: "pass", reason: "no_checks"},
		{name: "missing required", required: []requiredCheck{{Context: "ci"}}, elapsed: CheckDiscovery, status: "pending", reason: "missing_required_check"},
		{name: "all skipped", checks: []apiCheck{{Name: "ci", Conclusion: "skipped"}}, elapsed: CheckDiscovery, status: "fail", reason: "all_checks_skipped"},
		{name: "pending", checks: []apiCheck{{Name: "ci", Status: "queued"}}, status: "pending", reason: "checks_running"},
		{name: "red", checks: []apiCheck{{Name: "ci", Conclusion: "failure"}}, status: "fail", reason: "failed_check"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			status, reason, _ := interpretChecks(c.checks, c.required, c.elapsed)
			if status != c.status || reason != c.reason {
				t.Fatal(status, reason)
			}
		})
	}
}

func TestWatcherPersistsDiscoveryAcrossRestartAndFailsClosedOnAPIError(t *testing.T) {
	start := time.Unix(100, 0)
	now := start
	calls := 0
	gh := &GitHub{Run: func(dir string, args ...string) ([]byte, error) {
		calls++
		command := strings.Join(args, " ")
		switch {
		case command == "repo view --json nameWithOwner":
			return []byte(`{"nameWithOwner":"owner/repo"}`), nil
		case strings.Contains(command, "pulls/1"):
			return []byte(`{"number":1,"head":{"sha":"h","ref":"feature"},"base":{"sha":"b","ref":"main"},"state":"open","draft":true,"mergeable":true,"mergeable_state":"clean"}`), nil
		case strings.Contains(command, "rules/branches"):
			return []byte(`[[]]`), nil
		case strings.Contains(command, "branches/main"):
			return []byte(`{"protected":false,"commit":{"sha":"b"}}`), nil
		case strings.Contains(command, "check-runs"):
			return []byte(`[{"total_count":0,"check_runs":[]}]`), nil
		case strings.Contains(command, "statuses"):
			return []byte(`[[]]`), nil
		case strings.Contains(command, "reviewDecision"):
			return []byte(`{"reviewDecision":"","headRefOid":"h","baseRefOid":"b"}`), nil
		default:
			t.Fatalf("unexpected call: %s", command)
			return nil, nil
		}
	}}
	dir := t.TempDir()
	interrupted := errors.New("process interrupted")
	_, err := WatchPRs(context.Background(), dir, dir, "run", []int{1}, gh, func() time.Time { return now }, func(context.Context, time.Duration) error { return interrupted })
	if !errors.Is(err, interrupted) {
		t.Fatal(err)
	}
	var saved PRObservation
	data, _ := os.ReadFile(filepath.Join(dir, "verification", "pr-1.json"))
	json.Unmarshal(data, &saved)
	if !saved.Window.Started.Equal(start) {
		t.Fatal(saved)
	}
	now = start.Add(CheckDiscovery)
	observations, err := WatchPRs(context.Background(), dir, dir, "run", []int{1}, gh, func() time.Time { return now }, func(context.Context, time.Duration) error { t.Fatal("slept after persisted grace"); return nil })
	if err != nil || observations[0].CI != "pass" || observations[0].Reason != "no_checks" {
		t.Fatal(observations, err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".heartbeat")); err != nil {
		t.Fatal(err)
	}
	gh.Run = func(string, ...string) ([]byte, error) { return nil, errors.New("permission denied") }
	failed := gh.Observe(dir, 1, saved.Window, now)
	if failed.CI != "unknown" || failed.Reason == "no_checks" {
		t.Fatal(failed)
	}
	if calls == 0 {
		t.Fatal("no GitHub observations")
	}
}
