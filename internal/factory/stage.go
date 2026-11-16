package factory

import (
	"os"
	"os/exec"
	"slices"
	"strings"

	"github.com/afonsodelgado/factory/internal/state"
)

const BrowserTools = "mcp__chrome-devtools"

const openCodeQuiet = `OPENCODE_CONFIG_CONTENT='{"autoupdate":false}'`

type StageLaunch struct {
	Setting      state.AgentSetting
	Kickoff      string
	SessionID    string
	MCPConfig    string
	AddDirs      []string
	AddCwdGitDir bool
}

func (s StageLaunch) Command() string {
	switch s.Setting.ToolName() {
	case state.ToolCodex:
		return s.codex()
	case state.ToolOpenCode:
		return s.openCode()
	}
	return s.claude()
}

func (s StageLaunch) TypesKickoffAfterStart() bool {
	return s.Setting.ToolName() == state.ToolOpenCode && s.SessionID != ""
}

func (s StageLaunch) claude() string {
	parts := []string{"claude"}
	if s.SessionID != "" {
		parts = append(parts, "--resume", s.SessionID)
	}
	parts = append(parts, option("--model", s.Setting.Model)...)
	parts = append(parts, option("--effort", s.Setting.Effort)...)
	parts = append(parts, ShellQuote(s.Kickoff))
	if s.MCPConfig != "" {
		parts = append(parts, "--mcp-config", ShellQuote(s.MCPConfig), "--allowedTools", BrowserTools)
	}
	return strings.Join(parts, " ")
}

func (s StageLaunch) codex() string {
	parts := []string{"codex"}
	if s.SessionID != "" {
		parts = append(parts, "resume", s.SessionID)
	}
	parts = append(parts, option("-m", s.Setting.Model)...)
	if s.Setting.Effort != "" {
		parts = append(parts, "-c", "model_reasoning_effort="+s.Setting.Effort)
	}
	parts = append(parts, CodexSandbox(s.AddDirs)...)
	if s.AddCwdGitDir {
		parts = append(parts, "--add-dir", `"$(git rev-parse --path-format=absolute --git-dir)"`)
	}
	return strings.Join(append(parts, ShellQuote(s.Kickoff)), " ")
}

func CodexSandbox(addDirs []string) []string {
	flags := []string{"-a", "never", "-s", "workspace-write", "-c", "sandbox_workspace_write.network_access=true", "--dangerously-bypass-hook-trust"}
	for _, dir := range addDirs {
		flags = append(flags, "--add-dir", ShellQuote(dir))
	}
	return flags
}

func (s StageLaunch) openCode() string {
	parts := []string{openCodeQuiet, "opencode"}
	if s.SessionID != "" {
		parts = append(parts, "--session", s.SessionID)
	}
	if s.Setting.Model != "" {
		parts = append(parts, "-m", "opencode/"+s.Setting.Model)
	}
	parts = append(parts, option("--variant", s.Setting.Effort)...)
	parts = append(parts, "--auto")
	if !s.TypesKickoffAfterStart() {
		parts = append(parts, "--prompt", ShellQuote(s.Kickoff))
	}
	return strings.Join(parts, " ")
}

func option(flag, value string) []string {
	if value == "" {
		return nil
	}
	return []string{flag, value}
}

func CodexWritableDirs(checkout string, extra []string) []string {
	var dirs []string
	if out, err := exec.Command("git", "-C", checkout, "rev-parse", "--path-format=absolute", "--git-common-dir", "--git-dir").Output(); err == nil {
		dirs = append(dirs, strings.Split(strings.TrimSpace(string(out)), "\n")...)
	}
	dirs = append(dirs, toolOutput("go", "env", "GOCACHE", "GOMODCACHE")...)
	dirs = append(dirs, toolOutput("npm", "config", "get", "cache")...)
	for _, dir := range extra {
		dirs = append(dirs, state.ExpandHome(dir))
	}
	var existing []string
	for _, dir := range dirs {
		if info, err := os.Stat(dir); err == nil && info.IsDir() && !slices.Contains(existing, dir) {
			existing = append(existing, dir)
		}
	}
	return existing
}

func toolOutput(tool string, args ...string) []string {
	if _, err := exec.LookPath(tool); err != nil {
		return nil
	}
	out, err := exec.Command(tool, args...).Output()
	if err != nil {
		return nil
	}
	return strings.Fields(string(out))
}
