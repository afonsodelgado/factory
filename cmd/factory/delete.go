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

	"github.com/afonsodelgado/factory/internal/factory"
	"github.com/afonsodelgado/factory/internal/hub"
	"github.com/afonsodelgado/factory/internal/remote"
	"github.com/afonsodelgado/factory/internal/state"
	"github.com/afonsodelgado/factory/internal/tmux"
)

func runDelete(args []string, in *os.File, out io.Writer) int {
	flags := flag.NewFlagSet("delete", flag.ContinueOnError)
	repoFlag := flags.String("repo", "", "the repo that holds the factory")
	yes := flags.Bool("yes", false, "skip the first confirmation; unpushed or uncommitted work still asks")
	remoteFlag := flags.String("remote", "", "the remote the factory runs on")
	local := flags.Bool("local", false, "the factory runs on this machine")
	if err := flags.Parse(reorder(flags, args)); err != nil {
		return 2
	}
	if flags.NArg() != 1 || (*remoteFlag != "" && *local) {
		fmt.Fprintln(os.Stderr, "usage: factory delete <id> [--repo path] [--remote name | --local] [--yes]")
		return 2
	}
	id := flags.Arg(0)
	cfg, err := state.LoadConfig()
	if err != nil {
		fmt.Fprintln(os.Stderr, "factory delete:", err)
		return 1
	}
	f, err := findFactory(cfg, *repoFlag, id)
	where := *remoteFlag
	if elsewhere := remote.Holders(cfg, id); where == "" && !*local && errors.Is(err, errNoFactory) && len(elsewhere) > 0 {
		if len(elsewhere) > 1 {
			fmt.Fprintf(os.Stderr, "factory delete: %s exists on %s; pick one with --remote\n", id, strings.Join(elsewhere, " and "))
			return 1
		}
		where = elsewhere[0]
	}
	if where != "" {
		return deleteRemote(cfg, where, id, *yes, in, out)
	}
	if errors.Is(err, errNoFactory) {
		fmt.Fprintf(out, "Nothing to delete: %v.\n", err)
		return 0
	}
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

var errNoFactory = errors.New("no factory")

func deleteFailed(err error) int {
	fmt.Fprintln(os.Stderr, "factory delete:", err)
	return 1
}

func confirmRemoteDelete(ctx context.Context, client remote.Client, target remote.Target, yes bool, in *os.File, out io.Writer) (int, bool) {
	var preview remote.Removal
	if err := client.Read(ctx, "removal", target, &preview); err != nil {
		return deleteFailed(err), false
	}
	answers := bufio.NewReader(in)
	if !yes && !confirm(out, answers, preview.What+"Delete "+target.ID+" on "+client.Name+"? [y/N] ") {
		fmt.Fprintln(out, "Nothing was deleted.")
		return 0, false
	}
	if preview.Risk == "" {
		return 0, true
	}
	if !term.IsTerminal(in.Fd()) {
		return deleteFailed(fmt.Errorf("refusing: %s has unpushed or uncommitted work on %s and stdin is not a terminal; run it in a terminal", target.ID, client.Name)), false
	}
	if !confirm(out, answers, preview.Risk+"Delete it anyway? [y/N] ") {
		fmt.Fprintln(out, "Nothing was deleted.")
		return 0, false
	}
	return 0, true
}

func deleteRemote(cfg *state.Config, name, id string, yes bool, in *os.File, out io.Writer) int {
	client, err := remote.For(cfg, name)
	if err != nil {
		return deleteFailed(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), remote.CreateWait)
	defer cancel()
	target := remote.Target{ID: id}
	if !client.Unsettled("delete", target) {
		if code, confirmed := confirmRemoteDelete(ctx, client, target, yes, in, out); !confirmed {
			return code
		}
	}
	var report string
	err = client.MutateOnce(ctx, "delete", target, &report)
	fmt.Fprint(out, report)
	if err != nil {
		return deleteFailed(fmt.Errorf("%s on %s is not fully deleted: %w. Run factory delete --remote %s %s again to finish", id, name, err, name, id))
	}
	return exitOf(remote.UpdateCache(cfg.StateDir, name, func(c *remote.Cache) error {
		c.Inventory = slices.DeleteFunc(c.Inventory, func(l remote.Listing) bool { return l.ID == id })
		return nil
	}))
}

func exitOf(err error) int {
	if err != nil {
		fmt.Fprintln(os.Stderr, "factory:", err)
		return 1
	}
	return 0
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
	closed := factory.PanesClosed(f.Label(), hub.CloseFactoryPanes(tmux.Client{Socket: cfg.Tmux.Socket}, f.Repo, f.Slug))
	steps := factory.Run(closed, r, func(change func(*state.Registry)) error {
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
