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
	"time"

	"github.com/afonsodelgado/factory/internal/host"
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
	if len(args) == 2 && args[0] == "commands" {
		resolved, err := recipe.Resolve(args[1])
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if err := json.NewEncoder(os.Stdout).Encode(resolved.Recipe); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		return 0
	}
	if len(args) != 3 || args[0] != "next" {
		fmt.Fprintln(os.Stderr, "usage: factory workflow next <state.json> <event.json>")
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
	proposal, err := workflow.Next(*snapshot, event)
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
