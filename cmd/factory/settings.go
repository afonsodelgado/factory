package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"time"

	"github.com/afonsodelgado/factory/internal/host"
	"github.com/afonsodelgado/factory/internal/node"
	"github.com/afonsodelgado/factory/internal/state"
)

func readSettings(cfg *state.Config) map[string]string {
	local := map[string]string{}
	if shared, err := state.ReadShared(cfg.ConfigDir); err == nil && shared != "" {
		local["config"] = shared
	}
	if rules, err := os.ReadFile(cfg.RulesPath()); err == nil && strings.TrimSpace(string(rules)) != "" {
		local["rules"] = string(rules)
	}
	return local
}

func settingSources(cfg *state.Config, queue node.Caller) node.Sources {
	write := func(ctx context.Context, key, content string) (string, error) {
		switch {
		case key == "config":
			backup(cfg.ConfigFile())
			written, err := state.WriteShared(cfg.ConfigDir, content)
			if err != nil {
				return "", err
			}
			answer, err := queue.Call(ctx, host.Message{T: "settings"})
			if err == nil && answer.T != "done" {
				err = errors.New(answer.Error)
			}
			return written, err
		case key == "rules":
			return content, replaceFile(cfg.RulesPath(), content)
		}
		return "", errors.New("unknown setting " + key)
	}
	reload := func(key string) {
		if key == "config" {
			queue.Call(context.Background(), host.Message{T: "settings"})
		}
	}
	return node.Sources{Read: func() map[string]string { return readSettings(cfg) }, Write: write, Authored: reload}
}

func replaceFile(path, content string) error {
	backup(path)
	return state.WriteFileAtomic(path, []byte(content))
}

func backup(path string) {
	data, err := os.ReadFile(path)
	if err != nil || len(bytes.TrimSpace(data)) == 0 {
		return
	}
	os.WriteFile(path+"."+time.Now().Format("20060102-150405")+".bak", data, 0o600)
}
