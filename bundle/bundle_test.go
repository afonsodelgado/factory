package bundle

import (
	"bytes"
	"context"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/goccy/go-yaml"

	"github.com/afonsodelgado/factory/internal/state"
)

type frontmatter struct {
	Name   string   `yaml:"name"`
	Skills []string `yaml:"skills"`
	Model  string   `yaml:"model"`
	Effort string   `yaml:"effort"`
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
	if len(agents) != 7 {
		t.Fatalf("bundled agents = %d, want 7", len(agents))
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

func TestNoBundledInstructionAsksForVisibleChromeOrItsExtension(t *testing.T) {
	visible := regexp.MustCompile(`--chrome\b|claude-in-chrome|Claude in Chrome|chrome extension`)
	fs.WalkDir(Files, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || strings.HasPrefix(path, "pwa/") {
			return err
		}
		data, _ := fs.ReadFile(Files, path)
		if found := visible.Find(data); found != nil {
			t.Errorf("%s still says %q; QA drives only headless Chrome through Chrome DevTools MCP", path, found)
		}
		return nil
	})
}

func TestNoCommandConfigOrDocStillOffersTheOldSSHRemotes(t *testing.T) {
	removed := regexp.MustCompile(`factory remote\b|host request|\bntfy\b|default_remote|--remote(\s|$)|\bRemotes\b|^\s*remotes:`)
	migration := map[string]string{
		"README.md":                       "Upgrading from a release with ssh remotes",
		"cmd/factory/main.go":             "ssh remotes are gone",
		"internal/state/machine.go":       "",
		"internal/state/config_test.go":   "",
		"bundle/skills/gh-stack/SKILL.md": "",
	}
	root := ".."
	for _, top := range []string{"cmd", "internal", "bundle", "apps/pwa/src", "README.md", "CLAUDE.md"} {
		err := filepath.WalkDir(filepath.Join(root, top), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(root, path)
			switch {
			case d.IsDir() && rel == filepath.Join("bundle", "pwa"):
				return fs.SkipDir
			case d.IsDir() || rel == filepath.Join("bundle", "bundle_test.go"):
				return nil
			}
			data, err := os.ReadFile(path)
			for i, line := range strings.Split(string(data), "\n") {
				context, allowed := migration[filepath.ToSlash(rel)]
				if found := removed.FindString(line); found != "" && !(allowed && strings.Contains(line, context)) {
					t.Errorf("%s:%d still says %q; the ssh remotes, their tunnels and ntfy push are gone", rel, i+1, found)
				}
			}
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestDefaultAgentsMatchTheBundledFrontmatter(t *testing.T) {
	for _, role := range state.AgentRoles {
		fm := readFrontmatter(t, "agents/factory-"+role+".md")
		if got := (state.AgentSetting{Model: fm.Model, Effort: fm.Effort}); got != state.DefaultAgents[role] {
			t.Errorf("factory-%s.md says %+v, but the new-factory dialog shows %+v as its default", role, got, state.DefaultAgents[role])
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

func TestWaitForReviewCountsOpenCodeRabbitThreads(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil && os.Getenv("CI") == "" {
		t.Skip("jq not installed")
	}
	stubs := t.TempDir()
	stub := `#!/bin/sh
case "$1" in
pr) echo '[{"name":"CodeRabbit","bucket":"pass"},{"name":"build","bucket":"pass"}]' ;;
api) cat <<'JSON'
{"data":{"repository":{"pullRequest":{"reviewThreads":{"nodes":[
{"isResolved":false,"isOutdated":false,"comments":{"nodes":[{"author":{"login":"coderabbitai"}}]}},
{"isResolved":true,"isOutdated":false,"comments":{"nodes":[{"author":{"login":"coderabbitai"}}]}},
{"isResolved":false,"isOutdated":true,"comments":{"nodes":[{"author":{"login":"coderabbitai"}}]}},
{"isResolved":false,"isOutdated":false,"comments":{"nodes":[{"author":{"login":"a-teammate"}}]}}
]}}}}}
JSON
;;
esac
`
	if err := os.WriteFile(filepath.Join(stubs, "gh"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", "skills/factory/scripts/wait-for-review.sh", "7")
	cmd.Env = append(os.Environ(), "PATH="+stubs+string(os.PathListSeparator)+os.Getenv("PATH"))
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("wait-for-review.sh: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "PR 7: coderabbit=pass ci=pass threads=1") {
		t.Errorf("output = %q, want one open CodeRabbit thread: a passing check must not hide threads the shepherd has to triage", out)
	}
}

func TestSupervisorCommitRejectsStaleProposalsAndPreservesMetadata(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	proposal := filepath.Join(dir, "proposal.json")
	os.WriteFile(path, []byte(`{"schema_version":2,"revision":0,"incarnation":"one","holder_generation":1,"metadata":{"keep":true},"processed_events":[]}`), 0600)
	os.WriteFile(proposal, []byte(`{"expected_revision":0,"incarnation":"one","holder_generation":1,"snapshot":{"schema_version":2,"revision":1,"incarnation":"one","holder_generation":1,"processed_events":["event"]}}`), 0600)
	script := filepath.Join("skills", "factory", "scripts", "commit-state.py")
	for range 2 {
		if out, err := exec.Command(python, script, path, proposal, "supervisor").CombinedOutput(); err != nil {
			t.Fatalf("%v: %s", err, out)
		}
	}
	data, _ := os.ReadFile(path)
	if !bytes.Contains(data, []byte(`"keep": true`)) || !bytes.Contains(data, []byte(`"session_id": "supervisor"`)) {
		t.Fatal(string(data))
	}
	os.WriteFile(proposal, []byte(`{"expected_revision":0,"incarnation":"one","holder_generation":1,"snapshot":{"schema_version":2,"revision":1,"incarnation":"one","holder_generation":1,"processed_events":["other"]}}`), 0600)
	if out, err := exec.Command(python, script, path, proposal, "supervisor").CombinedOutput(); err == nil {
		t.Fatalf("stale commit accepted: %s", out)
	}
}
