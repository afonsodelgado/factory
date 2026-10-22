package factory

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

var ticketPattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9]*-[0-9]+$`)

func IsTicket(s string) bool {
	return ticketPattern.MatchString(s)
}

func ParseName(input string) (ticket, feature string) {
	input = strings.TrimSpace(input)
	head, tail, found := strings.Cut(input, "/")
	if IsTicket(head) {
		ticket = strings.ToUpper(head)
		if found {
			feature = Slugify(tail)
		}
		return ticket, feature
	}
	return "", Slugify(input)
}

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

func SlugFor(ticket, feature string) string {
	switch {
	case ticket != "" && feature != "":
		return strings.ToLower(ticket) + "-" + feature
	case ticket != "":
		return strings.ToLower(ticket)
	}
	return feature
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
	Ticket        string
	Brief         string
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

func (l Launch) Prompt() string {
	id := l.Ticket
	if id == "" {
		id = l.Slug
	}
	prompt := "/factory " + id
	if l.Ticket == "" && strings.TrimSpace(l.Brief) != "" {
		prompt += "\n" + strings.TrimRight(l.Brief, "\n")
	}
	return prompt
}

func (l Launch) CreateCommand() string {
	parts := append([]string{"claude", "--worktree", l.Slug, "--session-id", l.SessionID}, l.flags()...)
	worktree := filepath.Join(l.Repo, ".claude", "worktrees", l.Slug)
	return strings.Join(parts, " ") + " " + ShellQuote(l.Prompt()) + "; cd " + ShellQuote(worktree)
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
