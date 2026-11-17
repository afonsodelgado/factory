package host

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/afonsodelgado/factory/internal/factory"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/afonsodelgado/factory/internal/state"
)

const CheckDiscovery = 15 * time.Minute
const CheckDeadline = 2 * time.Hour

type CheckWindow struct {
	Head      string    `json:"head"`
	Base      string    `json:"base"`
	Started   time.Time `json:"started"`
	Discovery time.Time `json:"discovery"`
	Draft     bool      `json:"draft"`
}

type PRObservation struct {
	state.PRVerification
	Branch  string      `json:"branch"`
	Parent  string      `json:"parent"`
	URL     string      `json:"url"`
	Title   string      `json:"title"`
	Problem string      `json:"problem,omitempty"`
	Notes   []string    `json:"notes,omitempty"`
	Window  CheckWindow `json:"window"`
}

type requiredCheck struct {
	Context       string `json:"context"`
	AppID         int    `json:"app_id"`
	IntegrationID int    `json:"integration_id"`
}

type apiCheck struct {
	ID         int64     `json:"id"`
	Name       string    `json:"name"`
	Context    string    `json:"context"`
	Status     string    `json:"status"`
	State      string    `json:"state"`
	Conclusion string    `json:"conclusion"`
	HeadSHA    string    `json:"head_sha"`
	CreatedAt  time.Time `json:"created_at"`
	StartedAt  time.Time `json:"started_at"`
	App        struct {
		ID int `json:"id"`
	} `json:"app"`
}

func (g *GitHub) api(dir, path string, target any, paginate bool) error {
	args := []string{"api", path}
	if paginate {
		args = append(args, "--paginate", "--slurp")
	}
	data, err := g.Run(dir, args...)
	if err != nil {
		return err
	}
	if string(data) == "null" {
		return errors.New("incomplete GitHub response")
	}
	return json.Unmarshal(data, target)
}

func (g *GitHub) Observe(dir string, number int, previous CheckWindow, now time.Time) PRObservation {
	o := PRObservation{PRVerification: state.PRVerification{Number: number, CI: "unknown", Policy: "unknown", At: state.Time{Time: now}}, Window: previous}
	err := g.observe(dir, &o, now)
	if err != nil {
		o.CI, o.Policy, o.Problem = "unknown", "unknown", err.Error()
	}
	return o
}

func (g *GitHub) observe(dir string, o *PRObservation, now time.Time) error {
	var repo struct {
		Name string `json:"nameWithOwner"`
	}
	data, err := g.Run(dir, "repo", "view", "--json", "nameWithOwner")
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, &repo); err != nil {
		return err
	}
	if len(strings.Split(repo.Name, "/")) != 2 {
		return errors.New("repository identity missing")
	}
	prefix := "repos/" + repo.Name
	type ref struct {
		SHA string `json:"sha"`
		Ref string `json:"ref"`
	}
	var pr struct {
		Number     int    `json:"number"`
		Head       ref    `json:"head"`
		Base       ref    `json:"base"`
		State      string `json:"state"`
		Draft      bool   `json:"draft"`
		Mergeable  *bool  `json:"mergeable"`
		MergeState string `json:"mergeable_state"`
		MergeSHA   string `json:"merge_commit_sha"`
		URL        string `json:"html_url"`
		Title      string `json:"title"`
	}
	path := fmt.Sprintf("%s/pulls/%d", prefix, o.Number)
	if err := g.api(dir, path, &pr, false); err != nil {
		return err
	}
	if pr.Number != o.Number || pr.Head.SHA == "" || pr.Base.SHA == "" || pr.Base.Ref == "" {
		return errors.New("incomplete PR identity")
	}
	o.Head, o.Base, o.Branch, o.Parent, o.URL, o.Title = pr.Head.SHA, pr.Base.SHA, pr.Head.Ref, pr.Base.Ref, pr.URL, pr.Title
	o.Open, o.Draft = pr.State == "open", pr.Draft
	if o.Window.Head != o.Head || o.Window.Base != o.Base || o.Window.Started.IsZero() {
		o.Window = CheckWindow{Head: o.Head, Base: o.Base, Started: now, Discovery: now, Draft: pr.Draft}
	}
	if o.Window.Draft != pr.Draft {
		o.Window.Discovery, o.Window.Draft = now, pr.Draft
	}
	if o.Window.Discovery.IsZero() {
		o.Window.Discovery = o.Window.Started
	}
	var branch struct {
		Protected bool `json:"protected"`
		Commit    struct {
			SHA string `json:"sha"`
		} `json:"commit"`
	}
	if err := g.api(dir, prefix+"/branches/"+url.PathEscape(pr.Base.Ref), &branch, false); err != nil {
		return err
	}
	if branch.Commit.SHA != o.Base {
		return errors.New("base moved during verification")
	}
	var rules [][]struct {
		Type       string `json:"type"`
		Parameters struct {
			Required  []requiredCheck `json:"required_status_checks"`
			Approvals int             `json:"required_approving_review_count"`
			Threads   bool            `json:"required_review_thread_resolution"`
		} `json:"parameters"`
	}
	if err := g.api(dir, prefix+"/rules/branches/"+url.PathEscape(pr.Base.Ref), &rules, true); err != nil {
		return err
	}
	if len(rules) == 0 {
		return errors.New("incomplete rules response")
	}
	required := []requiredCheck{}
	approvals, threads, otherPolicy := false, false, false
	for _, page := range rules {
		for _, rule := range page {
			required = append(required, rule.Parameters.Required...)
			approvals = approvals || rule.Parameters.Approvals > 0
			threads = threads || rule.Parameters.Threads
			if rule.Type != "required_status_checks" && rule.Type != "pull_request" && rule.Type != "non_fast_forward" && rule.Type != "deletion" {
				otherPolicy = true
			}
		}
	}
	if branch.Protected {
		var protection struct {
			Status struct {
				Contexts []string        `json:"contexts"`
				Checks   []requiredCheck `json:"checks"`
			} `json:"required_status_checks"`
			Reviews *struct {
				Count  int  `json:"required_approving_review_count"`
				Owners bool `json:"require_code_owner_reviews"`
				Last   bool `json:"require_last_push_approval"`
			} `json:"required_pull_request_reviews"`
			Threads struct {
				Enabled bool `json:"enabled"`
			} `json:"required_conversation_resolution"`
			Lock struct {
				Enabled bool `json:"enabled"`
			} `json:"lock_branch"`
		}
		if err := g.api(dir, prefix+"/branches/"+url.PathEscape(pr.Base.Ref)+"/protection", &protection, false); err != nil {
			return err
		}
		required = append(required, protection.Status.Checks...)
		for _, name := range protection.Status.Contexts {
			if !slices.ContainsFunc(required, func(c requiredCheck) bool { return c.Context == name }) {
				required = append(required, requiredCheck{Context: name})
			}
		}
		approvals = approvals || protection.Reviews != nil && (protection.Reviews.Count > 0 || protection.Reviews.Owners || protection.Reviews.Last)
		threads = threads || protection.Threads.Enabled
		otherPolicy = otherPolicy || protection.Lock.Enabled
	}
	checks, err := g.checksAt(dir, prefix, o.Head)
	if err != nil {
		return err
	}
	if pr.MergeSHA != "" && pr.MergeSHA != o.Head && pr.Mergeable != nil && *pr.Mergeable {
		merged, err := g.checksAt(dir, prefix, pr.MergeSHA)
		if err != nil {
			return err
		}
		if len(merged) > 0 {
			checks = append(checks, merged...)
		}
	}
	o.CI, o.Reason, o.Notes = interpretChecks(checks, required, now.Sub(o.Window.Discovery))
	var review struct {
		ReviewDecision string `json:"reviewDecision"`
		Head           string `json:"headRefOid"`
		Base           string `json:"baseRefOid"`
	}
	data, err = g.Run(dir, "pr", "view", fmt.Sprint(o.Number), "--json", "reviewDecision,headRefOid,baseRefOid")
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, &review); err != nil {
		return err
	}
	if review.Head != o.Head || review.Base != o.Base {
		return errors.New("heads changed during verification")
	}
	o.ApprovalNeeded = approvals && review.ReviewDecision != "APPROVED"
	o.Policy = "fail"
	switch {
	case !o.Open:
		o.Problem = "PR is not open"
	case pr.Mergeable == nil:
		o.Problem = "mergeability is unknown"
	case !*pr.Mergeable:
		o.Problem = "merge conflicts"
	case review.ReviewDecision == "CHANGES_REQUESTED":
		o.Problem = "review changes requested"
	case pr.MergeState == "behind":
		o.Problem = "base requires restacking"
	case pr.MergeState == "clean" || pr.MergeState == "unstable":
		o.Policy = "pass"
	case pr.MergeState == "blocked" && o.ApprovalNeeded && !threads && !otherPolicy && o.CI == "pass":
		o.Policy = "pass"
	case pr.Draft && pr.MergeState == "draft":
		o.Policy = "pass"
	default:
		o.Problem = "repository policy is not satisfied: " + pr.MergeState
	}
	if now.Sub(o.Window.Started) >= CheckDeadline && o.CI == "pending" {
		o.CI, o.Reason = "timeout", "verification_deadline"
	}
	return nil
}

func (g *GitHub) checksAt(dir, prefix, sha string) ([]apiCheck, error) {
	var pages []struct {
		Total  int        `json:"total_count"`
		Checks []apiCheck `json:"check_runs"`
	}
	if err := g.api(dir, prefix+"/commits/"+sha+"/check-runs?filter=latest&per_page=100", &pages, true); err != nil {
		return nil, err
	}
	if len(pages) == 0 {
		return nil, errors.New("incomplete check pages")
	}
	checks := []apiCheck{}
	for _, page := range pages {
		if page.Checks == nil {
			return nil, errors.New("missing check_runs")
		}
		checks = append(checks, page.Checks...)
	}
	if len(checks) < pages[0].Total {
		return nil, errors.New("truncated check response")
	}
	for _, c := range checks {
		if c.HeadSHA != sha {
			return nil, errors.New("check belongs to another head")
		}
	}
	var statuses [][]apiCheck
	if err := g.api(dir, prefix+"/commits/"+sha+"/statuses?per_page=100", &statuses, true); err != nil {
		return nil, err
	}
	if len(statuses) == 0 {
		return nil, errors.New("incomplete status pages")
	}
	for _, page := range statuses {
		for _, c := range page {
			c.HeadSHA = sha
			checks = append(checks, c)
		}
	}
	latest := map[string]apiCheck{}
	for _, c := range checks {
		key := fmt.Sprintf("%s:%s:%s:%d", c.HeadSHA, c.Name, c.Context, c.App.ID)
		old, ok := latest[key]
		if !ok || c.ID > old.ID {
			latest[key] = c
		}
	}
	checks = nil
	for _, c := range latest {
		checks = append(checks, c)
	}
	return checks, nil
}

func interpretChecks(checks []apiCheck, required []requiredCheck, elapsed time.Duration) (string, string, []string) {
	var notes []string
	pending, success := false, false
	for _, c := range checks {
		switch {
		case strings.EqualFold(c.State, "failure") || strings.EqualFold(c.State, "error") || failing(strings.ToUpper(c.Conclusion)) || c.Conclusion == "stale":
			return "fail", "failed_check", notes
		case c.State == "success" || c.Conclusion == "success":
			success = true
		case c.Conclusion == "skipped" || c.Conclusion == "neutral":
			notes = append(notes, c.Name+": "+c.Conclusion)
		default:
			pending = true
		}
	}
	for _, r := range required {
		found := slices.ContainsFunc(checks, func(c apiCheck) bool {
			app := r.AppID
			if r.IntegrationID != 0 {
				app = r.IntegrationID
			}
			return (c.Name == r.Context || c.Context == r.Context) && (app <= 0 || c.App.ID == app) && (c.State == "success" || c.Conclusion == "success" || c.Conclusion == "neutral" || c.Conclusion == "skipped")
		})
		if !found {
			return "pending", "missing_required_check", notes
		}
	}
	if pending {
		return "pending", "checks_running", notes
	}
	if len(checks) == 0 {
		if elapsed < CheckDiscovery {
			return "pending", "discovering_checks", notes
		}
		return "pass", "no_checks", notes
	}
	if !success {
		return "fail", "all_checks_skipped", notes
	}
	return "pass", "checks_passed", notes
}

func WatchPRs(ctx context.Context, dir, worktree, runID string, numbers []int, g *GitHub, now func() time.Time, sleep func(context.Context, time.Duration) error) ([]PRObservation, error) {
	if dir == "" || runID == "" || len(numbers) == 0 {
		return nil, errors.New("watcher requires factory directory, run id and PRs")
	}
	records := filepath.Join(dir, "verification")
	if err := os.MkdirAll(records, 0700); err != nil {
		return nil, err
	}
	for {
		at := now()
		if err := os.WriteFile(filepath.Join(dir, ".heartbeat"), []byte(at.UTC().Format(time.RFC3339)), 0600); err != nil {
			return nil, err
		}
		observations := []PRObservation{}
		waiting := false
		for _, number := range numbers {
			path := filepath.Join(records, fmt.Sprintf("pr-%d.json", number))
			var previous PRObservation
			data, err := os.ReadFile(path)
			if err == nil {
				if err := json.Unmarshal(data, &previous); err != nil {
					return nil, err
				}
			} else if !os.IsNotExist(err) {
				return nil, err
			}
			observation := g.Observe(worktree, number, previous.Window, at)
			if err := SavePrivate(path, observation); err != nil {
				return nil, err
			}
			observations = append(observations, observation)
			waiting = waiting || observation.CI == "pending"
			if observation.CI == "unknown" || observation.CI == "fail" || observation.CI == "timeout" {
				waiting = false
				break
			}
		}
		if !waiting {
			if err := SavePrivate(filepath.Join(dir, "inbox", "ci-"+runID+".json"), map[string]any{"run_id": runID, "observations": observations, "text": "CI observation ready; validate the current run and head/base tuples."}); err != nil {
				return nil, err
			}
			return observations, nil
		}
		if err := sleep(ctx, GHEvery); err != nil {
			return nil, err
		}
	}
}

func (m *Runtime) verificationSnapshot() Snapshot {
	snap := m.snap
	snap.Items = slices.Clone(snap.Items)
	for i, it := range snap.Items {
		if it.Factory.State == nil || it.Factory.State.Confirmation == nil || it.Factory.Archived() {
			continue
		}
		current := m.prs[it.Factory.Key()]
		pending := len(current) != len(it.Factory.State.PRs)
		for _, p := range it.Factory.State.PRs {
			j := slices.IndexFunc(current, func(v PR) bool { return v.Number == p.Number })
			pending = pending || j < 0
			if j >= 0 {
				v := current[j]
				pending = pending || !v.Loaded || v.Problem != "" || v.Head != p.HeadSHA || v.Base != p.BaseSHA
			}
		}
		if pending {
			snap.Items[i].Status = factory.VerificationPending
			snap.Items[i].Unblocked = factory.VerificationPending
		}
	}
	return snap
}

func (m *Runtime) wakeChangedHeads() {
	for _, it := range m.snap.Items {
		f := it.Factory
		if f.State == nil || f.State.Confirmation == nil || f.Archived() || Paused(f) || m.unverified(f.Key()) != nil {
			continue
		}
		current := m.prs[f.Key()]
		changed := false
		for _, p := range f.State.PRs {
			for _, v := range current {
				if v.Number == p.Number && v.Loaded && v.Head != "" && v.Base != "" && (v.Head != p.HeadSHA || v.Base != p.BaseSHA) {
					changed = true
				}
			}
		}
		if !changed {
			continue
		}
		data, _ := json.Marshal(current)
		digest := sha256.Sum256(data)
		key := fmt.Sprintf("heads-%x", digest[:12])
		path := filepath.Join(f.Dir, "inbox", key+".json")
		if _, err := os.Stat(path); err == nil {
			continue
		}
		if err := SavePrivate(path, map[string]any{"text": "Published heads or bases changed. Commit workflow invalidation and re-run PR verification before completion.", "prs": current}); err != nil {
			m.notice = err.Error()
			continue
		}
		if it.Pane == nil || it.Pane.Dead || factory.IsShell(it.Pane.Command) {
			if _, err := m.wakeForHandoff(it); err != nil {
				m.notice = err.Error()
			}
		}
	}
}
