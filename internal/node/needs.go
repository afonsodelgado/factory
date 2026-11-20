package node

import (
	"cmp"
	"context"
	"net/http"
	"time"

	"github.com/afonsodelgado/factory/internal/gitclone"
)

const (
	NeedsRoute   = "/api/node/needs"
	NeedTool     = "tool"
	NeedFile     = "file"
	NeedVariable = "variable"
)

type Need struct {
	Name    string `json:"name"`
	Kind    string `json:"kind"`
	Why     string `json:"why"`
	Version string `json:"version,omitempty"`
}

type RepoNeeds struct {
	URL   string `json:"url"`
	Needs []Need `json:"needs"`
}

type NeedsRequest struct {
	Repos []RepoNeeds `json:"repos"`
}

type RepoReport struct {
	URL    string  `json:"url"`
	Checks []Check `json:"checks"`
}

func (r RepoReport) Missing() []Check {
	var missing []Check
	for _, c := range r.Checks {
		if c.Mark == MarkFail {
			missing = append(missing, c)
		}
	}
	return missing
}

type NeedsReport struct {
	Node  string       `json:"node"`
	At    time.Time    `json:"at"`
	Repos []RepoReport `json:"repos"`
}

type NeedsCheck func(ctx context.Context, on, repo, checkout string, needs []Need) []Check

func (n *Nodes) needsRoute(w http.ResponseWriter, r *http.Request, _ string) {
	var body NeedsRequest
	if err := decode(r, &body); err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	reply(w, http.StatusOK, n.CheckNeeds(r.Context(), body))
}

func (n *Nodes) CheckNeeds(ctx context.Context, body NeedsRequest) NeedsReport {
	report := NeedsReport{Node: n.Self.Name, At: n.now()}
	if n.Needs == nil {
		return report
	}
	on, checkouts := cmp.Or(n.Self.short(), "this node"), n.repos()
	for _, repo := range body.Repos {
		checkout, added := gitclone.Find(checkouts, repo.URL)
		if !added || gitclone.CheckURL(repo.URL) != nil {
			continue
		}
		checks := n.Needs(ctx, on, RepoName(repo.URL), checkout, repo.Needs)
		report.Repos = append(report.Repos, RepoReport{URL: repo.URL, Checks: checks})
	}
	return report
}
