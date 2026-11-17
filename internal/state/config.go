package state

import (
	"errors"
	"fmt"
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
	Tmux         Tmux     `yaml:"tmux"`
	Review       Review   `yaml:"review"`

	Pipelines       map[string][]string     `yaml:"pipelines"`
	DefaultPipeline string                  `yaml:"default_pipeline"`
	Agents          map[string]AgentSetting `yaml:"agents"`
	Supervisor      AgentSetting            `yaml:"supervisor"`
	Codex           Codex                   `yaml:"codex"`

	Machine `yaml:",inline"`

	ConfigDir string `yaml:"-"`
	StateDir  string `yaml:"-"`
}

type Hub struct {
	Session   string `yaml:"session"`
	ReturnKey string `yaml:"return_key"`
}

type Alerts struct {
	Bell    bool `yaml:"bell"`
	Desktop bool `yaml:"desktop"`
	MacOS   bool `yaml:"macos"`
}

func (a Alerts) Notifies() bool {
	return a.Desktop || a.MacOS
}

type Codex struct {
	WritableDirs []string `yaml:"writable_dirs"`
}

type Review struct {
	Chrome string `yaml:"chrome"`
}

type Tmux struct {
	Socket string `yaml:"socket"`
}

func LoadConfig() (*Config, error) {
	cfg := &Config{
		ConfigDir: ConfigDir(),
		StateDir:  StateDir(),
	}
	path := cfg.ConfigFile()
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	if err == nil {
		if err := yaml.Unmarshal(data, cfg); err != nil {
			return nil, err
		}
	}
	if cfg.StalledAfter != "" && !positiveDuration(cfg.StalledAfter) {
		return nil, fmt.Errorf("%s: stalled_after %q is not a duration; write it with a unit, such as 30m", path, cfg.StalledAfter)
	}
	if err := cfg.validatePipelines(path); err != nil {
		return nil, err
	}
	if err := ValidateAgents(path, cfg.Agents); err != nil {
		return nil, err
	}
	if err := ValidateSupervisor(path, cfg.Supervisor); err != nil {
		return nil, err
	}
	if err := cfg.Machine.Validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	cfg.applyDefaults()
	if err := cfg.addKnownRepos(); err != nil {
		return nil, fmt.Errorf("%s: %w", KnownReposPath(cfg.StateDir), err)
	}
	return cfg, nil
}

func (c *Config) validatePipelines(path string) error {
	if err := validatePipelines(path, c.Pipelines); err != nil {
		return err
	}
	_, err := ResolvePipelines(c, Profile{}, "")
	return err
}

func (c *Config) ConfigFile() string { return filepath.Join(c.ConfigDir, "config.yml") }

func (c *Config) applyDefaults() {
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
	if socket := os.Getenv("FACTORY_TMUX_SOCKET"); socket != "" {
		c.Tmux.Socket = socket
	}
}

func positiveDuration(s string) bool {
	d, err := time.ParseDuration(s)
	return err == nil && d > 0
}

func (c *Config) Stalled() time.Duration {
	if !positiveDuration(c.StalledAfter) {
		return 20 * time.Minute
	}
	d, _ := time.ParseDuration(c.StalledAfter)
	return d
}

func (c *Config) RegistryPath() string { return filepath.Join(c.StateDir, "registry.json") }
func (c *Config) SessionsDir() string  { return SessionsDir(c.StateDir) }
func (c *Config) DraftsDir() string    { return filepath.Join(c.StateDir, "drafts") }
func (c *Config) ActivityDir() string  { return filepath.Join(c.StateDir, "activity") }
func (c *Config) SocketPath() string   { return filepath.Join(c.StateDir, "hub.sock") }
func (c *Config) LockPath() string     { return filepath.Join(c.StateDir, "hub.lock") }
func (c *Config) NodeSocket() string   { return filepath.Join(c.StateDir, "node.sock") }
func (c *Config) ReviewSocket() string { return filepath.Join(c.StateDir, "review.sock") }
func (c *Config) NodeLockPath() string { return filepath.Join(c.StateDir, "node.lock") }
func (c *Config) HubViewLock() string  { return filepath.Join(c.StateDir, "hub-view.lock") }
func (c *Config) RulesPath() string    { return filepath.Join(c.ConfigDir, "rules.md") }

func SessionsDir(stateDir string) string {
	return filepath.Join(stateDir, "sessions")
}

func ConfigDir() string {
	if dir := os.Getenv("FACTORY_CONFIG_DIR"); dir != "" {
		return dir
	}
	return filepath.Join(xdg("XDG_CONFIG_HOME", ".config"), "factory")
}

func StateDir() string {
	if dir := os.Getenv("FACTORY_STATE_DIR"); dir != "" {
		return dir
	}
	return filepath.Join(xdg("XDG_STATE_HOME", ".local/state"), "factory")
}

func CodexHome() string {
	if dir := os.Getenv("CODEX_HOME"); dir != "" {
		return dir
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".codex")
}

func OpenCodeConfigDir() string {
	return filepath.Join(xdg("XDG_CONFIG_HOME", ".config"), "opencode")
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
