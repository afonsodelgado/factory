package node

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/afonsodelgado/factory/internal/state"
)

var serviceEnv = []string{"PATH", "HOME", "SHELL", "USER", "LANG", "LC_ALL", "LC_CTYPE", "XDG_CONFIG_HOME", "XDG_STATE_HOME", "FACTORY_CONFIG_DIR", "FACTORY_STATE_DIR", "FACTORY_TMUX_SOCKET", "CLAUDE_CONFIG_DIR", "CODEX_HOME"}

type Service struct {
	Binary string
	Home   string
	Log    string
	Env    []string
	Run    Runner
}

type ServiceState struct {
	Installed bool
	Loaded    bool
	Running   bool
	PID       string
}

func NewService(cfg *state.Config, binary, home string, run Runner) Service {
	var env []string
	for _, name := range serviceEnv {
		if value := os.Getenv(name); value != "" {
			env = append(env, name+"="+value)
		}
	}
	return Service{Binary: binary, Home: home, Log: filepath.Join(cfg.StateDir, "logs", "node.log"), Env: env, Run: run}
}

func (s Service) Write() error {
	if err := os.MkdirAll(filepath.Dir(s.Log), 0o700); err != nil {
		return err
	}
	log, err := os.OpenFile(s.Log, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	log.Close()
	if err := os.Chmod(s.Log, 0o600); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.UnitPath()), 0o755); err != nil {
		return err
	}
	return state.WriteFileAtomic(s.UnitPath(), s.Unit())
}

func (s Service) remove() error {
	err := os.Remove(s.UnitPath())
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

func (s Service) installed() bool {
	_, err := os.Stat(s.UnitPath())
	return err == nil
}

type Install struct {
	Binary   string `json:"binary,omitempty"`
	Host     string `json:"host,omitempty"`
	Serve    string `json:"serve,omitempty"`
	Complete bool   `json:"complete,omitempty"`
}

func InstallPath(stateDir string) string { return filepath.Join(stateDir, "node-install.json") }

func LoadInstall(stateDir string) (Install, error) {
	var record Install
	data, err := os.ReadFile(InstallPath(stateDir))
	if errors.Is(err, fs.ErrNotExist) {
		return record, nil
	}
	if err == nil {
		err = json.Unmarshal(data, &record)
	}
	return record, err
}

func SaveInstall(stateDir string, record Install) error {
	if record == (Install{}) {
		err := os.Remove(InstallPath(stateDir))
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	return state.WriteFileAtomic(InstallPath(stateDir), data)
}
