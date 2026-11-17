package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/afonsodelgado/factory/internal/install"
	"github.com/afonsodelgado/factory/internal/node"
	"github.com/afonsodelgado/factory/internal/phone"
	"github.com/afonsodelgado/factory/internal/state"
	"github.com/afonsodelgado/factory/internal/ui/dialog"
)

const phoneUsage = "usage: factory phone status | test | check"

func runPhone(args []string) int {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, phoneUsage)
		return 2
	}
	cfg, err := state.LoadConfig()
	if err != nil {
		fmt.Fprintln(os.Stderr, "factory phone:", err)
		return 1
	}
	switch args[0] {
	case "status":
		err = printPhoneStatus(os.Stdout, cfg)
	case "test":
		err = printTestPush(os.Stdout, cfg)
	case "check":
		if summarize("factory phone check · checks only, changes nothing", phoneDoctor(cfg)) > 0 {
			return 1
		}
	default:
		fmt.Fprintln(os.Stderr, phoneUsage)
		return 2
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "factory phone:", err)
		return 1
	}
	return 0
}

func phoneStatus(cfg *state.Config) (dialog.PhoneContext, error) {
	subs, err := phone.Open(cfg.StateDir).Subscriptions()
	return dialog.PhoneContext{Running: node.Answers(cfg.NodeSocket()), Hostname: cfg.NodeHostname(), Port: cfg.NodePort(), Subscriptions: len(subs)}, err
}

func printPhoneStatus(w io.Writer, cfg *state.Config) error {
	status, err := phoneStatus(cfg)
	if err != nil {
		return err
	}
	url := "unknown; put node.hostname, this machine's ts.net name, in " + cfg.ConfigFile()
	if status.Hostname != "" {
		url = "https://" + status.Hostname
	}
	running := "not running; start it with factory node run"
	if status.Running {
		running = "running"
	}
	fmt.Fprintf(w, "node      %s\nurl       %s\nport      127.0.0.1:%d\nnotify    %d subscribed\n", running, url, status.Port, status.Subscriptions)
	return nil
}

var errPushRejected = errors.New("a test notification did not reach the phone's push service")

func printTestPush(w io.Writer, cfg *state.Config) error {
	client, err := localNode(cfg)
	if err != nil {
		return err
	}
	var results []phone.Delivery
	if err := client.Do(context.Background(), http.MethodPost, "/api/push/test", nil, &results); err != nil {
		return err
	}
	if len(results) == 0 {
		fmt.Fprintln(w, "No phone has turned on notifications yet.")
		return nil
	}
	var failed error
	for _, r := range results {
		outcome := "accepted by the push service; check that the phone shows it"
		if !r.Accepted {
			outcome, failed = r.Problem, errPushRejected
		}
		fmt.Fprintf(w, "%s: %s\n", r.Subscription, outcome)
	}
	return failed
}

func phoneDoctor(cfg *state.Config) []install.Check {
	o := doctorOptions()
	o.Config = cfg
	return install.PhoneChecks(o)
}

type phoneActions struct{ cfg *state.Config }

func (a phoneActions) Status() (dialog.PhoneContext, error) { return phoneStatus(a.cfg) }

func (a phoneActions) Test() (string, error) {
	var b strings.Builder
	err := printTestPush(&b, a.cfg)
	outcome := strings.ReplaceAll(strings.TrimSpace(b.String()), "\n", "; ")
	if errors.Is(err, errPushRejected) {
		return "", errors.New(outcome)
	}
	return outcome, err
}

func (a phoneActions) Check() []dialog.CheckRow {
	var rows []dialog.CheckRow
	for _, c := range phoneDoctor(a.cfg) {
		rows = append(rows, dialog.CheckRow{Name: c.Name, Mark: checkMarks[c.Mark], Detail: c.Detail, Fix: c.Fix})
	}
	return rows
}
