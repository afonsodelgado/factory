package bundle

import (
	"bytes"
	"context"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/goccy/go-yaml"

	"github.com/afonsodelgado/factory/internal/state"
)

type frontmatter struct {
	Name   string   `yaml:"name"`
	Skills []string `yaml:"skills"`
}

func readFrontmatter(t *testing.T, path string) frontmatter {
	t.Helper()
	data, err := fs.ReadFile(Files, path)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.SplitN(string(data), "---\n", 3)
	if len(parts) < 3 {
		t.Fatalf("%s has no frontmatter", path)
	}
	var fm frontmatter
	if err := yaml.Unmarshal([]byte(parts[1]), &fm); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return fm
}

func TestEveryPreloadedSkillIsBundled(t *testing.T) {
	agents, err := fs.Glob(Files, "agents/factory-*.md")
	if err != nil {
		t.Fatal(err)
	}
	if len(agents) != 8 {
		t.Fatalf("bundled agents = %d, want 8", len(agents))
	}
	for _, agent := range agents {
		if body, _ := fs.ReadFile(Files, agent); !bytes.Contains(body, []byte("in a `misrouted-message` fenced block")) {
			t.Errorf("%s has no misrouted-message rule: a panel answer that reaches it instead of the supervisor is lost", agent)
		}
		for _, skill := range readFrontmatter(t, agent).Skills {
			path := "skills/" + skill + "/SKILL.md"
			if _, err := fs.Stat(Files, path); err != nil {
				t.Errorf("%s preloads %q, but %s is not bundled", agent, skill, path)
				continue
			}
			if name := readFrontmatter(t, path).Name; name != skill {
				t.Errorf("%s preloads %q, but %s is named %q", agent, skill, path, name)
			}
		}
	}
}

func TestStateTemplateReadsAsTheNewFormat(t *testing.T) {
	data, err := fs.ReadFile(Files, "skills/factory/templates/state.json")
	if err != nil {
		t.Fatal(err)
	}
	st, err := state.ParseState(data)
	if err != nil {
		t.Fatalf("the template does not parse into state.State: %v", err)
	}
	if !st.HasAttentionField() {
		t.Error("a factory started from the template would be read as the old format and fall back to the idle heuristic")
	}
	if len(st.Stages) != len(state.StageKeys) {
		t.Errorf("template has %d stages, the app tracks %d", len(st.Stages), len(state.StageKeys))
	}
}

func TestWaitForReviewCountsPollsNotWallClock(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil && os.Getenv("CI") == "" {
		t.Skip("jq not installed")
	}
	stubs := t.TempDir()
	factoryDir := t.TempDir()
	calls := filepath.Join(stubs, "calls")
	stub := "#!/bin/sh\necho x >> \"$GH_CALLS\"\necho '[]'\n"
	if err := os.WriteFile(filepath.Join(stubs, "gh"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", "skills/factory/scripts/wait-for-review.sh", "--dir", factoryDir, "7")
	cmd.Env = append(os.Environ(),
		"PATH="+stubs+string(os.PathListSeparator)+os.Getenv("PATH"),
		"GH_CALLS="+calls,
		"FACTORY_POLL_SECONDS=60",
		"FACTORY_SLEEP_SECONDS=0",
		"FACTORY_NO_CHECKS_MINUTES=3",
	)
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("wait-for-review.sh never reported no-checks: elapsed time is not counted from polls\n%s", out)
	}
	if err != nil {
		t.Fatalf("wait-for-review.sh: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "PR 7: no-checks after 3m") {
		t.Errorf("output = %q, want a no-checks report after 3m", out)
	}
	polled, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	if n := bytes.Count(polled, []byte("x")); n != 4 {
		t.Errorf("gh polled %d times, want 4 (minutes 0 to 3)", n)
	}
	if _, err := os.Stat(filepath.Join(factoryDir, ".heartbeat")); err != nil {
		t.Errorf("no heartbeat written: %v", err)
	}
}
