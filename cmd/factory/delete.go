package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"maps"
	"os"
	"slices"
	"strings"

	"github.com/charmbracelet/x/term"

	"github.com/afonsodelgado/factory/internal/factory"
	"github.com/afonsodelgado/factory/internal/hub"
	"github.com/afonsodelgado/factory/internal/state"
	"github.com/afonsodelgado/factory/internal/tmux"
)

func runDelete(args []string, in *os.File, out io.Writer) int {
	flags := flag.NewFlagSet("delete", flag.ContinueOnError)
	repoFlag := flags.String("repo", "", "the repo that holds the factory")
	yes := flags.Bool("yes", false, "skip the first confirmation; unpushed or uncommitted work still asks")
	if err := flags.Parse(reorder(args)); err != nil {
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
	if work := factory.CheckWork(removal); !work.Empty() {
		if !term.IsTerminal(in.Fd()) {
			fmt.Fprintf(os.Stderr, "factory delete: refusing: %s has unpushed or uncommitted work and stdin is not a terminal; run it in a terminal\n", f.Label())
			return 1
		}
		if !confirm(out, answers, workAtRisk(f, work)+"Delete it anyway? [y/N] ") {
			fmt.Fprintln(out, "Nothing was deleted.")
			return 0
		}
	}
	report, failed, err := deleteFactory(cfg, f, removal)
	fmt.Fprint(out, report)
	if err != nil {
		fmt.Fprintln(os.Stderr, "factory delete:", err)
		return 1
	}
	if failed {
		fmt.Fprintf(os.Stderr, "factory delete: %s is not fully deleted; run factory delete %s again to finish\n", f.Label(), id)
		return 1
	}
	return 0
}

func findFactory(cfg *state.Config, repoFlag, id string) (factory.Factory, error) {
	repos := cfg.Repos
	if repoFlag != "" {
		configured, ok := configuredRepo(cfg.Repos, repoFlag)
		if !ok {
			return factory.Factory{}, fmt.Errorf("the hub has no repo at %s (its repos: %s)", repoFlag, strings.Join(cfg.Repos, ", "))
		}
		repos = []string{configured}
	} else if repo := repoOf(cfg, id, cwdRepo(cfg)); repo != "" {
		repos = []string{repo}
	}
	registry, err := state.LoadRegistry(cfg.RegistryPath())
	if err != nil {
		return factory.Factory{}, err
	}
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
		return factory.Factory{}, fmt.Errorf("no factory %q in %s; pass --repo when it lives in another configured repo", id, strings.Join(repos, ", "))
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
	fmt.Fprintln(&b, "  session    its Claude pane, if it runs")
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
	fmt.Fprintln(&b, "  state      its factory and QA directories, activity log, drafts and registry entry")
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

func deleteFactory(cfg *state.Config, f factory.Factory, r factory.Removal) (string, bool, error) {
	if hubRunning(cfg) {
		reply, err := request(cfg.SocketPath(), "delete", hub.DeleteRequest{Repo: f.Repo, Slug: f.Slug})
		if err != nil {
			return "", false, err
		}
		var report string
		json.Unmarshal(reply.Data, &report)
		return report, reply.T == "error", nil
	}
	if err := closePane(cfg, f); err != nil {
		return "", false, err
	}
	steps := factory.Run(r, func(change func(*state.Registry)) error {
		registry, err := state.LoadRegistry(cfg.RegistryPath())
		if err != nil {
			return err
		}
		change(registry)
		return registry.Save()
	})
	_, failed := factory.FirstFailure(steps)
	return factory.Report(steps), failed, nil
}

func closePane(cfg *state.Config, f factory.Factory) error {
	tm := tmux.Client{Socket: cfg.Tmux.Socket}
	if !tm.HasSession(cfg.Hub.Session) {
		return nil
	}
	panes, err := tm.Panes()
	if err != nil {
		return err
	}
	pane, ok := hub.FactoryPane(panes, f.Repo, f.Slug)
	switch {
	case !ok:
		return nil
	case pane.WindowName == hub.HubWindow:
		return tm.KillPane(pane.ID)
	}
	return tm.KillWindow(pane.WindowID)
}
