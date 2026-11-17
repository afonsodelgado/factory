package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/afonsodelgado/factory/internal/state"
	"github.com/afonsodelgado/factory/internal/workflow"
)

func runWorkflow(args []string) int {
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
