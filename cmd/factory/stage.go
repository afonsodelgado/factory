package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/afonsodelgado/factory/internal/hub"
	"github.com/afonsodelgado/factory/internal/state"
)

const resultPoll = 2 * time.Second

const stageUsage = `usage: factory stage start --role <role> --task <file> [--wait]
       factory stage send --role <role> --message <file> [--wait]
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
	role := flags.String("role", "", "the stage agent's role, such as engineer")
	text := flags.String(map[string]string{"start": "task", "send": "message"}[action], "", "a file with the task or the follow-up message")
	wait := flags.Bool("wait", false, "wait for the stage's result file, then print it")
	if err := flags.Parse(args[1:]); err != nil {
		return 2
	}
	result, err := stage(action, *role, *text)
	if err != nil {
		fmt.Fprintln(os.Stderr, "factory stage:", err)
		return 1
	}
	if !*wait {
		fmt.Println(result)
		return 0
	}
	data := waitForFile(result)
	fmt.Printf("%s\n%s\n", result, data)
	return 0
}

func runStageCI(prs []string) int {
	if len(prs) == 0 {
		fmt.Fprintln(os.Stderr, stageUsage)
		return 2
	}
	inbox, err := ask(hub.StageRequest{Action: "ci", PRs: prs})
	if err != nil {
		fmt.Fprintln(os.Stderr, "factory stage:", err)
		return 1
	}
	fmt.Println("The factory app waits for CI and CodeRabbit and types the result into this session; the summary also lands in " + inbox)
	return 0
}

func stage(action, role, textFile string) (string, error) {
	if role == "" || textFile == "" {
		return "", errors.New("give --role and the file with the task or message")
	}
	text, err := os.ReadFile(textFile)
	if err != nil {
		return "", err
	}
	return ask(hub.StageRequest{Action: action, Role: role, Text: string(text)})
}

func ask(r hub.StageRequest) (string, error) {
	cfg, err := state.LoadConfig()
	if err != nil {
		return "", err
	}
	if !hubRunning(cfg) {
		return "", errors.New("the factory hub is not running; start it with factory open")
	}
	r.Token = os.Getenv("FACTORY_PANE_TOKEN")
	r.Cwd, _ = os.Getwd()
	reply, err := request(cfg.SocketPath(), "stage", r)
	if err != nil {
		return "", err
	}
	if reply.T == "error" {
		return "", errors.New(reply.Error)
	}
	var answer hub.StageReply
	if err := json.Unmarshal(reply.Data, &answer); err != nil {
		return "", err
	}
	return answer.Result, nil
}

func waitForFile(path string) []byte {
	for {
		if data, err := os.ReadFile(path); err == nil && json.Valid(data) {
			return data
		}
		time.Sleep(resultPoll)
	}
}
