package state

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"

	"github.com/goccy/go-yaml"
	"github.com/goccy/go-yaml/parser"
)

type Shared struct {
	StalledAfter    string                  `yaml:"stalled_after,omitempty"`
	Pipelines       map[string][]string     `yaml:"pipelines,omitempty"`
	DefaultPipeline string                  `yaml:"default_pipeline,omitempty"`
	Agents          map[string]AgentSetting `yaml:"agents,omitempty"`
	Supervisor      AgentSetting            `yaml:"supervisor,omitempty"`
	Codex           Codex                   `yaml:"codex,omitempty"`
}

func (s Shared) entries() []struct {
	name  string
	value any
	zero  bool
} {
	return []struct {
		name  string
		value any
		zero  bool
	}{
		{"stalled_after", s.StalledAfter, s.StalledAfter == ""},
		{"pipelines", s.Pipelines, len(s.Pipelines) == 0},
		{"default_pipeline", s.DefaultPipeline, s.DefaultPipeline == ""},
		{"agents", s.Agents, len(s.Agents) == 0},
		{"supervisor", s.Supervisor, s.Supervisor == AgentSetting{}},
		{"codex", s.Codex, len(s.Codex.WritableDirs) == 0},
	}
}

func (s Shared) validate(path string) error {
	cfg := Config{StalledAfter: s.StalledAfter, Pipelines: s.Pipelines, DefaultPipeline: s.DefaultPipeline, Agents: s.Agents, Supervisor: s.Supervisor}
	if cfg.StalledAfter != "" && !positiveDuration(cfg.StalledAfter) {
		return fmt.Errorf("%s: stalled_after %q is not a duration", path, cfg.StalledAfter)
	}
	if err := cfg.validatePipelines(path); err != nil {
		return err
	}
	if err := ValidateAgents(path, cfg.Agents); err != nil {
		return err
	}
	return ValidateSupervisor(path, cfg.Supervisor)
}

func ReadShared(configDir string) (string, error) {
	data, err := os.ReadFile(filepath.Join(configDir, "config.yml"))
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	var shared Shared
	if err := yaml.Unmarshal(data, &shared); err != nil {
		return "", err
	}
	return shared.canonical()
}

func (s Shared) canonical() (string, error) {
	if reflect.DeepEqual(s, Shared{}) {
		return "", nil
	}
	data, err := yaml.Marshal(s)
	return string(data), err
}

func WriteShared(configDir, content string) (string, error) {
	var shared Shared
	if err := yaml.Unmarshal([]byte(content), &shared); err != nil {
		return "", err
	}
	if err := shared.validate("the shared settings"); err != nil {
		return "", err
	}
	err := rewriteConfig(configDir, func(path string, data []byte) ([]byte, error) {
		if len(bytes.TrimSpace(data)) == 0 {
			return yaml.Marshal(shared)
		}
		file, err := parser.ParseBytes(data, parser.ParseComments)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		if holdsOnlyComments(file) {
			settings, err := yaml.Marshal(shared)
			return append(append(bytes.TrimRight(data, "\n"), '\n'), settings...), err
		}
		for _, entry := range shared.entries() {
			if entry.zero {
				removeKey(file, entry.name)
				continue
			}
			if err := setKey(file, entry.name, entry.value, false); err != nil {
				return nil, err
			}
		}
		return []byte(file.String()), nil
	})
	if err != nil {
		return "", err
	}
	return ReadShared(configDir)
}

func (c *Config) TakeShared(from *Config) {
	c.StalledAfter, c.Pipelines, c.DefaultPipeline = from.StalledAfter, from.Pipelines, from.DefaultPipeline
	c.Agents, c.Supervisor, c.Codex = from.Agents, from.Supervisor, from.Codex
}
