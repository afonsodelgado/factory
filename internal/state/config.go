package state

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/goccy/go-yaml"
)

type Config struct {
	Repos        []string `yaml:"repos"`
	Hub          Hub      `yaml:"hub"`
	StalledAfter string   `yaml:"stalled_after"`
	Alerts       Alerts   `yaml:"alerts"`
	Icons        string   `yaml:"icons"`
	Claude       Claude   `yaml:"claude"`
	Tmux         Tmux     `yaml:"tmux"`

	ConfigDir string `yaml:"-"`
	StateDir  string `yaml:"-"`
}

type Hub struct {
	Session   string `yaml:"session"`
	ReturnKey string `yaml:"return_key"`
}

type Alerts struct {
	Bell  bool `yaml:"bell"`
	MacOS bool `yaml:"macos"`
}

type Claude struct {
	RemoteControl *bool `yaml:"remote_control"`
}

type Tmux struct {
	Socket string `yaml:"socket"`
}

func LoadConfig() (*Config, error) {
	cfg := &Config{
		ConfigDir: ConfigDir(),
		StateDir:  StateDir(),
	}
	data, err := os.ReadFile(filepath.Join(cfg.ConfigDir, "config.yml"))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	if err == nil {
		if err := yaml.Unmarshal(data, cfg); err != nil {
			return nil, err
		}
	}
	cfg.applyDefaults()
	return cfg, nil
}

func (c *Config) applyDefaults() {
	if len(c.Repos) == 0 {
		c.Repos = []string{"~/dev/cowork", "~/dev/factory"}
	}
	for i, repo := range c.Repos {
		c.Repos[i] = ExpandHome(repo)
	}
	if c.Hub.Session == "" {
		c.Hub.Session = "factory"
	}
	if c.Hub.ReturnKey == "" {
		c.Hub.ReturnKey = "M-0"
	}
	if c.Icons == "" {
		c.Icons = "nerd"
	}
	if socket := os.Getenv("FACTORY_TUI_TMUX_SOCKET"); socket != "" {
		c.Tmux.Socket = socket
	}
}

func (c *Config) Stalled() time.Duration {
	if d, err := time.ParseDuration(c.StalledAfter); err == nil && d > 0 {
		return d
	}
	return 20 * time.Minute
}

func (c *Config) RemoteControl() bool {
	return c.Claude.RemoteControl == nil || *c.Claude.RemoteControl
}

func (c *Config) RegistryPath() string { return filepath.Join(c.StateDir, "registry.json") }
func (c *Config) SessionsDir() string  { return SessionsDir(c.StateDir) }
func (c *Config) DraftsDir() string    { return filepath.Join(c.StateDir, "drafts") }
func (c *Config) ActivityDir() string  { return filepath.Join(c.StateDir, "activity") }
func (c *Config) SocketPath() string   { return filepath.Join(c.StateDir, "hub.sock") }
func (c *Config) LockPath() string     { return filepath.Join(c.StateDir, "hub.lock") }

func SessionsDir(stateDir string) string {
	return filepath.Join(stateDir, "sessions")
}

func ConfigDir() string {
	if dir := os.Getenv("FACTORY_TUI_CONFIG_DIR"); dir != "" {
		return dir
	}
	return filepath.Join(xdg("XDG_CONFIG_HOME", ".config"), "factory-tui")
}

func StateDir() string {
	if dir := os.Getenv("FACTORY_TUI_STATE_DIR"); dir != "" {
		return dir
	}
	return filepath.Join(xdg("XDG_STATE_HOME", ".local/state"), "factory-tui")
}

func xdg(env, fallback string) string {
	if dir := os.Getenv(env); dir != "" {
		return dir
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, fallback)
}

func ExpandHome(path string) string {
	if path != "~" && !strings.HasPrefix(path, "~/") {
		return path
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, strings.TrimPrefix(path, "~"))
}
