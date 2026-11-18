package setup

import (
	"bufio"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/afonsodelgado/factory/internal/install"
	"github.com/afonsodelgado/factory/internal/node"
)

type Session struct {
	Run       node.Runner
	Interact  func(ctx context.Context, name string, args ...string) error
	CanSudo   func(ctx context.Context) bool
	In        *bufio.Reader
	Out       io.Writer
	Home      string
	User      string
	Host      string
	OS        string
	Options   func() install.Options
	Yes       bool
	Tailscale bool
	Command   string
}

type Step struct {
	Name   string
	Needed func(context.Context, *Session) string
	Root   func(*Session) []string
	Apply  func(context.Context, *Session) error
}

var ErrDeclined = errors.New("nothing was changed")

type NeedsRoot struct{ Commands []string }

func (e NeedsRoot) Error() string {
	return "these steps need root, and this account cannot use sudo"
}

func (s *Session) rootCommands(step Step) []string {
	if step.Root == nil {
		return nil
	}
	return step.Root(s)
}

func Run(ctx context.Context, s *Session, steps []Step) (bool, error) {
	var todo []string
	var root []string
	for _, step := range steps {
		if why := step.Needed(ctx, s); why != "" {
			todo = append(todo, fmt.Sprintf("%-18s %s", step.Name, why))
			root = append(root, s.rootCommands(step)...)
		}
	}
	if len(todo) == 0 {
		fmt.Fprintln(s.Out, "Everything is set up. Nothing changed.")
		return false, nil
	}
	fmt.Fprintf(s.Out, "%s · %s\n\n", cmp.Or(s.Command, "factory setup"), s.Host)
	for _, line := range todo {
		fmt.Fprintln(s.Out, "  • "+line)
	}
	if !s.confirm("\nContinue? [Y/n] ", true) {
		return false, ErrDeclined
	}
	if len(root) > 0 && !s.CanSudo(ctx) {
		return false, NeedsRoot{Commands: root}
	}
	for _, step := range steps {
		if step.Needed(ctx, s) == "" {
			continue
		}
		fmt.Fprintf(s.Out, "\n▸ %s\n", step.Name)
		if err := step.Apply(ctx, s); err != nil {
			return true, fmt.Errorf("%s: %w", step.Name, err)
		}
		if why := step.Needed(ctx, s); why != "" {
			return true, fmt.Errorf("%s is still not done: %s", step.Name, why)
		}
		install.WriteCheck(s.Out, install.Check{Name: step.Name, Mark: install.Pass, Detail: "done"})
	}
	return true, nil
}

func (s *Session) confirm(question string, yes bool) bool {
	fmt.Fprint(s.Out, question)
	if s.Yes {
		fmt.Fprintln(s.Out, map[bool]string{true: "y (--yes)", false: "n (--yes)"}[yes])
		return yes
	}
	answer := strings.ToLower(strings.TrimSpace(s.read()))
	if answer == "" {
		return yes
	}
	return answer == "y" || answer == "yes"
}

func (s *Session) ask(question, fallback string) string {
	fmt.Fprintf(s.Out, "%s [%s]: ", question, fallback)
	if s.Yes {
		fmt.Fprintln(s.Out, fallback)
		return fallback
	}
	if answer := strings.TrimSpace(s.read()); answer != "" {
		return answer
	}
	return fallback
}

func (s *Session) read() string {
	if s.In == nil {
		return ""
	}
	line, _ := s.In.ReadString('\n')
	return line
}

func (s *Session) output(ctx context.Context, name string, args ...string) (string, error) {
	out, err := s.Run(ctx, name, args...)
	return strings.TrimSpace(string(out)), err
}

func (s *Session) shell(ctx context.Context, script string) error {
	return s.Interact(ctx, "sh", "-c", script)
}

func Sudoer(groups []string) bool {
	return slices.ContainsFunc(groups, func(g string) bool { return g == "sudo" || g == "wheel" || g == "admin" })
}
