package state

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"

	"github.com/goccy/go-yaml"
)

type Profile struct {
	Commands struct {
		Build   string `yaml:"build"`
		Lint    string `yaml:"lint"`
		Test    string `yaml:"test"`
		Migrate string `yaml:"migrate"`
	} `yaml:"commands"`
	CodebaseSkill string `yaml:"codebase_skill"`
	Design        struct {
		Source string   `yaml:"source"`
		Files  []string `yaml:"files"`
	} `yaml:"design"`
	QA struct {
		Mode           string `yaml:"mode"`
		Notes          string `yaml:"notes"`
		URL            string `yaml:"url"`
		APIPort        int    `yaml:"api_port"`
		WebPort        int    `yaml:"web_port"`
		Start          string `yaml:"start"`
		Health         string `yaml:"health"`
		HealthStatus   int    `yaml:"health_status"`
		StartupTimeout string `yaml:"startup_timeout"`
		Seed           string `yaml:"seed"`
		SignIn         string `yaml:"sign_in"`
	} `yaml:"qa"`
	Pipelines       map[string][]string     `yaml:"pipelines"`
	DefaultPipeline string                  `yaml:"default_pipeline"`
	Agents          map[string]AgentSetting `yaml:"agents"`
	Supervisor      AgentSetting            `yaml:"supervisor"`
	StalledAfter    string                  `yaml:"stalled_after"`
	Codex           Codex                   `yaml:"codex"`
}

var FactoryPaths = []string{".factory/", ".claude/worktrees/", ".claude/factory/", ".claude/factory-archive/", ".claude/qa/"}

var machineKeys = []string{"repos", "hub", "alerts", "icons", "tmux", "review"}

func RepoDir(repo string) string {
	return filepath.Join(repo, ".factory")
}

func RepoConfigPath(repo string) string {
	return filepath.Join(RepoDir(repo), "config.yml")
}

func LegacyProfilePath(repo string) string {
	return filepath.Join(repo, ".claude", "factory.yml")
}

func ProfilePath(repo string) string {
	current := RepoConfigPath(repo)
	if _, err := os.Stat(current); errors.Is(err, fs.ErrNotExist) {
		if _, err := os.Stat(LegacyProfilePath(repo)); err == nil {
			return LegacyProfilePath(repo)
		}
	}
	return current
}

func ReadProfile(repo string) (Profile, error) {
	var p Profile
	data, err := os.ReadFile(ProfilePath(repo))
	if errors.Is(err, fs.ErrNotExist) {
		return p, nil
	}
	if err != nil {
		return p, err
	}
	err = yaml.Unmarshal(data, &p)
	return p, err
}

func CheckProfileKeys(repo string) error {
	path := ProfilePath(repo)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var keys map[string]any
	if err := yaml.Unmarshal(data, &keys); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	for _, key := range machineKeys {
		if _, set := keys[key]; set {
			return fmt.Errorf("%s: %s is a setting of this machine, not of a repo; move it to the global config.yml", path, key)
		}
	}
	if stalled, set := keys["stalled_after"]; set && !positiveDuration(fmt.Sprint(stalled)) {
		return fmt.Errorf("%s: stalled_after %v is not a duration, so the global value applies; write it with a unit, such as 30m", path, stalled)
	}
	return nil
}

func (p Profile) BrowserQA() bool {
	return p.QA.Mode == "browser"
}

const NotNeeded = "none"

func (c *Config) WithProfile(p Profile) *Config {
	merged := *c
	merged.Supervisor = p.Supervisor.Over(c.Supervisor)
	if positiveDuration(p.StalledAfter) {
		merged.StalledAfter = p.StalledAfter
	}
	merged.Codex.WritableDirs = slices.Concat(c.Codex.WritableDirs, p.Codex.WritableDirs)
	return &merged
}
