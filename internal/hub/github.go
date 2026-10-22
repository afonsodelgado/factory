package hub

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/afonsodelgado/factory/internal/ui/panel"
)

const (
	ghEvery   = 60 * time.Second
	ghBackoff = 5 * time.Minute
)

type GitHub struct {
	Run func(dir string, args ...string) ([]byte, error)

	mu       sync.Mutex
	cache    map[string]cachedPR
	pausedTo time.Time
	last     time.Time
}

type cachedPR struct {
	pr panel.PR
	at time.Time
}

func RunGH(dir string, args ...string) ([]byte, error) {
	cmd := exec.Command("gh", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return out, fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	return out, nil
}

type prJSON struct {
	Number            int    `json:"number"`
	Title             string `json:"title"`
	IsDraft           bool   `json:"isDraft"`
	URL               string `json:"url"`
	StatusCheckRollup []struct {
		Status     string `json:"status"`
		Conclusion string `json:"conclusion"`
		State      string `json:"state"`
	} `json:"statusCheckRollup"`
}

func (g *GitHub) Checked() time.Time {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.last
}

func (g *GitHub) Statuses(dir string, numbers []int, now time.Time, force bool) []panel.PR {
	g.mu.Lock()
	if g.cache == nil {
		g.cache = map[string]cachedPR{}
	}
	g.mu.Unlock()
	prs := make([]panel.PR, len(numbers))
	for i, n := range numbers {
		key := fmt.Sprintf("%s#%d", dir, n)
		g.mu.Lock()
		cached, ok := g.cache[key]
		paused := now.Before(g.pausedTo)
		g.mu.Unlock()
		fresh := ok && now.Sub(cached.at) < ghEvery && !force
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

func (g *GitHub) fetch(dir string, n int, now time.Time) panel.PR {
	out, err := g.Run(dir, "pr", "view", fmt.Sprint(n), "--json", "number,title,isDraft,url,statusCheckRollup")
	if err != nil {
		if rateLimited(err.Error()) {
			g.mu.Lock()
			g.pausedTo = now.Add(ghBackoff)
			g.mu.Unlock()
		}
		return panel.PR{Number: n, Problem: "gh failed"}
	}
	var raw prJSON
	if err := json.Unmarshal(out, &raw); err != nil {
		return panel.PR{Number: n, Problem: "unreadable gh output"}
	}
	g.mu.Lock()
	g.last = now
	g.mu.Unlock()
	pr := panel.PR{Number: raw.Number, Title: raw.Title, Draft: raw.IsDraft, URL: raw.URL, Loaded: true, Total: len(raw.StatusCheckRollup)}
	pr.CI = "none"
	failed, pending := false, false
	for _, c := range raw.StatusCheckRollup {
		switch {
		case c.State == "FAILURE" || c.State == "ERROR" || failing(c.Conclusion):
			failed = true
			pr.Done++
		case c.State == "PENDING" || c.State == "EXPECTED" || (c.State == "" && c.Status != "COMPLETED"):
			pending = true
		default:
			pr.Done++
		}
	}
	switch {
	case pr.Total == 0:
	case failed:
		pr.CI = "fail"
	case pending:
		pr.CI = "pending"
	default:
		pr.CI = "pass"
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
