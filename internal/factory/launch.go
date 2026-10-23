package factory

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"
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

var ReservedIDs = []string{"open", "init", "doctor", "bootstrap", "install-hooks", "version", "update", "hub", "view", "dialog", "hook", "help", "delete", "-h", "--help", "--version"}

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
	if l.Pipeline != "" {
		message += " --pipeline " + l.Pipeline
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
