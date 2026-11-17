package state

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

func TestRemovedStagesAreRejectedBeforeSchemaInEveryStateLocation(t *testing.T) {
	for _, old := range []string{"coderabbit", "qa_fixes", "test_gaps", "walkthrough"} {
		for _, patch := range []map[string]any{
			{"stage": old}, {"stages": map[string]any{old: map[string]any{}}}, {"pipeline_stages": []string{old}},
			{"history": []map[string]string{{"stage": old}}}, {"attention": []map[string]string{{"stage": old}}},
		} {
			data, _ := json.Marshal(patch)
			if _, err := ParseState(data); err == nil || !strings.Contains(err.Error(), "unknown stage") {
				t.Fatalf("%s: %v", data, err)
			}
		}
		for _, scope := range []string{"user", "repo"} {
			cfg, profile := &Config{}, Profile{}
			if scope == "user" {
				cfg.Pipelines = map[string][]string{"bad": {old}}
			} else {
				profile.Pipelines = map[string][]string{"bad": {old}}
			}
			if _, err := ResolvePipelines(cfg, profile, "repo"); err == nil || !strings.Contains(err.Error(), "unknown stage") {
				t.Fatalf("%s %s: %v", scope, old, err)
			}
		}
	}
	if _, err := ParseState([]byte(`{"ticket":"legacy"}`)); err == nil {
		t.Fatal("legacy state accepted")
	}
	for _, role := range []string{"shepherd", "fixer"} {
		if _, err := ParseState([]byte(`{"schema_version":2,"agents":{"` + role + `":{}}}`)); err == nil {
			t.Fatal("removed role accepted")
		}
	}
	if !slices.Equal(StageKeys, []string{"requirements", "design", "plan", "implement", "review", "qa", "pr", "handoff"}) || len(AgentRoles) != 7 {
		t.Fatal(StageKeys, AgentRoles)
	}
	s := State{Stage: "pr", Stages: map[string]Stage{"review": {Status: "done"}}, Attention: []Attention{{ID: "open-review", StageKey: "review", Kind: "cap"}}}
	if len(s.OpenAttention()) != 1 {
		t.Fatal("backwards-loop attention hidden")
	}
}
