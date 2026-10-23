package factory

import (
	"fmt"
	"path/filepath"
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
}

func (l Launch) flags() []string {
	var flags []string
	if l.Chrome {
		flags = append(flags, "--chrome")
	}
	if l.RemoteControl {
		flags = append(flags, "--remote-control", l.Slug)
	}
	return flags
}

func (l Launch) Message() string {
	message := "/factory " + l.Slug
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

func (l Launch) CreateCommand() string {
	parts := append([]string{"claude", "--worktree", l.Slug, "--session-id", l.SessionID}, l.flags()...)
	worktree := filepath.Join(l.Repo, ".claude", "worktrees", l.Slug)
	return strings.Join(parts, " ") + " " + ShellQuote(l.Message()) + "; cd " + ShellQuote(worktree)
}

func (l Launch) ResumeCommand() string {
	parts := []string{"claude"}
	if l.SessionID == "" {
		parts = append(parts, "--continue")
	} else {
		parts = append(parts, "--resume", l.SessionID)
	}
	parts = append(parts, l.flags()...)
	return strings.Join(parts, " ") + " " + ShellQuote("/factory resume")
}

func ShellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
