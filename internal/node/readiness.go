package node

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/afonsodelgado/factory/internal/host"
)

const (
	ReadyWait = 10 * time.Second
	MarkOK    = "ok"
	MarkWarn  = "warn"
	MarkFail  = "fail"
	MarkSkip  = "skip"
)

type Check struct {
	Name   string    `json:"name"`
	Mark   string    `json:"mark"`
	Detail string    `json:"detail"`
	Fix    string    `json:"fix,omitempty"`
	On     string    `json:"on,omitempty"`
	Lines  []string  `json:"lines,omitempty"`
	At     time.Time `json:"at"`
}

type Probe struct {
	Name string
	Run  func(context.Context) Check
}

type Report struct {
	Node     string    `json:"node"`
	At       time.Time `json:"at"`
	Checks   []Check   `json:"checks"`
	Problems int       `json:"problems"`
}

type Scope struct {
	Repo   string
	Wanted []string
}

type Readiness struct {
	Probes func(Scope) []Probe
	Wait   time.Duration
	Now    func() time.Time
}

func RepoName(gitURL string) string {
	return strings.TrimSuffix(path.Base(strings.TrimRight(gitURL, "/")), ".git")
}

func MissingRepo(gitURL, on string) Check {
	return Check{Name: "repo", Mark: MarkFail, Detail: RepoName(gitURL) + " isn't added on " + on, Fix: "clone it there, then run factory init in it or add it from that node's hub (n → + add a repo…)"}
}

func (r Readiness) now() time.Time {
	if r.Now == nil {
		return time.Now()
	}
	return r.Now()
}

func (r Readiness) Run(ctx context.Context, node string, scope Scope) Report {
	if r.Probes == nil {
		return Report{Node: node, At: r.now()}
	}
	probes := r.Probes(scope)
	checks := make([]Check, len(probes))
	var wg sync.WaitGroup
	for i, p := range probes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			checks[i] = r.within(ctx, p)
		}()
	}
	wg.Wait()
	report := Report{Node: node, At: r.now(), Checks: checks}
	for _, c := range checks {
		if c.Mark == MarkFail {
			report.Problems++
		}
	}
	return report
}

func (r Readiness) within(ctx context.Context, p Probe) Check {
	wait := r.Wait
	if wait == 0 {
		wait = ReadyWait
	}
	ctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	done := make(chan Check, 1)
	go func() { done <- p.Run(ctx) }()
	select {
	case c := <-done:
		c.At = r.now()
		return c
	case <-ctx.Done():
		return Check{Name: p.Name, Mark: MarkFail, Detail: fmt.Sprintf("no result within %s, so this node does not count it as ready", wait), At: r.now()}
	}
}

func (rep Report) Runnable(short string) error {
	if rep.Problems == 0 {
		return nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s can't run factories until %d %s fixed:", short, rep.Problems, plural(rep.Problems, "check is", "checks are"))
	for _, c := range rep.Checks {
		if c.Mark != MarkFail {
			continue
		}
		fmt.Fprintf(&b, "\n  %s: %s", c.Name, c.Detail)
		if c.Fix != "" {
			fmt.Fprintf(&b, "\n    fix on %s  %s", short, c.Fix)
		}
	}
	return fmt.Errorf("%s", b.String())
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

type Activation struct {
	Caller
	Ready  func(ctx context.Context, repo string) error
	Exists func(repo, slug string) bool
}

func (a Activation) Call(ctx context.Context, m host.Message) (host.Message, error) {
	repo, slug, names := creation(m)
	createsAFactory := names && a.Ready != nil && !a.Exists(repo, slug)
	if !createsAFactory {
		return a.Caller.Call(ctx, m)
	}
	if err := a.Ready(ctx, repo); err != nil {
		return host.Message{T: "error", Error: err.Error()}, nil
	}
	return a.Caller.Call(ctx, m)
}

func creation(m host.Message) (repo, slug string, ok bool) {
	var r struct{ Repo, Slug string }
	if m.T != "create" || json.Unmarshal(m.Data, &r) != nil {
		return "", "", false
	}
	return r.Repo, r.Slug, r.Slug != ""
}
