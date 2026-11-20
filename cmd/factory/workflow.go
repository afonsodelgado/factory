package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/afonsodelgado/factory/internal/host"
	"github.com/afonsodelgado/factory/internal/install"
	"github.com/afonsodelgado/factory/internal/node"
	"github.com/afonsodelgado/factory/internal/recipe"
	"github.com/afonsodelgado/factory/internal/state"
	"github.com/afonsodelgado/factory/internal/workflow"
)

func runWorkflow(args []string) int {
	if (len(args) == 2 || len(args) == 3) && args[0] == "comments" {
		return refreshComments(args[1], args[2:])
	}
	if len(args) > 0 && args[0] == "watch" {
		return watchWorkflow(args[1:])
	}
	if len(args) > 0 && args[0] == "record" {
		return recordCommands(args[1:])
	}
	if (len(args) == 2 || (len(args) == 3 && args[2] == "--qa")) && args[0] == "commands" {
		resolved, err := resolveRecipe(args[1], len(args) == 3)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if err := json.NewEncoder(os.Stdout).Encode(resolved); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		return 0
	}
	if len(args) != 3 || args[0] != "next" {
		fmt.Fprintln(os.Stderr, "usage: factory workflow next <state.json> <event.json> | commands <worktree> [--qa] | record --worktree <worktree> --factory <id> [--replace <key>]... [--supersede <key>]... [--yours] <key>=<value>... | comments <state.json> [<dispositions.json>] | watch --dir <factory dir> --run <run id> <pr number>...")
		return 2
	}
	snapshot, err := state.ReadState(args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	data, err := os.ReadFile(args[2])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	var event workflow.Event
	if err := json.Unmarshal(data, &event); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	proposal, err := workflow.Next(filepath.Dir(args[1]), *snapshot, event)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := json.NewEncoder(os.Stdout).Encode(proposal); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

func resolveRecipe(worktree string, qa bool) (state.Recipe, error) {
	resolved, err := recipe.Resolve(worktree)
	if qa && resolved.Profile.QA.Mode == "" {
		resolved.Recipe.Unresolved = append(resolved.Recipe.Unresolved, "qa.mode: not chosen")
	}
	return resolved.Recipe, err
}

type keys []string

func (k *keys) String() string     { return strings.Join(*k, ",") }
func (k *keys) Set(v string) error { *k = append(*k, v); return nil }

const recordUsage = "usage: factory workflow record --worktree <worktree> --factory <id> [--replace <key>]... [--supersede <key>]... [--yours] <key>=<value>..."

var stackKeys = []string{"qa.start", "qa.url", "qa.health", "qa.seed", "qa.sign_in"}

func recordCommands(args []string) int {
	flags := flag.NewFlagSet("workflow record", flag.ContinueOnError)
	worktree := flags.String("worktree", "", "the factory's worktree")
	id := flags.String("factory", "", "the factory that found the values")
	yours := flags.Bool("yours", false, "the developer typed these values")
	var replace keys
	flags.Var(&replace, "replace", "a key whose discovered value stopped working")
	var supersede keys
	flags.Var(&supersede, "supersede", "a key whose developer-typed value fails on the base commit too")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	values := map[string]string{}
	for _, arg := range flags.Args() {
		key, value, ok := strings.Cut(arg, "=")
		if !ok {
			fmt.Fprintln(os.Stderr, recordUsage)
			return 2
		}
		values[key] = value
	}
	if *worktree == "" || (*id == "" && !*yours) || len(values) == 0 {
		fmt.Fprintln(os.Stderr, recordUsage)
		return 2
	}
	recorded, err := record(*worktree, *id, *yours, values, replace, supersede)
	if err != nil {
		fmt.Fprintln(os.Stderr, "factory workflow record:", err)
		return 1
	}
	if err := json.NewEncoder(os.Stdout).Encode(recorded); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

type recordedCommands struct {
	state.Recorded
	Recipe state.Recipe `json:"recipe"`
}

func record(worktree, id string, yours bool, values map[string]string, replace, supersede []string) (recordedCommands, error) {
	var out recordedCommands
	repo, _, err := install.MainCheckout(worktree)
	if err != nil {
		return out, err
	}
	verify := func(effective map[string]string, written []string) error {
		if !slices.ContainsFunc(written, func(key string) bool { return slices.Contains(stackKeys, key) }) {
			return nil
		}
		if err := stackAnswers(repo, worktree, effective); err != nil {
			return fmt.Errorf("recorded nothing: the stack did not come up in %s with these values and the ones already saved: %w", worktree, err)
		}
		return nil
	}
	if yours {
		out.Recorded, err = state.RecordYours(repo, values)
	} else {
		out.Recorded, err = state.RecordDiscovered(repo, id, time.Now(), values, replace, supersede, verify)
	}
	if err != nil {
		return out, err
	}
	out.Recipe, err = resolveRecipe(worktree, false)
	return out, err
}

func stackAnswers(repo, worktree string, values map[string]string) error {
	profile, err := state.ReadProfile(repo)
	if err != nil {
		return err
	}
	qa := &profile.QA
	qa.Start, qa.URL, qa.Health, qa.Seed, qa.SignIn = values["qa.start"], values["qa.url"], values["qa.health"], values["qa.seed"], values["qa.sign_in"]
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	return node.TryStack(ctx, worktree, profile)
}

func watchWorkflow(args []string) int {
	flags := flag.NewFlagSet("workflow watch", flag.ContinueOnError)
	dir := flags.String("dir", "", "factory directory")
	runID := flags.String("run", "", "active run id")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	var numbers []int
	for _, arg := range flags.Args() {
		n, err := strconv.Atoi(arg)
		if err != nil || n <= 0 {
			fmt.Fprintln(os.Stderr, "usage: factory workflow watch --dir <factory dir> --run <run id> <pr number>...")
			return 2
		}
		numbers = append(numbers, n)
	}
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	sleep := func(ctx context.Context, d time.Duration) error {
		timer := time.NewTimer(d)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return nil
		}
	}
	observations, err := host.WatchPRs(context.Background(), *dir, cwd, *runID, numbers, &host.GitHub{Run: host.RunGH}, time.Now, sleep)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := json.NewEncoder(os.Stdout).Encode(observations); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

func refreshComments(statePath string, proposed []string) int {
	snapshot, err := state.ReadState(statePath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	dispositions := maps.Clone(snapshot.Evidence.CommentDispositions)
	if dispositions == nil {
		dispositions = map[string]string{}
	}
	if len(proposed) == 1 {
		data, err := os.ReadFile(proposed[0])
		var changes map[string]string
		if err == nil {
			err = json.Unmarshal(data, &changes)
		}
		for key, disposition := range changes {
			if !slices.Contains([]string{"pending", "handled", "declined"}, disposition) {
				err = fmt.Errorf("comment %s: disposition must be pending, handled or declined", key)
			}
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		maps.Copy(dispositions, changes)
	}
	snapshot.Evidence.CommentDispositions = dispositions
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	now := time.Now()
	result, err := (&host.GitHub{Run: host.RunGH}).Comments(cwd, *snapshot, now)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if result.Evidence.Status != "skipped" {
		if snapshot.Workflow.ActiveRun == "" {
			fmt.Fprintln(os.Stderr, "comment refresh requires an active pr run")
			return 1
		}
		if _, err := host.QueueObservation(filepath.Dir(statePath), "comments", snapshot.Workflow.ActiveRun, now, map[string]any{"comments": result.Evidence, "comment_dispositions": dispositions, "refresh": result}, "commit its comments evidence and comment_dispositions, with the current stack, PRs and latest observations, as an observe event before any reply"); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
	}
	if err := json.NewEncoder(os.Stdout).Encode(result); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}
