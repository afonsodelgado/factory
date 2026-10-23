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

func runStage(args []string) int {
	if len(args) == 0 || (args[0] != "start" && args[0] != "send") {
		fmt.Fprintln(os.Stderr, "usage: factory stage start --role <role> --task <file> [--wait]\n       factory stage send --role <role> --message <file> [--wait]")
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

func stage(action, role, textFile string) (string, error) {
	if role == "" || textFile == "" {
		return "", errors.New("give --role and the file with the task or message")
	}
	text, err := os.ReadFile(textFile)
	if err != nil {
		return "", err
	}
	cfg, err := state.LoadConfig()
	if err != nil {
		return "", err
	}
	if !hubRunning(cfg) {
		return "", errors.New("the factory hub is not running; start it with factory open")
	}
	cwd, _ := os.Getwd()
	reply, err := request(cfg.SocketPath(), "stage", hub.StageRequest{Action: action, Token: os.Getenv("FACTORY_PANE_TOKEN"), Cwd: cwd, Role: role, Text: string(text)})
	if err != nil {
		return "", err
	}
	if reply.T == "error" {
		return "", errors.New(reply.Error)
	}
	var r hub.StageReply
	if err := json.Unmarshal(reply.Data, &r); err != nil {
		return "", err
	}
	return r.Result, nil
}

func waitForFile(path string) []byte {
	for {
		if data, err := os.ReadFile(path); err == nil && json.Valid(data) {
			return data
		}
		time.Sleep(resultPoll)
	}
}
