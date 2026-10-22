package install

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

type Mark string

const (
	Pass Mark = "✓"
	Warn Mark = "!"
	Fail Mark = "✗"
)

type Check struct {
	Name   string
	Mark   Mark
	Detail string
	Note   string
	Lines  []string
	Fix    string
}

type Options struct {
	ClaudeDir string
	Binary    string
	Browser   []string
	Home      string
}

func run(name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

var versionPattern = regexp.MustCompile(`\d+(\.\d+)+[a-z]?`)

func version(text string) string {
	return versionPattern.FindString(text)
}

func atLeast(have, want string) bool {
	parts := func(v string) []int {
		var n []int
		for _, p := range strings.Split(strings.TrimRight(v, "abcdefghijklmnopqrstuvwxyz"), ".") {
			i, _ := strconv.Atoi(p)
			n = append(n, i)
		}
		return n
	}
	h, w := parts(have), parts(want)
	for i := range w {
		if i >= len(h) {
			return false
		}
		if h[i] != w[i] {
			return h[i] > w[i]
		}
	}
	return true
}

func installCommand(pkg string) string {
	if runtime.GOOS == "darwin" {
		return "brew install " + pkg
	}
	return "sudo apt-get install -y " + pkg
}

func Doctor(o Options) []Check {
	return []Check{
		tool("claude", "2.1.300", "claude", []string{"--version"}, "curl -fsSL https://claude.ai/install.sh | bash"),
		tool("git", "", "git", []string{"--version"}, installCommand("git")),
		tool("tmux", "3.3", "tmux", []string{"-V"}, installCommand("tmux")),
		ghCheck(),
		ghStack(),
		revmux(o),
		plugin(o, "impeccable", "impeccable@impeccable", "design stage"),
		chrome(o),
		nerdFont(o),
		bundleCheck(o),
		hooksCheck(o),
	}
}

func tool(name, minimum, command string, args []string, fix string) Check {
	out, err := run(command, args...)
	if err != nil {
		return Check{Name: name, Mark: Fail, Detail: "not installed", Fix: fix}
	}
	v := version(out)
	c := Check{Name: name, Mark: Pass, Detail: v}
	if minimum != "" {
		c.Note = "needs " + minimum + " or later"
		if !atLeast(v, minimum) {
			c.Mark, c.Fix = Fail, fix
		}
	}
	return c
}

var accountPattern = regexp.MustCompile(`account (\S+)`)

func ghCheck() Check {
	out, err := run("gh", "--version")
	if err != nil {
		return Check{Name: "gh", Mark: Fail, Detail: "not installed", Fix: installCommand("gh") + " && gh auth login"}
	}
	v := version(out)
	status, err := run("gh", "auth", "status")
	if err != nil {
		return Check{Name: "gh", Mark: Fail, Detail: v + ", but not logged in", Fix: "gh auth login"}
	}
	detail := v + " · logged in"
	if m := accountPattern.FindStringSubmatch(status); m != nil {
		detail += " as " + m[1]
	}
	return Check{Name: "gh", Mark: Pass, Detail: detail}
}

func ghStack() Check {
	out, err := run("gh", "extension", "list")
	if err != nil || !strings.Contains(out, "gh-stack") {
		return Check{Name: "gh-stack", Mark: Fail, Detail: "not installed", Fix: "gh extension install github/gh-stack"}
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "gh-stack") {
			return Check{Name: "gh-stack", Mark: Pass, Detail: strings.Join(strings.Fields(line)[1:], " ")}
		}
	}
	return Check{Name: "gh-stack", Mark: Pass}
}

func installedPlugins(claudeDir string) map[string]string {
	data, err := os.ReadFile(filepath.Join(claudeDir, "plugins", "installed_plugins.json"))
	if err != nil {
		return nil
	}
	var file struct {
		Plugins map[string][]struct {
			Version string `json:"version"`
		} `json:"plugins"`
	}
	if json.Unmarshal(data, &file) != nil {
		return nil
	}
	found := map[string]string{}
	for name, installs := range file.Plugins {
		if len(installs) > 0 {
			found[name] = installs[0].Version
		}
	}
	return found
}

func plugin(o Options, name, id, purpose string) Check {
	v, ok := installedPlugins(o.ClaudeDir)[id]
	if !ok {
		return Check{Name: name, Mark: Fail, Detail: "plugin " + id + " not installed; the " + purpose + " needs it", Fix: "claude plugin install " + id}
	}
	return Check{Name: name, Mark: Pass, Detail: strings.TrimSpace("plugin " + id + " " + v), Note: purpose}
}

func revmux(o Options) Check {
	_, binaryErr := exec.LookPath("revmux")
	_, pluginInstalled := installedPlugins(o.ClaudeDir)["revmux@revmux"]
	switch {
	case binaryErr != nil:
		return Check{Name: "revmux", Mark: Fail, Detail: "the revmux binary is not on PATH", Fix: "install revmux, then claude plugin install revmux@revmux"}
	case !pluginInstalled:
		return Check{Name: "revmux", Mark: Fail, Detail: "plugin revmux@revmux not installed", Fix: "claude plugin install revmux@revmux"}
	}
	out, _ := run("revmux", "--version")
	return Check{Name: "revmux", Mark: Pass, Detail: strings.TrimSpace(version(out) + " · plugin revmux@revmux")}
}

func chrome(o Options) Check {
	detail := "can't be detected from here. Only repos whose profile uses browser QA need it"
	if len(o.Browser) > 0 {
		detail += " (" + strings.Join(o.Browser, ", ") + ")"
	}
	return Check{Name: "Chrome", Mark: Warn, Detail: detail + ".", Fix: "install the Claude in Chrome extension; factory-tui passes --chrome per profile"}
}

func nerdFont(o Options) Check {
	fix := "brew install --cask font-jetbrains-mono-nerd-font"
	var found []string
	if runtime.GOOS == "darwin" {
		for _, dir := range []string{filepath.Join(o.Home, "Library", "Fonts"), "/Library/Fonts"} {
			matches, _ := filepath.Glob(filepath.Join(dir, "*Nerd*"))
			found = append(found, matches...)
		}
	} else if out, err := run("fc-list"); err == nil && strings.Contains(out, "Nerd") {
		found = append(found, "fc-list")
	}
	if len(found) == 0 {
		return Check{Name: "Nerd Font", Mark: Warn, Detail: "none found. Icons need one; set icons: unicode otherwise.", Fix: fix}
	}
	return Check{Name: "Nerd Font", Mark: Warn, Detail: "installed; doctor can't tell whether your terminal uses it.", Fix: fix + "  if icons show as boxes"}
}

func bundleCheck(o Options) Check {
	plan, err := NewPlan(o.ClaudeDir, o.Binary)
	if err != nil {
		return Check{Name: "bundle", Mark: Fail, Detail: err.Error()}
	}
	pending := plan.Pending()
	total := len(plan.Files)
	if len(pending) == 0 {
		return Check{Name: "bundle", Mark: Pass, Detail: fmt.Sprintf("all %d files match this binary", total)}
	}
	missing := 0
	c := Check{Name: "bundle", Mark: Fail, Fix: "factory-tui bootstrap  backs up each changed file with a timestamp first"}
	for i, f := range pending {
		if f.State == Missing {
			missing++
		}
		if i < 6 {
			c.Lines = append(c.Lines, fmt.Sprintf("%-52s %s", tilde(f.Target), f.State))
		}
	}
	if more := len(pending) - 6; more > 0 {
		c.Lines = append(c.Lines, fmt.Sprintf("and %d more", more))
	}
	c.Detail = fmt.Sprintf("%d of %d files differ from this binary", len(pending), total)
	if missing == total {
		c.Detail, c.Lines, c.Fix = fmt.Sprintf("not installed (0 of %d files)", total), nil, "factory-tui bootstrap"
	}
	return c
}

func hooksCheck(o Options) Check {
	plan, err := NewPlan(o.ClaudeDir, o.Binary)
	if err != nil {
		return Check{Name: "hooks", Mark: Fail, Detail: err.Error()}
	}
	settings := tilde(filepath.Join(o.ClaudeDir, "settings.json"))
	var moved []string
	for _, command := range plan.Stale {
		binary := strings.Fields(strings.ReplaceAll(command, "'", ""))[0]
		if _, err := os.Stat(binary); err != nil {
			moved = append(moved, binary+" no longer exists")
		} else {
			moved = append(moved, "they run "+binary+", not this binary")
		}
	}
	switch {
	case len(moved) > 0:
		return Check{Name: "hooks", Mark: Fail, Detail: "factory-tui hooks point at another binary", Lines: moved, Fix: "factory-tui install-hooks  then restart running Claude Code sessions"}
	case len(plan.Hooks) > 0:
		return Check{Name: "hooks", Mark: Fail, Detail: fmt.Sprintf("factory-tui status hooks are missing from %s for %s", settings, strings.Join(plan.Hooks, ", ")), Fix: "factory-tui install-hooks  merges with your existing hooks"}
	}
	return Check{Name: "hooks", Mark: Pass, Detail: "installed in " + settings, Note: "restart running sessions after changing them"}
}

func tilde(path string) string {
	home, err := os.UserHomeDir()
	if err == nil && home != "" && strings.HasPrefix(path, home+string(filepath.Separator)) {
		return "~" + strings.TrimPrefix(path, home)
	}
	return path
}

func Report(w io.Writer, checks []Check, header string) (problems, warnings int) {
	fmt.Fprintln(w, header)
	fmt.Fprintln(w)
	for _, c := range checks {
		line := fmt.Sprintf("  %s %-12s %s", c.Mark, c.Name, c.Detail)
		if c.Note != "" {
			line = fmt.Sprintf("%-60s %s", line, c.Note)
		}
		fmt.Fprintln(w, strings.TrimRight(line, " "))
		for _, l := range c.Lines {
			fmt.Fprintf(w, "%17s%s\n", "", l)
		}
		if c.Fix != "" && c.Mark != Pass {
			fmt.Fprintf(w, "%17sfix  %s\n", "", c.Fix)
		}
		switch c.Mark {
		case Fail:
			problems++
		case Warn:
			warnings++
		}
	}
	fmt.Fprintln(w)
	return problems, warnings
}
