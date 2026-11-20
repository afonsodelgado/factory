package host

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"
)

const (
	GHEvery   = 60 * time.Second
	ghBackoff = 5 * time.Minute
)

type PR struct {
	Number  int
	Head    string
	Base    string
	Title   string
	Draft   bool
	URL     string
	CI      string
	Done    int
	Total   int
	Loaded  bool
	Problem string
}

type GitHub struct {
	Run func(dir string, args ...string) ([]byte, error)

	mu       sync.Mutex
	cache    map[string]cachedPR
	pausedTo time.Time
	last     time.Time
}

type cachedPR struct {
	pr PR
	at time.Time
}

func RunGH(dir string, args ...string) ([]byte, error) {
	cmd := exec.Command("gh", args...)
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return out, fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()+"\n"+string(out)))
	}
	return out, nil
}

type prJSON struct {
	Head              string  `json:"headRefOid"`
	Base              string  `json:"baseRefOid"`
	Number            int     `json:"number"`
	Title             string  `json:"title"`
	IsDraft           bool    `json:"isDraft"`
	URL               string  `json:"url"`
	StatusCheckRollup []check `json:"statusCheckRollup"`
}

type check struct {
	Name         string    `json:"name"`
	WorkflowName string    `json:"workflowName"`
	Context      string    `json:"context"`
	Status       string    `json:"status"`
	Conclusion   string    `json:"conclusion"`
	State        string    `json:"state"`
	StartedAt    time.Time `json:"startedAt"`
	CreatedAt    time.Time `json:"createdAt"`
}

func (c check) key() string {
	return c.WorkflowName + "\x00" + c.Name + "\x00" + c.Context
}

func (c check) at() time.Time {
	if c.StartedAt.IsZero() {
		return c.CreatedAt
	}
	return c.StartedAt
}

func latestRuns(checks []check) []check {
	index := map[string]int{}
	var latest []check
	for _, c := range checks {
		i, seen := index[c.key()]
		switch {
		case !seen:
			index[c.key()] = len(latest)
			latest = append(latest, c)
		case c.at().After(latest[i].at()):
			latest[i] = c
		}
	}
	return latest
}

func (g *GitHub) Checked() time.Time {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.last
}

func (g *GitHub) Statuses(dir string, numbers []int, now time.Time, force bool) []PR {
	g.mu.Lock()
	if g.cache == nil {
		g.cache = map[string]cachedPR{}
	}
	g.mu.Unlock()
	prs := make([]PR, len(numbers))
	for i, n := range numbers {
		key := fmt.Sprintf("%s#%d", dir, n)
		g.mu.Lock()
		cached, ok := g.cache[key]
		paused := now.Before(g.pausedTo)
		g.mu.Unlock()
		fresh := ok && now.Sub(cached.at) < GHEvery && !force
		if fresh || paused || dir == "" {
			prs[i] = cached.pr
			prs[i].Number = n
			continue
		}
		pr := g.fetch(dir, n, now)
		g.mu.Lock()
		g.cache[key] = cachedPR{pr: pr, at: now}
		g.mu.Unlock()
		prs[i] = pr
	}
	return prs
}

func (g *GitHub) fetch(dir string, n int, now time.Time) PR {
	out, err := g.Run(dir, "pr", "view", fmt.Sprint(n), "--json", "number,title,isDraft,url,statusCheckRollup,headRefOid,baseRefOid")
	if err != nil {
		if rateLimited(err.Error()) {
			g.mu.Lock()
			g.pausedTo = now.Add(ghBackoff)
			g.mu.Unlock()
		}
		return PR{Number: n, Problem: "gh failed"}
	}
	var raw prJSON
	if err := json.Unmarshal(out, &raw); err != nil {
		return PR{Number: n, Problem: "unreadable gh output"}
	}
	g.mu.Lock()
	g.last = now
	g.mu.Unlock()
	checks := latestRuns(raw.StatusCheckRollup)
	pr := PR{Head: raw.Head, Base: raw.Base, Number: raw.Number, Title: raw.Title, Draft: raw.IsDraft, URL: raw.URL, Loaded: true, Total: len(checks)}
	apiChecks := make([]apiCheck, 0, len(checks))
	for _, c := range checks {
		apiChecks = append(apiChecks, apiCheck{Name: c.Name, Context: c.Context, Status: strings.ToLower(c.Status), State: strings.ToLower(c.State), Conclusion: strings.ToLower(c.Conclusion)})
		if c.Status == "COMPLETED" || c.State == "SUCCESS" || c.State == "FAILURE" || c.State == "ERROR" {
			pr.Done++
		}
	}
	pr.CI = "none"
	if len(apiChecks) > 0 {
		var reason string
		pr.CI, reason, _ = interpretChecks(apiChecks, nil, CheckDiscovery)
		if reason == "all_checks_skipped" {
			pr.CI = "pass"
		}
	}

	return pr
}

func failing(conclusion string) bool {
	switch conclusion {
	case "FAILURE", "CANCELLED", "TIMED_OUT", "ACTION_REQUIRED", "STARTUP_FAILURE":
		return true
	}
	return false
}

func rateLimited(message string) bool {
	lower := strings.ToLower(message)
	return strings.Contains(lower, "403") || strings.Contains(lower, "rate limit")
}

func (m *Runtime) PollPRs(force bool) {
	if m.GitHub == nil || m.Jobs == nil || (!force && m.Now().Sub(m.prsPolled) < GHEvery) {
		return
	}
	now, gh := m.Now(), m.GitHub
	m.prsPolled = now
	type lookup struct {
		key, dir string
		numbers  []int
	}
	var lookups []lookup
	for _, it := range m.snap.Items {
		f := it.Factory
		if f.State == nil || len(f.State.PRs) == 0 || f.Archived() {
			continue
		}
		l := lookup{key: f.Key(), dir: f.Worktree}
		if !DirExists(l.dir) {
			l.dir = f.Repo
		}
		for _, pr := range f.State.PRs {
			l.numbers = append(l.numbers, pr.Number)
		}
		lookups = append(lookups, l)
	}
	m.Jobs.Start("prs", func(ctx context.Context) Message {
		statuses := map[string][]PR{}
		for _, l := range lookups {
			if ctx.Err() != nil {
				break
			}
			statuses[l.key] = gh.Statuses(l.dir, l.numbers, now, force)
		}
		data, _ := json.Marshal(statuses)
		return Message{T: "prs", Data: data}
	})
}

func (m *Runtime) prsChecked() time.Time {
	if m.GitHub == nil {
		return time.Time{}
	}
	return m.GitHub.Checked()
}
