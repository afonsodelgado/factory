package state

import (
	"fmt"
	"slices"
	"sort"
	"strings"
)

const (
	DefaultPipeline = "full"
	CustomPipeline  = "custom"
)

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
	repo    RepoSupport
}

type RepoSupport struct {
	CodeRabbit bool
	BrowserQA  bool
}

func (p Profile) Support() RepoSupport {
	return RepoSupport{CodeRabbit: p.CodeRabbit == nil || *p.CodeRabbit, BrowserQA: p.BrowserQA()}
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

func (ps Pipelines) Custom(stages []string) (Pipeline, error) {
	if err := checkStages("--stages", stages); err != nil {
		return Pipeline{}, err
	}
	return Pipeline{Name: CustomPipeline, Stages: EffectiveStages(stages, ps.repo)}, nil
}

func checkStages(source string, stages []string) error {
	for _, stage := range stages {
		if !slices.Contains(StageKeys, stage) {
			return fmt.Errorf("%s has the unknown stage %q; stages are %s", source, stage, strings.Join(StageKeys, ", "))
		}
	}
	return nil
}

func validatePipelines(path string, pipelines map[string][]string) error {
	for _, name := range sortedNames(pipelines) {
		if name == CustomPipeline {
			return fmt.Errorf("%s: pipelines.%s is reserved for a factory's own list of stages; give the preset another name", path, name)
		}
		if err := checkStages(fmt.Sprintf("%s: pipelines.%s", path, name), pipelines[name]); err != nil {
			return err
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
	ps := Pipelines{Default: DefaultPipeline, repo: profile.Support()}
	for _, source := range []struct{ path, name string }{{cfg.ConfigFile(), cfg.DefaultPipeline}, {profilePath, profile.DefaultPipeline}} {
		if source.name == "" {
			continue
		}
		if _, ok := merged[source.name]; !ok {
			return Pipelines{}, fmt.Errorf("%s: default_pipeline %q names no pipeline; available: %s", source.path, source.name, strings.Join(sortedNames(merged), ", "))
		}
		ps.Default = source.name
	}
	for _, name := range sortedNames(merged) {
		ps.List = append(ps.List, Pipeline{Name: name, Stages: EffectiveStages(merged[name], ps.repo)})
	}
	return ps, nil
}

func EffectiveStages(chosen []string, repo RepoSupport) []string {
	withoutQA := !slices.Contains(chosen, "qa")
	var stages []string
	for _, key := range StageKeys {
		wanted := slices.Contains(chosen, key) || slices.Contains(alwaysRun, key)
		removed := (withoutQA && dependsOnQA(key)) || !repo.Offers(key)
		if wanted && !removed {
			stages = append(stages, key)
		}
	}
	return stages
}

func (r RepoSupport) Offers(stage string) bool {
	switch stage {
	case "coderabbit":
		return r.CodeRabbit
	case "walkthrough":
		return r.BrowserQA
	}
	return true
}

func AlwaysRuns(stage string) bool { return slices.Contains(alwaysRun, stage) }

func DependsOnQA(stage string) bool { return dependsOnQA(stage) }

func dependsOnQA(stage string) bool {
	return stage == "qa_fixes" || stage == "test_gaps"
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
