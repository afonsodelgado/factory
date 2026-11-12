package factory

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/afonsodelgado/factory/internal/state"
)

func Slugify(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(s) {
		alnum := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		if alnum {
			b.WriteRune(r)
			dash = false
			continue
		}
		if !dash && b.Len() > 0 {
			b.WriteByte('-')
			dash = true
		}
	}
	return strings.TrimSuffix(b.String(), "-")
}

var ReservedIDs = []string{"open", "init", "doctor", "bootstrap", "install-hooks", "version", "update", "hub", "view", "dialog", "hook", "host", "remote", "help", "delete", "forget", "stage", "review", "-h", "--help", "--version"}

func IsReservedID(name string) bool {
	return slices.Contains(ReservedIDs, name) || slices.Contains(ReservedIDs, Slugify(name))
}

func ReservedIDRefusal(name string) string {
	id := Slugify(name)
	return fmt.Sprintf("`%s` is a factory command, not an id. Pick another id, such as %s-fixes.", strings.TrimSpace(name), id)
}

func FreshSlug(slug string, taken func(string) bool) string {
	fresh := slug
	for n := 2; taken(fresh); n++ {
		fresh = fmt.Sprintf("%s-%d", slug, n)
	}
	return fresh
}

type Launch struct {
	Repo          string
	Slug          string
	Prompt        string
	Pipeline      string
	Stages        []string
	Agents        map[string]state.AgentSetting
	SessionID     string
	Chrome        bool
	RemoteControl bool
	Supervisor    state.AgentSetting
	AddDirs       []string
}

func (l Launch) flags() []string {
	flags := option("--model", l.Supervisor.Model)
	flags = append(flags, option("--effort", l.Supervisor.Effort)...)
	if l.Chrome {
		flags = append(flags, "--chrome")
	}
	if l.RemoteControl {
		flags = append(flags, "--remote-control", l.Slug)
	}
	return flags
}

const openCodeSkillCommand = "Use the factory skill for: /factory"

func SkillCommand(tool string) string {
	switch tool {
	case state.ToolCodex:
		return "$factory"
	case state.ToolOpenCode:
		return openCodeSkillCommand
	}
	return "/factory"
}

func ForSupervisor(tool, message string) string {
	if rest, ok := strings.CutPrefix(message, "/factory"); ok {
		return SkillCommand(tool) + rest
	}
	return message
}

func (l Launch) Message() string {
	message := SkillCommand(l.Supervisor.ToolName()) + " " + l.Slug
	switch {
	case len(l.Stages) > 0:
		message += " --stages " + strings.Join(l.Stages, ",")
	case l.Pipeline != "":
		message += " --pipeline " + l.Pipeline
	}
	for _, role := range state.AgentRoles {
		if setting, ok := l.Agents[role]; ok {
			message += " --agent " + setting.Flag(role)
		}
	}
	if strings.TrimSpace(l.Prompt) != "" {
		message += "\n" + strings.TrimRight(l.Prompt, "\n")
	}
	return message
}

func (l Launch) worktree() string {
	return filepath.Join(WorktreesDir(l.Repo), l.Slug)
}

func (l Launch) CreateCommand() string {
	if !l.Supervisor.OnClaude() {
		supervisor := l.supervisorLaunch("", l.Message())
		supervisor.AddCwdGitDir = true
		return l.addWorktree() + "\ncd " + ShellQuote(l.worktree()) + " && " + supervisor.Command()
	}
	parts := append([]string{"claude", "--worktree", l.Slug, "--session-id", l.SessionID}, l.flags()...)
	return strings.Join(parts, " ") + " " + ShellQuote(l.Message()) + "; cd " + ShellQuote(l.worktree())
}

func (l Launch) addWorktree() string {
	add := "git -C " + ShellQuote(l.Repo) + " worktree add -b worktree-" + l.Slug + " " + ShellQuote(l.worktree())
	return add + " origin/HEAD || " + add
}

func (l Launch) ResumeMessage() string {
	return SkillCommand(l.Supervisor.ToolName()) + " resume"
}

func (l Launch) ResumeCommand() string {
	if !l.Supervisor.OnClaude() {
		return l.supervisor(l.SessionID, l.ResumeMessage())
	}
	parts := []string{"claude"}
	if l.SessionID == "" {
		parts = append(parts, "--continue")
	} else {
		parts = append(parts, "--resume", l.SessionID)
	}
	parts = append(parts, l.flags()...)
	return strings.Join(parts, " ") + " " + ShellQuote(l.ResumeMessage())
}

func (l Launch) TypesResumeAfterStart() bool {
	return l.supervisorLaunch(l.SessionID, l.ResumeMessage()).TypesKickoffAfterStart()
}

func (l Launch) supervisor(sessionID, message string) string {
	return l.supervisorLaunch(sessionID, message).Command()
}

func (l Launch) supervisorLaunch(sessionID, message string) StageLaunch {
	return StageLaunch{Setting: l.Supervisor, Kickoff: message, SessionID: sessionID, AddDirs: l.AddDirs}
}

func ShellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
