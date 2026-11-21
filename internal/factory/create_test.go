package factory

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/afonsodelgado/factory/internal/state"
)

func TestCreateCommand(t *testing.T) {
	noPrompt := Launch{Repo: "/r/cowork", Slug: "eng-740", SessionID: "5f0c9d2e"}
	want := "claude --worktree eng-740 --session-id 5f0c9d2e '/factory eng-740 --comments off'; cd '/r/cowork/.claude/worktrees/eng-740'"
	if got := noPrompt.CreateCommand(); got != want {
		t.Errorf("factory without a prompt runs =\n%s\nwant\n%s", got, want)
	}

	promptInTerminalRepo := Launch{Repo: "/r/factory", Slug: "csv-export", Prompt: "Export contacts as CSV.\nIt's for ops, who'd rather not wait.\n", Pipeline: "quick", SessionID: "2b7e4c1a"}
	want = "claude --worktree csv-export --session-id 2b7e4c1a '/factory csv-export --pipeline quick --comments off\nExport contacts as CSV.\nIt'\\''s for ops, who'\\''d rather not wait.'; cd '/r/factory/.claude/worktrees/csv-export'"
	if got := promptInTerminalRepo.CreateCommand(); got != want {
		t.Errorf("factory with a prompt and the quick pipeline runs =\n%s\nwant\n%s", got, want)
	}

	customWithCheaperAgents := Launch{Repo: "/r/factory", Slug: "csv-export", Prompt: "Export contacts.", Pipeline: state.CustomPipeline, Stages: []string{"requirements", "plan", "implement", "handoff"}, Agents: map[string]state.AgentSetting{"reviewer": {Effort: "low"}, "engineer": {Model: "sonnet", Effort: "medium"}}, SessionID: "2b7e4c1a", WatchComments: true}
	want = "claude --worktree csv-export --session-id 2b7e4c1a '/factory csv-export --stages requirements,plan,implement,handoff --agent engineer=sonnet:medium --agent reviewer=:low --comments on\nExport contacts.'; cd '/r/factory/.claude/worktrees/csv-export'"
	if got := customWithCheaperAgents.CreateCommand(); got != want {
		t.Errorf("factory with its own stages and agent overrides runs =\n%s\nwant\n%s\nthe skill parses this first line, so its shape is a contract", got, want)
	}
	onCodex := Launch{Repo: "/r/factory", Slug: "csv-export", Prompt: "Export contacts.", Supervisor: state.AgentSetting{Tool: state.ToolCodex, Model: "gpt-5.6-sol", Effort: "high"}, AddDirs: []string{"/r/factory/.git"}}
	want = "git -C '/r/factory' worktree add -b worktree-csv-export '/r/factory/.claude/worktrees/csv-export' origin/HEAD || git -C '/r/factory' worktree add -b worktree-csv-export '/r/factory/.claude/worktrees/csv-export'\ncd '/r/factory/.claude/worktrees/csv-export' && codex -m gpt-5.6-sol -c model_reasoning_effort=high -a never -s workspace-write -c sandbox_workspace_write.network_access=true --dangerously-bypass-hook-trust --add-dir '/r/factory/.git' --add-dir \"$(git rev-parse --path-format=absolute --git-dir)\" '$factory csv-export --comments off\nExport contacts.'"
	if got := onCodex.CreateCommand(); got != want {
		t.Errorf("a Codex supervisor runs =\n%s\nwant\n%s\nCodex refuses a /factory line, and without the worktree branch delete cannot clean up", got, want)
	}
	onOpenCode := Launch{Repo: "/r/factory", Slug: "csv-export", Prompt: "Export contacts.", Supervisor: state.AgentSetting{Tool: state.ToolOpenCode, Model: "big-pickle"}}
	wantKickoff := ShellQuote("Use the factory skill for: /factory csv-export --comments off\nExport contacts.")
	if got := onOpenCode.CreateCommand(); !strings.HasSuffix(got, "--prompt "+wantKickoff) {
		t.Errorf("an OpenCode supervisor runs\n%s\nwant it to end with --prompt %s: OpenCode has no /factory command, so the bare line would never load the skill", got, wantKickoff)
	}
	if got := ForSupervisor(state.ToolOpenCode, "/factory resume"); got != "Use the factory skill for: /factory resume" {
		t.Errorf("a panel message reaches OpenCode as %q; want it to name the skill", got)
	}
}

func TestCodexInAWorktreeCanWriteTheWorktreesOwnGitDir(t *testing.T) {
	for k, v := range map[string]string{"GIT_AUTHOR_NAME": "t", "GIT_AUTHOR_EMAIL": "t@example.com", "GIT_COMMITTER_NAME": "t", "GIT_COMMITTER_EMAIL": "t@example.com", "GIT_CONFIG_GLOBAL": "/dev/null", "GIT_CONFIG_SYSTEM": "/dev/null"} {
		t.Setenv(k, v)
	}
	bin := t.TempDir()
	args := filepath.Join(bin, "args")
	writeFile(t, filepath.Join(bin, "codex"), "#!/bin/sh\necho \"$@\" > "+args+"\n")
	os.Chmod(filepath.Join(bin, "codex"), 0o755)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	repo, _ := filepath.EvalSymlinks(t.TempDir())
	gitRun(t, repo, "init", "-q")
	gitRun(t, repo, "commit", "-q", "--allow-empty", "-m", "init")
	launch := Launch{Repo: repo, Slug: "csv", Supervisor: state.AgentSetting{Tool: state.ToolCodex}}
	gitDir := filepath.Join(repo, ".git", "worktrees", "csv")

	if out, err := exec.Command("sh", "-c", launch.CreateCommand()).CombinedOutput(); err != nil {
		t.Fatalf("create command: %v\n%s", err, out)
	}

	if got, _ := os.ReadFile(args); !strings.Contains(string(got), "--add-dir "+gitDir+" ") {
		t.Errorf("a new Codex supervisor ran codex %q; want --add-dir %s, or it cannot commit in its own worktree", got, gitDir)
	}
	if dirs := CodexWritableDirs(launch.worktree(), nil); !slices.Contains(dirs, gitDir) {
		t.Errorf("codex writable dirs %q lack %s; Codex keeps a worktree's gitdir read-only unless it is named, so stages cannot commit, rebase or run gh stack", dirs, gitDir)
	}
}

func TestStagesGetTheMainCheckoutsIgnoredEnvFilesWithoutOverwritingTheWorktrees(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	repo, _ := filepath.EvalSymlinks(t.TempDir())
	gitRun(t, repo, "init", "-q")
	writeFile(t, filepath.Join(repo, ".gitignore"), ".env*\nnode_modules/\n")
	writeFile(t, filepath.Join(repo, "apps", "server", ".env"), "OPENROUTER_API_KEY=main\n")
	writeFile(t, filepath.Join(repo, ".env.local"), "PORT=main\nOPENROUTER_API_KEY=stale\nexport OPENROUTER_API_KEY=main\nCERT=\"-----BEGIN\nab=cd\n-----END\"\n")
	writeFile(t, filepath.Join(repo, "node_modules", "pkg", ".env"), "VENDORED=1\n")
	worktree := filepath.Join(WorktreesDir(repo), "evals")
	writeFile(t, filepath.Join(worktree, ".env.local"), "PORT=worktree\n")

	if _, err := CopyEnvFiles(repo, worktree); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(worktree, "apps", "server", ".env")); string(got) != "OPENROUTER_API_KEY=main\n" {
		t.Errorf("the worktree's apps/server/.env reads %q; QA cannot reach keys the main checkout has", got)
	}
	if got, _ := os.ReadFile(filepath.Join(worktree, ".env.local")); string(got) != "PORT=worktree\nexport OPENROUTER_API_KEY=main\nCERT=\"-----BEGIN\nab=cd\n-----END\"\n" {
		t.Errorf("the worktree's stale .env.local reads %q; it must keep its own values and gain the keys only the main checkout has", got)
	}
	if nested, _ := filepath.Glob(filepath.Join(worktree, ".claude", "*")); len(nested) > 0 {
		t.Errorf("copying reached into the worktrees directory and made %v", nested)
	}
}
