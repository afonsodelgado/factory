package node

import (
	"cmp"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"syscall"
	"time"

	"github.com/afonsodelgado/factory/internal/gitclone"
	"github.com/afonsodelgado/factory/internal/host"
	"github.com/afonsodelgado/factory/internal/recipe"
	"github.com/afonsodelgado/factory/internal/state"
)

const (
	recipeWait = 10 * time.Minute
	qaBlank    = "QA commands are blank on this node; discovered before QA"
)

type PreflightRequest struct {
	Operation   string `json:"operation"`
	Source      string `json:"source"`
	Incarnation string `json:"incarnation"`
	Factory     string `json:"factory"`
	Slug        string `json:"slug"`
	GitURL      string `json:"git_url"`
	Branch      string `json:"branch"`
	Protocol    int    `json:"protocol"`

	IgnoredConfig string `json:"config,omitempty"`
}

type Preflight struct {
	Node    string    `json:"node"`
	Checks  []Check   `json:"checks"`
	Refusal string    `json:"refusal,omitempty"`
	At      time.Time `json:"at"`
}

func (p Preflight) refused() bool { return p.Refusal != "" }

func (p *Preflight) refuse(n *Nodes, name, problem, fix string) {
	p.Refusal = problem
	p.Checks = append(p.Checks, Check{Name: name, Mark: MarkFail, Detail: problem, Fix: fix, On: n.Self.short(), At: n.now()})
}

func (p *Preflight) pass(n *Nodes, name, detail string) {
	p.Checks = append(p.Checks, Check{Name: name, Mark: MarkOK, Detail: detail, At: n.now()})
}

func (ts *Transfers) Preflight(ctx context.Context, r PreflightRequest) Preflight {
	n := ts.Nodes
	p := Preflight{Node: n.Self.Name, At: n.now()}
	steps := []func(context.Context, PreflightRequest, *Preflight){ts.checkVersion, ts.checkIdentity, ts.checkDisk}
	for _, step := range steps {
		if step(ctx, r, &p); p.refused() {
			return p
		}
	}
	repo, ok := ts.checkRepo(r, &p)
	if !ok {
		return p
	}
	if err := ts.host(ctx, "can-adopt", host.AdoptRequest{Repo: repo, Slug: r.Slug, Incarnation: r.Incarnation}, nil); err != nil {
		p.refuse(n, "factory", err.Error(), "")
		return p
	}
	report := n.Ready.Run(ctx, n.Self.Name, Scope{Repo: repo})
	p.Checks = append(p.Checks, report.Checks...)
	if err := report.Runnable(cmp.Or(n.Self.short(), "this node")); err != nil {
		p.Refusal = err.Error()
		return p
	}
	ts.checkRecipe(ctx, repo, r, &p)
	return p
}

func (ts *Transfers) checkVersion(_ context.Context, r PreflightRequest, p *Preflight) {
	n := ts.Nodes
	newest, oldest := cmp.Or(n.Protocol, Protocol), cmp.Or(n.MinProtocol, MinProtocol)
	if r.Protocol < oldest || r.Protocol > newest {
		p.refuse(n, "node", fmt.Sprintf("this node speaks protocol %d to %d and the factory comes from protocol %d", oldest, newest, r.Protocol), "factory update on both nodes")
		return
	}
	p.pass(n, "node", cmp.Or(n.Version, "factory")+" · protocol "+fmt.Sprint(newest))
}

func (ts *Transfers) checkIdentity(_ context.Context, r PreflightRequest, p *Preflight) {
	if err := validateSlug(r.Slug); err != nil {
		p.refuse(ts.Nodes, "factory", err.Error(), "")
		return
	}
	if ts.Holds != nil && ts.Holds(r.Incarnation) {
		p.refuse(ts.Nodes, "factory", r.Factory+" already runs on this node", "")
	}
}

func (ts *Transfers) checkDisk(_ context.Context, _ PreflightRequest, p *Preflight) {
	if err := ts.room(); err != nil {
		p.refuse(ts.Nodes, "disk", err.Error(), "free disk space on this node")
		return
	}
	p.pass(ts.Nodes, "disk", "room for the checkpoint")
}

func (ts *Transfers) room() error {
	need := uint64(ts.limits().Total) * 2
	free, err := freeBytes(ts.StateDir)
	switch {
	case err != nil:
		return fmt.Errorf("this node cannot tell how much disk it has free: %w", err)
	case free < need:
		return fmt.Errorf("this node has %d MiB free and a move needs %d MiB", free>>20, need>>20)
	}
	return nil
}

func freeBytes(dir string) (uint64, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return 0, err
	}
	var fs syscall.Statfs_t
	if err := syscall.Statfs(dir, &fs); err != nil {
		return 0, err
	}
	return uint64(fs.Bavail) * uint64(fs.Bsize), nil
}

func (ts *Transfers) checkRepo(r PreflightRequest, p *Preflight) (string, bool) {
	repo, check := ts.Nodes.repoCheck(r.GitURL)
	if check.Mark == MarkFail {
		p.refuse(ts.Nodes, check.Name, check.Detail, check.Fix)
		return "", false
	}
	p.pass(ts.Nodes, check.Name, check.Detail)
	return repo, true
}

func (ts *Transfers) checkRecipe(ctx context.Context, repo string, r PreflightRequest, p *Preflight) {
	resolved, err := recipe.Resolve(repo)
	profile := resolved.Profile
	switch {
	case err != nil:
		p.refuse(ts.Nodes, "stack", "this node's repo config does not parse: "+err.Error(), "")
		return
	case !profile.BrowserQA():
		p.pass(ts.Nodes, "stack", "terminal QA needs no stack")
		return
	case undiscovered(profile):
		p.pass(ts.Nodes, "stack", qaBlank)
		return
	}
	ctx, cancel := context.WithTimeout(ctx, recipeWait)
	defer cancel()
	if err := tryRecipe(ctx, repo, r.Branch, profile); err != nil {
		p.refuse(ts.Nodes, "stack", fmt.Sprintf("this node's stack recipe failed at %s: %v", failureOf(err), err), "")
		return
	}
	p.pass(ts.Nodes, "stack", "this node's qa.start, qa.health, qa.seed and qa.sign_in ran in a scratch checkout")
}

func (t Transfer) MissingRepo() (Check, bool) {
	i := slices.IndexFunc(t.Checks, func(c Check) bool { return c.Name == "repo" && c.Mark == MarkFail })
	if i < 0 {
		return Check{}, false
	}
	return t.Checks[i], true
}

func (p Preflight) discoversQA() bool {
	return slices.ContainsFunc(p.Checks, func(c Check) bool { return c.Name == "stack" && c.Detail == qaBlank })
}

func undiscovered(p state.Profile) bool {
	qa := p.QA
	return qa.Start == "" || qa.Health == "" || (qa.URL == "" && qa.WebPort == 0)
}

func tryRecipe(ctx context.Context, repo, branch string, profile state.Profile) error {
	scratch, err := os.MkdirTemp("", "factory-preflight-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(scratch)
	checkout := filepath.Join(scratch, "checkout")
	revision := "origin/" + branch
	if _, err := gitclone.Git(ctx, repo, "rev-parse", "--verify", revision); err != nil {
		revision = "HEAD"
	}
	if _, err := gitclone.Git(ctx, repo, "worktree", "add", "--detach", checkout, revision); err != nil {
		return failed(host.FailStack, err)
	}
	defer gitclone.Git(context.Background(), repo, "worktree", "remove", "--force", checkout)
	recipe, err := RecipeOf(profile, checkout)
	if err != nil {
		return failed(host.FailRecipe, err)
	}
	if recipe, err = recipe.Bind(""); err != nil {
		return err
	}
	stack, err := bringUp(ctx, recipe, func(*stackProcess) {})
	if err != nil {
		return err
	}
	defer stack.stop()
	if _, err := signIn(ctx, recipe); err != nil {
		return failed(host.FailSignIn, err)
	}
	return nil
}
