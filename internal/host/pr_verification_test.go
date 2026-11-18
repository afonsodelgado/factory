package host

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
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

func TestApprovalOnlyBlocksAreNotesAndUncomputedMergeabilityKeepsWaiting(t *testing.T) {
	gh := func(merge, rules, resolved string) *GitHub {
		return &GitHub{Run: func(dir string, args ...string) ([]byte, error) {
			command := strings.Join(args, " ")
			switch {
			case strings.Contains(command, "graphql"):
				return []byte(`[{"data":{"repository":{"pullRequest":{"reviewThreads":{"nodes":[{"isResolved":` + resolved + `}],"pageInfo":{"hasNextPage":false}}}}}}]`), nil
			case command == "repo view --json nameWithOwner":
				return []byte(`{"nameWithOwner":"owner/repo"}`), nil
			case strings.Contains(command, "pulls/1"):
				return []byte(`{"number":1,"head":{"sha":"h","ref":"feature"},"base":{"sha":"b","ref":"main"},"state":"open","draft":false,` + merge + `}`), nil
			case strings.Contains(command, "rules/branches"):
				return []byte(rules), nil
			case strings.Contains(command, "branches/main"):
				return []byte(`{"protected":false,"commit":{"sha":"b"}}`), nil
			case strings.Contains(command, "check-runs"):
				return []byte(`[{"total_count":1,"check_runs":[{"id":1,"name":"ci","status":"completed","conclusion":"success","head_sha":"h"}]}]`), nil
			case strings.Contains(command, "statuses"):
				return []byte(`[[]]`), nil
			case strings.Contains(command, "reviewDecision"):
				return []byte(`{"reviewDecision":"REVIEW_REQUIRED","headRefOid":"h","baseRefOid":"b"}`), nil
			}
			t.Fatalf("unexpected call: %s", command)
			return nil, nil
		}}
	}
	blocked := `"mergeable":true,"mergeable_state":"blocked"`
	strict := `[[{"type":"pull_request","parameters":{"required_approving_review_count":1,"required_review_thread_resolution":true}},{"type":"required_linear_history","parameters":{}}]]`
	now := time.Unix(100, 0)
	if o := gh(blocked, strict, "true").Observe("", 1, CheckWindow{}, now); o.Policy != "pass" || !o.ApprovalNeeded {
		t.Fatal("a missing approval under thread resolution and linear history became a policy failure", o)
	}
	if o := gh(blocked, strict, "false").Observe("", 1, CheckWindow{}, now); o.Policy != "fail" {
		t.Fatal("an unresolved review thread passed as an approval-only block", o)
	}
	unreachable := gh(blocked, strict, "true")
	answer := unreachable.Run
	unreachable.Run = func(dir string, args ...string) ([]byte, error) {
		if slices.Contains(args, "graphql") {
			return nil, errors.New("graphql: rate limited")
		}
		return answer(dir, args...)
	}
	if o := unreachable.Observe("", 1, CheckWindow{}, now); o.Policy != "unknown" || o.Reason == "checks_passed" {
		t.Fatal("a failed thread query still reported its checks as passed", o)
	}
	computing := gh(`"mergeable":null,"mergeable_state":"unknown"`, `[[]]`, "true")
	dir := t.TempDir()
	interrupted := errors.New("slept")
	if _, err := WatchPRs(context.Background(), dir, dir, "run", []int{1}, computing, func() time.Time { return now }, func(context.Context, time.Duration) error { return interrupted }); !errors.Is(err, interrupted) {
		t.Fatal("the watcher handed back mergeability GitHub had not computed yet", err)
	}
	if o := computing.Observe("", 1, CheckWindow{Head: "h", Base: "b", Started: now, Discovery: now}, now.Add(CheckDeadline)); o.Policy != "unknown" {
		t.Fatal("uncomputed mergeability kept waiting past the deadline", o)
	}
}

func TestWatcherPersistsDiscoveryAcrossRestartAndFailsClosedOnAPIError(t *testing.T) {
	start := time.Unix(100, 0)
	now := start
	calls := 0
	draft := true
	checkRuns := "[]"
	gh := &GitHub{Run: func(dir string, args ...string) ([]byte, error) {
		calls++
		command := strings.Join(args, " ")
		switch {
		case command == "repo view --json nameWithOwner":
			return []byte(`{"nameWithOwner":"owner/repo"}`), nil
		case strings.Contains(command, "pulls/1"):
			return []byte(strings.ReplaceAll(`{"number":1,"head":{"sha":"h","ref":"feature"},"base":{"sha":"b","ref":"main"},"state":"open","draft":DRAFT,"mergeable":true,"mergeable_state":"clean"}`, "DRAFT", fmt.Sprint(draft))), nil
		case strings.Contains(command, "rules/branches"):
			return []byte(`[[]]`), nil
		case strings.Contains(command, "branches/main"):
			return []byte(`{"protected":false,"commit":{"sha":"b"}}`), nil
		case strings.Contains(command, "check-runs"):
			return []byte(`[{"total_count":0,"check_runs":` + checkRuns + `}]`), nil
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
	if _, err := WatchPRs(context.Background(), dir, dir, "run", []int{1}, gh, func() time.Time { return now.Add(time.Second) }, nil); err != nil {
		t.Fatal(err)
	}
	if queued, _ := filepath.Glob(filepath.Join(dir, "inbox", "ci-run-*.json")); len(queued) != 2 {
		t.Fatal("a second watch in the same run did not queue its own observation", queued)
	}
	draft = false
	ready := gh.Observe(dir, 1, observations[0].Window, now)
	if ready.CI != "pending" || ready.Reason != "discovering_checks" || !ready.Window.Started.Equal(start) {
		t.Fatal("ready transition reused empty-check evidence or reset the deadline", ready)
	}
	ready = gh.Observe(dir, 1, ready.Window, start.Add(CheckDeadline))
	if ready.CI != "pass" {
		t.Fatal(ready)
	}
	draft = true
	checkRuns = `[{"id":1,"name":"ci","status":"completed","conclusion":"success","head_sha":"h","started_at":"1970-01-01T00:01:40Z"}]`
	green := gh.Observe(dir, 1, CheckWindow{}, now)
	draft = false
	readyAt := now.Add(time.Second)
	if stale := gh.Observe(dir, 1, green.Window, readyAt); green.CI != "pass" || stale.CI != "pending" {
		t.Fatal("a check from before the PR was marked ready passed it", green, stale)
	}
	marked := gh.Observe(dir, 1, green.Window, readyAt)
	if settled := gh.Observe(dir, 1, marked.Window, readyAt.Add(ReadyDiscovery)); settled.CI != "pass" {
		t.Fatal("green checks did not pass once the ready grace ended", settled)
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
