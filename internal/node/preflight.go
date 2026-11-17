package node

import (
	"cmp"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/goccy/go-yaml"

	"github.com/afonsodelgado/factory/internal/gitclone"
	"github.com/afonsodelgado/factory/internal/host"
	"github.com/afonsodelgado/factory/internal/state"
)

const recipeWait = 10 * time.Minute

type PreflightRequest struct {
	Operation   string `json:"operation"`
	Source      string `json:"source"`
	Incarnation string `json:"incarnation"`
	Factory     string `json:"factory"`
	Slug        string `json:"slug"`
	GitURL      string `json:"git_url"`
	Branch      string `json:"branch"`
	Config      string `json:"config"`
	Protocol    int    `json:"protocol"`
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
	clone, ok := ts.checkClone(ctx, r, &p)
	if !ok {
		return p
	}
	if err := ts.host(ctx, "can-adopt", host.AdoptRequest{Repo: clone, Slug: r.Slug, Incarnation: r.Incarnation}, nil); err != nil {
		p.refuse(n, "factory", err.Error(), "")
		return p
	}
	report := n.Ready.Run(ctx, n.Self.Name, Scope{Repo: clone})
	p.Checks = append(p.Checks, report.Checks...)
	if err := report.Runnable(cmp.Or(n.Self.short(), "this node")); err != nil {
		p.Refusal = err.Error()
		return p
	}
	ts.checkRecipe(ctx, clone, r, &p)
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

func (ts *Transfers) checkClone(ctx context.Context, r PreflightRequest, p *Preflight) (string, bool) {
	n := ts.Nodes
	if err := gitclone.CheckURL(r.GitURL); err != nil {
		p.refuse(n, "clone", fmt.Sprintf("this node clones only network git remotes: %v", err), "")
		return "", false
	}
	ctx, cancel := context.WithTimeout(ctx, gitclone.Timeout)
	defer cancel()
	clone, err := ts.repoFor(ctx, r.GitURL)
	if err != nil {
		p.refuse(n, "clone", fmt.Sprintf("this node cannot clone %s with its own git credentials: %v", r.GitURL, err), "git clone "+r.GitURL)
		return "", false
	}
	p.pass(n, "clone", r.GitURL+" in "+clone)
	return clone, true
}

func (ts *Transfers) checkRecipe(ctx context.Context, clone string, r PreflightRequest, p *Preflight) {
	var profile state.Profile
	if err := yaml.Unmarshal([]byte(r.Config), &profile); err != nil {
		p.refuse(ts.Nodes, "stack", "the factory's repo config does not parse: "+err.Error(), "")
		return
	}
	if !profile.BrowserQA() {
		p.pass(ts.Nodes, "stack", "terminal QA needs no stack")
		return
	}
	ctx, cancel := context.WithTimeout(ctx, recipeWait)
	defer cancel()
	if err := tryRecipe(ctx, clone, r.Branch, profile); err != nil {
		p.refuse(ts.Nodes, "stack", fmt.Sprintf("the repo's stack recipe failed here at %s: %v", failureOf(err), err), "")
		return
	}
	p.pass(ts.Nodes, "stack", "qa.start, qa.health, qa.seed and qa.sign_in ran in a scratch checkout")
}

func tryRecipe(ctx context.Context, clone, branch string, profile state.Profile) error {
	scratch, err := os.MkdirTemp("", "factory-preflight-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(scratch)
	checkout := filepath.Join(scratch, "checkout")
	revision := "origin/" + branch
	if _, err := gitclone.Git(ctx, clone, "rev-parse", "--verify", revision); err != nil {
		revision = "HEAD"
	}
	if _, err := gitclone.Git(ctx, clone, "worktree", "add", "--detach", checkout, revision); err != nil {
		return failed(host.FailStack, err)
	}
	defer gitclone.Git(context.Background(), clone, "worktree", "remove", "--force", checkout)
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
