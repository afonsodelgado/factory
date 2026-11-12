package main

import (
	"context"
	"strings"
	"time"

	"github.com/afonsodelgado/factory/internal/install"
	"github.com/afonsodelgado/factory/internal/remote"
	"github.com/afonsodelgado/factory/internal/state"
	"github.com/afonsodelgado/factory/internal/ui/dialog"
)

const probeWait = 10 * time.Second

type remoteActions struct{}

func (remoteActions) Add(name, target, identity string) error {
	r, err := remote.ParseTarget(strings.TrimSpace(target))
	if err != nil {
		return err
	}
	r.Identity = identity
	return addRemote(name, r)
}

var checkMarks = map[install.Mark]string{install.Pass: "ok", install.Fail: "fail", install.Skip: "skip", install.Warn: "warn"}

func (remoteActions) Check(name string) []dialog.CheckRow {
	cfg, err := state.LoadConfig()
	if err != nil {
		return []dialog.CheckRow{{Name: "config", Mark: "fail", Detail: err.Error()}}
	}
	_, checks := remoteDoctor(cfg, name)
	var rows []dialog.CheckRow
	for _, c := range checks {
		if c.Name == "push" {
			continue
		}
		rows = append(rows, dialog.CheckRow{Name: c.Name, Mark: checkMarks[c.Mark], Detail: c.Detail, Fix: c.Fix})
	}
	return rows
}

func (remoteActions) SetDefault(name string) error {
	return setDefaultRemote(name)
}

func (remoteActions) Remove(name string) error {
	_, err := removeRemote(name)
	return err
}

func (remoteActions) SetPush(p state.Push) error {
	_, err := updatePush(func(current *state.Push) { *current = p })
	return err
}

func (remoteActions) TestPush() string {
	lines, err := pushTests()
	if err != nil {
		return err.Error()
	}
	return strings.Join(lines, "; ")
}

func (remoteActions) NewTopic() string {
	return newTopic()
}

func probeRemote(name string) (string, error) {
	cfg, err := state.LoadConfig()
	if err != nil {
		return "", err
	}
	client, err := remote.For(cfg, name)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), probeWait)
	defer cancel()
	hello, err := client.Hello(ctx)
	if err != nil {
		return "", err
	}
	same := "the same as here"
	if hello.Release != version {
		same = "but this machine has " + version + "; run factory update on both"
	}
	return client.Endpoint() + "  connected · factory " + hello.Release + ", " + same, nil
}
