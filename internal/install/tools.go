package install

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/afonsodelgado/factory/internal/catalog"
	"github.com/afonsodelgado/factory/internal/factory"
	"github.com/afonsodelgado/factory/internal/state"
)

func toolChecks(o Options) []Check {
	if o.Config == nil {
		return nil
	}
	var checks []Check
	if _, err := os.Stat(o.Config.RulesPath()); err != nil {
		checks = append(checks, Check{Name: "rules", Mark: Warn, Detail: "no rules file, so stage agents get only the repo's own rules", Fix: "write your coding rules to " + tilde(o.Config.RulesPath())})
	}
	used := toolsInUse(o)
	if repos, ok := used[state.ToolCodex]; ok {
		checks = append(checks, codexChecks(o, repos)...)
	}
	if _, ok := used[state.ToolOpenCode]; ok {
		checks = append(checks, openCodeChecks(o)...)
	}
	return append(checks, agentValueChecks(o)...)
}

func toolsInUse(o Options) map[string][]string {
	used := map[string][]string{}
	note := func(setting state.AgentSetting, repos ...string) {
		if tool := setting.ToolName(); tool != state.ToolClaude {
			used[tool] = append(used[tool], repos...)
		}
	}
	note(o.Config.Supervisor, o.Repos...)
	for _, setting := range o.Config.Agents {
		note(setting, o.Repos...)
	}
	for _, repo := range o.Repos {
		profile, _ := state.ReadProfile(repo)
		note(profile.Supervisor, repo)
		for _, setting := range profile.Agents {
			note(setting, repo)
		}
	}
	for _, entry := range liveEntries(o.Config) {
		note(entry.Supervisor, entry.Repo)
		for _, setting := range entry.Agents {
			note(setting, entry.Repo)
		}
	}
	for tool, repos := range used {
		slices.Sort(repos)
		used[tool] = slices.Compact(repos)
	}
	return used
}

func liveEntries(cfg *state.Config) []state.Entry {
	registry, err := state.LoadRegistry(cfg.RegistryPath())
	if err != nil {
		return nil
	}
	return slices.DeleteFunc(registry.Factories, func(e state.Entry) bool { return e.Archived })
}

func agentValueChecks(o Options) []Check {
	catalogs := catalog.Local(o.Config.StateDir)
	results := []error{
		catalogs.ValidateAgents(o.ConfigFile, o.Config.Agents),
		catalogs.Validate(o.ConfigFile, "supervisor", o.Config.Supervisor),
	}
	for _, repo := range o.Repos {
		profile, _ := state.ReadProfile(repo)
		results = append(results, catalogs.ValidateAgents(tilde(state.ProfilePath(repo)), profile.Agents), catalogs.Validate(tilde(state.ProfilePath(repo)), "supervisor", profile.Supervisor))
	}
	var checks []Check
	for _, err := range results {
		switch {
		case err == nil:
		case errors.Is(err, catalog.ErrUnavailable):
			checks = append(checks, Check{Name: "agents", Mark: Warn, Detail: err.Error()})
		default:
			checks = append(checks, Check{Name: "agents", Mark: Fail, Detail: "a stage would fail inside its pane", Lines: []string{err.Error()}, Fix: "pick a listed model and effort in that file"})
		}
	}
	return checks
}

func codexChecks(o Options, repos []string) []Check {
	checks := []Check{tool("codex", "0.156", "codex", []string{"--version"}, "npm install -g @openai/codex")}
	if checks[0].Mark == Fail {
		return checks
	}
	home := state.CodexHome()
	if _, err := catalog.Codex(home); err != nil {
		return append(checks, Check{Name: "codex login", Mark: Fail, Detail: err.Error(), Fix: "codex login"})
	}
	checks = append(checks, Check{Name: "codex login", Mark: Pass, Detail: "ChatGPT subscription"})
	var untrusted []string
	for _, repo := range repos {
		if !catalog.CodexTrusts(home, repo) {
			untrusted = append(untrusted, tilde(repo))
		}
	}
	if len(untrusted) > 0 {
		checks = append(checks, Check{Name: "codex trust", Mark: Fail, Detail: "Codex would stop at a trust prompt in " + strings.Join(untrusted, ", "), Fix: "run codex once in each repo, choose Trust and continue, then Trust all for the factory hooks"})
	}
	return append(checks, codexSandbox(o))
}

func codexSandbox(o Options) Check {
	dir, err := os.MkdirTemp("", "factory-sandbox-")
	if err != nil {
		return Check{Name: "codex sandbox", Mark: Fail, Detail: err.Error()}
	}
	defer os.RemoveAll(dir)
	dir, _ = filepath.EvalSymlinks(dir)
	repo, remote := filepath.Join(dir, "repo"), filepath.Join(dir, "remote.git")
	worktree := filepath.Join(factory.WorktreesDir(repo), "probe")
	setup := [][]string{{"init", "-q", "--bare", remote}, {"init", "-q", repo}, {"-C", repo, "remote", "add", "origin", remote}, {"-C", repo, "-c", "user.name=f", "-c", "user.email=f@f", "commit", "-q", "--allow-empty", "-m", "init"}, {"-C", repo, "worktree", "add", "-q", "-b", "probe", worktree}}
	for _, args := range setup {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			return Check{Name: "codex sandbox", Mark: Fail, Detail: "git " + args[0] + ": " + strings.TrimSpace(string(out))}
		}
	}
	probe := `git -c user.name=f -c user.email=f@f commit -q --allow-empty -m probe || { echo "commit is blocked"; exit 1; }
git push -q origin HEAD:refs/heads/probe || { echo "push is blocked"; exit 1; }
gh auth status >/dev/null 2>&1 || { echo "gh cannot reach its login"; exit 1; }`
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	roots := append(factory.CodexWritableDirs(worktree, o.Config.Codex.WritableDirs), remote)
	quoted := make([]string, len(roots))
	for i, root := range roots {
		quoted[i] = strconv.Quote(root)
	}
	cmd := exec.CommandContext(ctx, "codex", "sandbox", "-c", `sandbox_mode="workspace-write"`, "-c", "sandbox_workspace_write.network_access=true", "-c", "sandbox_workspace_write.writable_roots=["+strings.Join(quoted, ", ")+"]", "--", "sh", "-c", probe)
	cmd.Dir = worktree
	if out, err := cmd.CombinedOutput(); err != nil {
		return Check{Name: "codex sandbox", Mark: Fail, Detail: "a stage in Codex would stop: " + strings.TrimSpace(string(out)), Fix: "add the blocked directories to codex.writable_dirs in " + tilde(o.ConfigFile)}
	}
	return Check{Name: "codex sandbox", Mark: Pass, Detail: "commit, push and gh work without approvals"}
}

func openCodeChecks(o Options) []Check {
	checks := []Check{tool("opencode", "1.18", "opencode", []string{"--version"}, "curl -fsSL https://opencode.ai/install | bash")}
	if checks[0].Mark == Fail {
		return checks
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	catalog.RefreshOpenCode(ctx, o.Config.StateDir)
	models, err := catalog.OpenCode(o.Config.StateDir)
	if err != nil || len(models) == 0 {
		return append(checks, Check{Name: "opencode models", Mark: Fail, Detail: "no free models listed", Fix: "opencode models opencode"})
	}
	checks = append(checks, Check{Name: "opencode models", Mark: Pass, Detail: fmt.Sprintf("%d free models", len(models))})
	plan, err := NewOpenCodePlan(state.OpenCodeConfigDir(), o.Binary)
	if err != nil || len(plan.Pending()) > 0 {
		return append(checks, Check{Name: "opencode plugin", Mark: Fail, Detail: "OpenCode panes cannot report their status", Fix: "factory bootstrap"})
	}
	return append(checks, Check{Name: "opencode plugin", Mark: Pass, Detail: "installed"})
}
