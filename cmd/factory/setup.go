package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/charmbracelet/x/term"

	"github.com/afonsodelgado/factory/internal/install"
	"github.com/afonsodelgado/factory/internal/node"
	"github.com/afonsodelgado/factory/internal/review"
	"github.com/afonsodelgado/factory/internal/setup"
	"github.com/afonsodelgado/factory/internal/state"
)

const (
	factorySetupUsage = "usage: factory setup [--yes]"
	settingsWait      = 2 * time.Minute
)

func runSetup(args []string) int {
	flags := flag.NewFlagSet("setup", flag.ContinueOnError)
	yes := flags.Bool("yes", false, "answer every question with its default and never wait for Enter")
	if err := flags.Parse(args); err != nil || flags.NArg() > 0 {
		fmt.Fprintln(os.Stderr, factorySetupUsage)
		return 2
	}
	home, _ := os.UserHomeDir()
	os.Setenv("PATH", filepath.Join(home, ".local", "bin")+string(os.PathListSeparator)+os.Getenv("PATH"))
	s := newSetupSession(home, *yes || !term.IsTerminal(os.Stdin.Fd()))
	ctx := context.Background()
	_, err := setup.Run(ctx, s, append(setup.Machine(), setupSteps()...))
	var root setup.NeedsRoot
	switch {
	case errors.Is(err, setup.ErrDeclined):
		fmt.Println(err)
		return 1
	case errors.As(err, &root):
		fmt.Fprintf(os.Stderr, "\n%s is not allowed to use sudo. From an account that is, run:\n\n", s.User)
		for _, command := range root.Commands {
			fmt.Fprintln(os.Stderr, "  "+command)
		}
		fmt.Fprintf(os.Stderr, "\nThen run factory setup again as %s.\n", s.User)
		return 1
	case err != nil:
		fmt.Fprintf(os.Stderr, "\nfactory setup stopped at %s.\nFix that, then run factory setup again: it skips what is already done.\n", err)
		return 1
	}
	return finishSetup()
}

func newSetupSession(home string, yes bool) *setup.Session {
	login := os.Getenv("USER")
	if u, err := user.Current(); err == nil {
		login = u.Username
	}
	host, _ := os.Hostname()
	host, _, _ = strings.Cut(host, ".")
	return &setup.Session{
		Run:      node.System,
		Interact: interact,
		CanSudo:  canSudo,
		In:       bufio.NewReader(os.Stdin),
		Out:      os.Stdout,
		Home:     home,
		User:     login,
		Host:     host,
		OS:       runtime.GOOS,
		Options:  doctorOptions,
		Yes:      yes,
	}
}

func interact(ctx context.Context, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}

func canSudo(ctx context.Context) bool {
	if os.Getuid() == 0 || exec.CommandContext(ctx, "sudo", "-n", "true").Run() == nil {
		return true
	}
	out, err := exec.CommandContext(ctx, "id", "-Gn").Output()
	if err != nil || !setup.Sudoer(strings.Fields(string(out))) {
		return false
	}
	return interact(ctx, "sudo", "-v") == nil
}

func setupSteps() []setup.Step {
	return []setup.Step{skillsStep(), nodeStep(), repoToolsStep(), browserStep()}
}

func skillsStep() setup.Step {
	return setup.Step{
		Name: "skills and hooks",
		Needed: func(_ context.Context, s *setup.Session) string {
			for _, c := range install.BundleChecks(s.Options()) {
				if c.Mark != install.Pass {
					return "installs factory's skills, agents and hooks for Claude Code"
				}
			}
			return ""
		},
		Apply: func(_ context.Context, s *setup.Session) error {
			plans, err := installPlans(binaryPath())
			if err != nil {
				return err
			}
			for _, plan := range plans {
				if !applyPlan(plan) {
					return errors.New("could not write the bundled files")
				}
			}
			return nil
		},
	}
}

func nodeAnswers(cfg *state.Config) bool {
	_, alive := runningMe(cfg)
	return alive
}

func joinable(ctx context.Context, cfg *state.Config) bool {
	local := node.Unix(cfg.NodeSocket())
	var roster node.Roster
	if local.Do(ctx, http.MethodGet, "/api/nodes", nil, &roster) != nil || len(roster.Members) > 1 {
		return false
	}
	var answer node.Candidates
	if local.Do(ctx, http.MethodGet, "/api/nodes/candidates", nil, &answer) != nil {
		return false
	}
	return slices.ContainsFunc(answer.Candidates, func(c node.Candidate) bool { return c.Selectable })
}

func nodeStep() setup.Step {
	return setup.Step{
		Name: "node",
		Needed: func(ctx context.Context, s *setup.Session) string {
			cfg := s.Options().Config
			if cfg == nil {
				return ""
			}
			record, _ := node.LoadInstall(cfg.StateDir)
			switch {
			case !record.Complete || !nodeAnswers(cfg):
				return "runs this machine's node as a service and joins your other nodes"
			case joinable(ctx, cfg):
				return "joins the node list of your other machines"
			}
			return ""
		},
		Apply: func(ctx context.Context, s *setup.Session) error {
			cfg, err := state.LoadConfig()
			if err != nil {
				return err
			}
			n := newNodeSetup(cfg, s.Out)
			n.in, n.tty, n.join = s.In, true, alwaysJoin
			if err := n.install(ctx); err != nil {
				return err
			}
			awaitSettings(ctx, cfg, s)
			return nil
		},
	}
}

func awaitSettings(ctx context.Context, cfg *state.Config, s *setup.Session) {
	local := node.Unix(cfg.NodeSocket())
	var roster node.Roster
	if local.Do(ctx, http.MethodGet, "/api/nodes", nil, &roster) != nil || len(roster.Members) < 2 {
		return
	}
	fmt.Fprintln(s.Out, "\nCopying your shared settings and repos from your other nodes…")
	deadline := time.Now().Add(settingsWait)
	for time.Now().Before(deadline) {
		var status node.SettingsStatus
		if local.Do(ctx, http.MethodGet, "/api/settings", nil, &status) == nil && status.Records > 0 && len(status.Pending) == 0 {
			install.WriteCheck(s.Out, install.Check{Name: "settings", Mark: install.Pass, Detail: count(status.Records, "shared setting") + " in place"})
			return
		}
		time.Sleep(2 * time.Second)
	}
	install.WriteCheck(s.Out, install.Check{Name: "settings", Mark: install.Warn, Detail: "still copying; the node keeps going in the background, and factory doctor shows what is missing"})
}

func failing(checks []install.Check, names ...string) []install.Check {
	return slices.DeleteFunc(checks, func(c install.Check) bool { return c.Mark != install.Fail || !slices.Contains(names, c.Name) })
}

func repoToolsStep() setup.Step {
	return setup.Step{
		Name: "codex and opencode",
		Needed: func(_ context.Context, s *setup.Session) string {
			if missing := failing(install.ToolChecks(s.Options()), "codex", "codex login", "opencode"); len(missing) > 0 {
				return "installs and logs in the agents your settings pick: " + missing[0].Name
			}
			return ""
		},
		Apply: func(ctx context.Context, s *setup.Session) error {
			for _, c := range failing(install.ToolChecks(s.Options()), "codex", "codex login", "opencode") {
				var err error
				switch c.Name {
				case "codex":
					_, err = s.Run(ctx, "npm", "install", "--global", "--prefix", filepath.Join(s.Home, ".local"), "@openai/codex")
				case "codex login":
					err = s.Interact(ctx, "codex", "login")
				case "opencode":
					err = s.Interact(ctx, "sh", "-c", "curl -fsSL https://opencode.ai/install | bash")
				}
				if err != nil {
					return fmt.Errorf("%s: %w", c.Name, err)
				}
			}
			return nil
		},
	}
}

func chromeInstall(s *setup.Session) []string {
	m, _ := install.PackageManager()
	switch {
	case s.OS == "darwin":
		return []string{"brew install --cask google-chrome"}
	case m.Name == "apt-get" && runtime.GOARCH == "amd64":
		return []string{"curl -fsSLo /tmp/google-chrome.deb https://dl.google.com/linux/direct/google-chrome-stable_current_amd64.deb", "sudo apt-get install -y /tmp/google-chrome.deb", "rm /tmp/google-chrome.deb"}
	case m.Name == "":
		return nil
	}
	return []string{strings.Join(m.Command("chromium"), " ")}
}

func browserStep() setup.Step {
	return setup.Step{
		Name: "browser QA",
		Needed: func(_ context.Context, s *setup.Session) string {
			o := s.Options()
			if len(o.Browser) == 0 || install.BrowserQA(o).Mark == install.Pass {
				return ""
			}
			return "installs Chrome for browser QA in " + strings.Join(o.Browser, ", ")
		},
		Root: func(s *setup.Session) []string {
			if s.OS == "darwin" {
				return nil
			}
			return chromeInstall(s)
		},
		Apply: func(ctx context.Context, s *setup.Session) error {
			configured := ""
			if cfg := s.Options().Config; cfg != nil {
				configured = cfg.Review.Chrome
			}
			if _, found := review.ChromeBinary(configured); found {
				return nil
			}
			commands := chromeInstall(s)
			if len(commands) == 0 {
				return errors.New("install Google Chrome or Chromium, then run factory setup again")
			}
			return s.Interact(ctx, "sh", "-c", strings.Join(commands, " && "))
		},
	}
}

func finishSetup() int {
	o := doctorOptions()
	var open []install.Check
	for _, c := range install.Doctor(o) {
		if c.Mark == install.Fail || c.Mark == install.Warn {
			open = append(open, c)
		}
	}
	fmt.Println()
	if len(open) == 0 {
		fmt.Println("factory doctor finds nothing left to do.")
	} else {
		fmt.Println("factory doctor still lists:")
		for _, c := range open {
			install.WriteCheck(os.Stdout, c)
		}
	}
	if o.Config != nil && o.Config.NodeHostname() != "" {
		fmt.Printf("\nThis machine is a factory node at https://%s.\nOpen the hub with factory, or that URL on your phone with Tailscale on.\n", o.Config.NodeHostname())
	}
	if slices.ContainsFunc(open, func(c install.Check) bool { return c.Mark == install.Fail && c.Name != "repos" }) {
		return 1
	}
	return 0
}
