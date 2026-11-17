package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/afonsodelgado/factory/internal/host"
	"github.com/afonsodelgado/factory/internal/recipe"
	"github.com/afonsodelgado/factory/internal/state"
	"github.com/afonsodelgado/factory/internal/workflow"
)

func runWorkflow(args []string) int {
	if len(args) == 2 && args[0] == "comments" {
		snapshot, err := state.ReadState(args[1])
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		cwd, err := os.Getwd()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		result, err := (&host.GitHub{Run: host.RunGH}).Comments(cwd, *snapshot, time.Now())
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if err := json.NewEncoder(os.Stdout).Encode(result); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		return 0
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
	flags.Bool("inbox", false, "write structured completion to the inbox")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	var numbers []int
	for _, arg := range flags.Args() {
		n, err := strconv.Atoi(arg)
		if err != nil || n <= 0 {
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
	json.NewEncoder(os.Stdout).Encode(observations)
	return 0
}
