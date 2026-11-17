package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"os"
	"slices"
	"strings"

	"github.com/charmbracelet/x/term"
	"github.com/google/uuid"

	"github.com/afonsodelgado/factory/internal/factory"
	"github.com/afonsodelgado/factory/internal/host"
	"github.com/afonsodelgado/factory/internal/state"
)

func runDelete(args []string, in *os.File, out io.Writer) int {
	flags := flag.NewFlagSet("delete", flag.ContinueOnError)
	repoFlag := flags.String("repo", "", "the repo that holds the factory")
	yes := flags.Bool("yes", false, "skip the first confirmation; unpushed or uncommitted work still asks")
	if err := flags.Parse(reorder(flags, args)); err != nil {
		return 2
	}
	if flags.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: factory delete <id> [--repo path] [--yes]")
		return 2
	}
	id := flags.Arg(0)
	cfg, err := state.LoadConfig()
	if err != nil {
		fmt.Fprintln(os.Stderr, "factory delete:", err)
		return 1
	}
	f, err := findFactory(cfg, *repoFlag, id)
	if errors.Is(err, errNoFactory) {
		fmt.Fprintf(out, "Nothing to delete: %v.\n", err)
		return 0
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "factory delete:", err)
		return 1
	}
	runtime, err := localNode(cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, "factory delete:", err)
		return 1
	}
	removal := factory.PlanRemoval(f, cfg)
	answers := bufio.NewReader(in)
	if !*yes && !confirm(out, answers, whatGoes(f, removal)+"Delete "+f.Label()+"? [y/N] ") {
		fmt.Fprintln(out, "Nothing was deleted.")
		return 0
	}
	work := factory.CheckWork(removal)
	if !work.Empty() {
		if !term.IsTerminal(in.Fd()) {
			fmt.Fprintf(os.Stderr, "factory delete: refusing: %s has unpushed or uncommitted work and stdin is not a terminal; run it in a terminal\n", f.Label())
			return 1
		}
		if !confirm(out, answers, workAtRisk(f, work)+"Delete it anyway? [y/N] ") {
			fmt.Fprintln(out, "Nothing was deleted.")
			return 0
		}
	}
	var report string
	answer, err := runtime.Call(context.Background(), host.Message{T: "delete", Op: uuid.NewString(), Data: mustJSON(host.DeleteRequest{Repo: f.Repo, Slug: f.Slug, Work: host.WorkDigest(work)})})
	json.Unmarshal(answer.Data, &report)
	fmt.Fprint(out, report)
	switch {
	case err != nil:
		fmt.Fprintln(os.Stderr, "factory delete:", err)
		return 1
	case answer.T != "done":
		fmt.Fprintf(os.Stderr, "factory delete: %s is not fully deleted: %s. Run factory delete %s again to finish\n", f.Label(), answer.Error, id)
		return 1
	}
	return 0
}

var errNoFactory = errors.New("no factory")

func mustJSON(value any) json.RawMessage {
	data, _ := json.Marshal(value)
	return data
}

func findFactory(cfg *state.Config, repoFlag, id string) (factory.Factory, error) {
	registry, err := state.LoadRegistry(cfg.RegistryPath())
	if err != nil {
		return factory.Factory{}, err
	}
	known := knownRepos(cfg, registry)
	repos := cfg.Repos
	if repoFlag != "" {
		configured, ok := configuredRepo(known, repoFlag)
		if !ok {
			return factory.Factory{}, fmt.Errorf("the hub has no repo at %s (its repos: %s)", repoFlag, strings.Join(cfg.Repos, ", "))
		}
		repos = []string{configured}
	} else if repo := repoOf(cfg, id, cwdRepo(cfg)); repo != "" {
		repos = []string{repo}
	}
	f, err := matchFactory(repos, registry, id)
	if errors.Is(err, errNoFactory) && repoFlag == "" {
		if f, err := matchFactory(known, registry, id); err == nil {
			return f, nil
		}
	}
	return f, err
}

func knownRepos(cfg *state.Config, registry *state.Registry) []string {
	repos := slices.Clone(cfg.Repos)
	for _, e := range registry.Factories {
		if !slices.Contains(repos, e.Repo) {
			repos = append(repos, e.Repo)
		}
	}
	return repos
}

func matchFactory(repos []string, registry *state.Registry, id string) (factory.Factory, error) {
	slug := factory.Slugify(id)
	var bySlug, byLabel []factory.Factory
	for _, f := range factory.Discover(repos, registry) {
		switch {
		case f.Slug == slug:
			bySlug = append(bySlug, f)
		case factory.Slugify(f.Label()) == slug:
			byLabel = append(byLabel, f)
		}
	}
	matches := bySlug
	if len(matches) == 0 {
		matches = byLabel
	}
	switch len(matches) {
	case 0:
		return factory.Factory{}, fmt.Errorf("%w %q in %s; pass --repo when it lives in another configured repo", errNoFactory, id, strings.Join(repos, ", "))
	case 1:
		return matches[0], nil
	}
	var found []string
	for _, f := range matches {
		found = append(found, f.Slug+" in "+f.Repo)
	}
	return factory.Factory{}, fmt.Errorf("%q matches %s; pass its worktree name, or --repo", id, strings.Join(found, " and "))
}

func confirm(out io.Writer, answers *bufio.Reader, question string) bool {
	fmt.Fprint(out, question)
	line, _ := answers.ReadString('\n')
	answer := strings.ToLower(strings.TrimSpace(line))
	return answer == "y" || answer == "yes"
}

func whatGoes(f factory.Factory, r factory.Removal) string {
	var b strings.Builder
	name := f.Label()
	if f.Title() != "" {
		name += " · " + f.Title()
	}
	fmt.Fprintf(&b, "Deletes %s (%s) for good:\n", name, f.RepoName())
	fmt.Fprintln(&b, "  session    its Claude and stage panes, and what they started")
	worktree := r.Worktree
	if _, err := os.Stat(r.Worktree); r.Worktree == "" || err != nil {
		worktree = "already removed"
	}
	fmt.Fprintln(&b, "  worktree  ", worktree)
	branches := strings.Join(r.Branches, ", ")
	if branches == "" {
		branches = "none left"
	}
	fmt.Fprintln(&b, "  branches  ", branches, "(local only)")
	fmt.Fprintln(&b, "  state      its factory and QA directories, activity log, drafts, hook records, launch scripts and registry entry")
	fmt.Fprintln(&b, "Pushed PRs and remote branches stay. The id can be used again.")
	return b.String()
}

func workAtRisk(f factory.Factory, w factory.Work) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s has work that exists nowhere else:\n", f.Label())
	for _, line := range w.Dirty {
		fmt.Fprintln(&b, "  uncommitted ", line)
	}
	for _, branch := range slices.Sorted(maps.Keys(w.Unpushed)) {
		for _, commit := range w.Unpushed[branch] {
			fmt.Fprintf(&b, "  not pushed   %s  %s\n", branch, commit)
		}
	}
	fmt.Fprintln(&b, "Deleting loses it.")
	return b.String()
}
