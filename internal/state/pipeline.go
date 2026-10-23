package state

import (
	"fmt"
	"slices"
	"sort"
	"strings"
)

const DefaultPipeline = "full"

var builtinPipelines = map[string][]string{
	"full":  StageKeys,
	"quick": {"requirements", "plan", "implement", "review", "handoff"},
}

var alwaysRun = []string{"requirements", "handoff"}

type Pipeline struct {
	Name   string   `json:"name"`
	Stages []string `json:"stages"`
}

type Pipelines struct {
	List    []Pipeline
	Default string
}

func (ps Pipelines) Names() []string {
	names := make([]string, len(ps.List))
	for i, p := range ps.List {
		names[i] = p.Name
	}
	return names
}

func (ps Pipelines) Find(name string) (Pipeline, error) {
	if name == "" {
		name = ps.Default
	}
	for _, p := range ps.List {
		if p.Name == name {
			return p, nil
		}
	}
	return Pipeline{}, fmt.Errorf("no pipeline is called %q; available: %s", name, strings.Join(ps.Names(), ", "))
}

func validatePipelines(path string, pipelines map[string][]string) error {
	for _, name := range sortedNames(pipelines) {
		for _, stage := range pipelines[name] {
			if !slices.Contains(StageKeys, stage) {
				return fmt.Errorf("%s: pipelines.%s has the unknown stage %q; stages are %s", path, name, stage, strings.Join(StageKeys, ", "))
			}
		}
	}
	return nil
}

func ResolvePipelines(cfg *Config, profile Profile, profilePath string) (Pipelines, error) {
	if err := validatePipelines(profilePath, profile.Pipelines); err != nil {
		return Pipelines{}, err
	}
	merged := map[string][]string{}
	for _, layer := range []map[string][]string{builtinPipelines, cfg.Pipelines, profile.Pipelines} {
		for name, stages := range layer {
			merged[name] = stages
		}
	}
	ps := Pipelines{Default: DefaultPipeline}
	for _, source := range []struct{ path, name string }{{cfg.ConfigFile(), cfg.DefaultPipeline}, {profilePath, profile.DefaultPipeline}} {
		if source.name == "" {
			continue
		}
		if _, ok := merged[source.name]; !ok {
			return Pipelines{}, fmt.Errorf("%s: default_pipeline %q names no pipeline; available: %s", source.path, source.name, strings.Join(sortedNames(merged), ", "))
		}
		ps.Default = source.name
	}
	dropCodeRabbit := profile.CodeRabbit != nil && !*profile.CodeRabbit
	for _, name := range sortedNames(merged) {
		ps.List = append(ps.List, Pipeline{Name: name, Stages: effectiveStages(merged[name], dropCodeRabbit)})
	}
	return ps, nil
}

func effectiveStages(chosen []string, dropCodeRabbit bool) []string {
	var stages []string
	for _, key := range StageKeys {
		wanted := slices.Contains(chosen, key) || slices.Contains(alwaysRun, key)
		if wanted && !(key == "coderabbit" && dropCodeRabbit) {
			stages = append(stages, key)
		}
	}
	return stages
}

func sortedNames(pipelines map[string][]string) []string {
	var names []string
	for name := range pipelines {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool {
		bi, bj := builtinRank(names[i]), builtinRank(names[j])
		if bi != bj {
			return bi < bj
		}
		return names[i] < names[j]
	})
	return names
}

func builtinRank(name string) int {
	switch name {
	case "full":
		return 0
	case "quick":
		return 1
	}
	return 2
}

func (c *Config) PipelinesFor(repo string) (Pipelines, error) {
	profile, err := ReadProfile(repo)
	if err != nil {
		return Pipelines{}, fmt.Errorf("%s: %w", ProfilePath(repo), err)
	}
	return ResolvePipelines(c, profile, ProfilePath(repo))
}
