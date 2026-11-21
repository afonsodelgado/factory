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
	classic := func(required string) *GitHub {
		g := gh(blocked, `[[]]`, "true")
		answer := g.Run
		g.Run = func(dir string, args ...string) ([]byte, error) {
			command := strings.Join(args, " ")
			switch {
			case strings.Contains(command, "/protection"):
				t.Fatal("the watcher asked for branch protection, which needs admin access")
			case strings.Contains(command, "branches/main") && !strings.Contains(command, "rules/"):
				return []byte(`{"protected":true,"commit":{"sha":"b"},"protection":{"enabled":true,"required_status_checks":{"contexts":["` + required + `"],"checks":[]}}}`), nil
			}
			return answer(dir, args...)
		}
		return g
	}
	if o := classic("ci").Observe("", 1, CheckWindow{}, now); o.CI != "pass" || o.Policy != "pass" || !o.ApprovalNeeded {
		t.Fatal("a push-only token could not verify green CI on a protected base waiting for approval", o)
	}
	if o := classic("deploy").Observe("", 1, CheckWindow{}, now); o.CI == "pass" {
		t.Fatal("a required check from the base branch's protection was ignored", o)
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
			return []byte(`{"protected":false,"commit":{"sha":"trunk-moved-on"}}`), nil
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
	clock, slept := now, 0
	tick := func(context.Context, time.Duration) error {
		slept++
		clock = clock.Add(GHEvery)
		return nil
	}
	gaveUp, err := WatchPRs(context.Background(), dir, dir, "run", []int{1}, gh, func() time.Time { return clock }, tick)
	if err != nil || gaveUp[0].CI != "unknown" || slept < int(ErrorGrace/GHEvery) {
		t.Fatal("the watcher did not retry a GitHub error through its grace before reporting it", slept, gaveUp, err)
	}
	if calls == 0 {
		t.Fatal("no GitHub observations")
	}
}

func TestRunGHKeepsStderrHintsOutOfTheJSON(t *testing.T) {
	bin := t.TempDir()
	script := "#!/bin/sh\necho '{\"number\":7}'\necho 'A new release of gh is available' >&2\n"
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	out, err := RunGH(t.TempDir(), "pr", "view", "7", "--json", "number")
	if err != nil {
		t.Fatal(err)
	}
	var pr struct{ Number int }
	if err := json.Unmarshal(out, &pr); err != nil || pr.Number != 7 {
		t.Fatalf("output %q is not the PR JSON: %v", out, err)
	}
}

func TestAWatchNeverTimesOutFromAWindowStartedInAnEarlierRun(t *testing.T) {
	began := time.Unix(100_000, 0)
	checkRuns := `[{"id":1,"name":"ci","status":"completed","conclusion":"success","head_sha":"h"}]`
	gh := &GitHub{Run: func(dir string, args ...string) ([]byte, error) {
		command := strings.Join(args, " ")
		switch {
		case command == "repo view --json nameWithOwner":
			return []byte(`{"nameWithOwner":"owner/repo"}`), nil
		case strings.Contains(command, "pulls/1"):
			return []byte(`{"number":1,"head":{"sha":"h","ref":"feature"},"base":{"sha":"b","ref":"main"},"state":"open","draft":false,"mergeable":true,"mergeable_state":"clean"}`), nil
		case strings.Contains(command, "rules/branches"):
			return []byte(`[[]]`), nil
		case strings.Contains(command, "branches/main"):
			return []byte(`{"protected":false}`), nil
		case strings.Contains(command, "check-runs"):
			return []byte(`[{"total_count":1,"check_runs":` + checkRuns + `}]`), nil
		case strings.Contains(command, "statuses"):
			return []byte(`[[]]`), nil
		case strings.Contains(command, "reviewDecision"):
			return []byte(`{"reviewDecision":"","headRefOid":"h","baseRefOid":"b"}`), nil
		}
		t.Fatalf("unexpected call: %s", command)
		return nil, nil
	}}
	stored := func(draft bool) string {
		dir := t.TempDir()
		old := began.Add(-3 * time.Hour)
		if err := SavePrivate(filepath.Join(dir, "verification", "pr-1.json"), PRObservation{Window: CheckWindow{Head: "h", Base: "b", Started: old, Discovery: old, Draft: draft}}); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	stop := errors.New("stopped")
	firstObservation := func(dir string) PRObservation {
		if _, err := WatchPRs(context.Background(), dir, dir, "run", []int{1}, gh, func() time.Time { return began }, func(context.Context, time.Duration) error { return stop }); !errors.Is(err, stop) {
			t.Fatal("the watch handed back its first observation instead of waiting", err)
		}
		var saved PRObservation
		data, _ := os.ReadFile(filepath.Join(dir, "verification", "pr-1.json"))
		json.Unmarshal(data, &saved)
		return saved
	}

	dir := stored(true)
	if o := firstObservation(dir); o.CI != "pending" || o.Reason != "discovering_checks" {
		t.Fatal("a PR marked ready since an old draft window did not wait for its ready checks", o)
	}
	settled, err := WatchPRs(context.Background(), dir, dir, "run", []int{1}, gh, func() time.Time { return began.Add(ReadyDiscovery) }, nil)
	if err != nil || settled[0].CI != "pass" {
		t.Fatal("green checks on a PR marked ready after an old draft window did not pass", settled, err)
	}

	checkRuns = `[{"id":1,"name":"ci","status":"in_progress","head_sha":"h"}]`
	if o := firstObservation(stored(false)); o.CI != "pending" {
		t.Fatal("a watch reported its first observation of an old window as", o.CI)
	}

	clock := began
	tick := func(context.Context, time.Duration) error {
		clock = clock.Add(GHEvery)
		return nil
	}
	continuous, err := WatchPRs(context.Background(), t.TempDir(), "", "run", []int{1}, gh, func() time.Time { return clock }, tick)
	if err != nil || continuous[0].CI != "timeout" || clock.Sub(began) < CheckDeadline {
		t.Fatal("one continuous watch never timed out on checks that stayed pending", continuous, err)
	}
}
