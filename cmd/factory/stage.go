package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/afonsodelgado/factory/internal/host"
	"github.com/afonsodelgado/factory/internal/state"
	"github.com/google/uuid"
)

const resultPoll = 2 * time.Second

const stageUsage = `usage: factory stage start --role <role> --effect <id> --task <file> [--factory id] [--wait]
       factory stage send --role <role> --effect <id> --message <file> [--reask] [--factory id] [--wait]
       factory stage ci <pr number>...`

func runStage(args []string) int {
	if len(args) > 0 && args[0] == "ci" {
		return runStageCI(args[1:])
	}
	if len(args) == 0 || (args[0] != "start" && args[0] != "send") {
		fmt.Fprintln(os.Stderr, stageUsage)
		return 2
	}
	action := args[0]
	flags := flag.NewFlagSet("stage "+action, flag.ContinueOnError)
	effect := flags.String("effect", "", "the committed workflow effect id")
	role := flags.String("role", "", "the stage agent's role, such as engineer")
	text := flags.String(map[string]string{"start": "task", "send": "message"}[action], "", "a file with the task or the follow-up message")
	wait := flags.Bool("wait", false, "wait for the stage's result file, then print it")
	id := flags.String("factory", "", "the factory, when this is not its supervisor's pane")
	reask := flags.Bool("reask", false, "with send: ask the active run again after the engine rejected its result")
	if err := flags.Parse(args[1:]); err != nil {
		return 2
	}
	result, err := stage(stageCall{Action: action, Role: *role, TextFile: *text, Factory: *id, EffectID: *effect, Reask: *reask})
	if err != nil {
		fmt.Fprintln(os.Stderr, "factory stage:", err)
		return 1
	}
	if !*wait {
		fmt.Println(result)
		return 0
	}
	fmt.Printf("%s\n%s\n", result, waitForFile(result))
	return 0
}

type stageCall struct {
	Action, Role, TextFile, Factory, EffectID string
	Reask                                     bool
}

func runStageCI(prs []string) int {
	if len(prs) == 0 {
		fmt.Fprintln(os.Stderr, stageUsage)
		return 2
	}
	inbox, err := ask(host.StageRequest{Action: "ci", PRs: prs})
	if err != nil {
		fmt.Fprintln(os.Stderr, "factory stage:", err)
		return 1
	}
	fmt.Println("The factory app waits for CI and types the result into this session; the summary also lands in " + inbox)
	return 0
}

func stage(c stageCall) (string, error) {
	if c.Role == "" || c.TextFile == "" {
		return "", errors.New("give --role and the file with the task or message")
	}
	text, err := os.ReadFile(c.TextFile)
	if err != nil {
		return "", err
	}
	return ask(host.StageRequest{Action: c.Action, Role: c.Role, Text: string(text), Factory: c.Factory, EffectID: c.EffectID, Reask: c.Reask})
}

func ask(r host.StageRequest) (string, error) {
	cfg, err := state.LoadConfig()
	if err != nil {
		return "", err
	}
	runtime, err := localNode(cfg)
	if err != nil {
		return "", err
	}
	r.Token = os.Getenv("FACTORY_PANE_TOKEN")
	r.Cwd, _ = os.Getwd()
	var answer host.StageReply
	return answer.Result, runtime.Request(context.Background(), "stage", uuid.NewString(), r, &answer)
}

func waitForFile(path string) []byte {
	for {
		if data, err := os.ReadFile(path); err == nil && json.Valid(data) {
			return data
		}
		time.Sleep(resultPoll)
	}
}
