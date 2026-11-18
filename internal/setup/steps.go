package setup

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/afonsodelgado/factory/internal/install"
	"github.com/afonsodelgado/factory/internal/node"
	"github.com/afonsodelgado/factory/internal/state"
)

const (
	tailscaleInstall = "curl -fsSL https://tailscale.com/install.sh | sh"
	httpsWait        = 10 * time.Minute
)

var basePackages = []string{"git", "tmux", "python3", "jq", "curl", "gh", "node"}

const ghAptRepo = `set -e
sudo mkdir -p -m 755 /etc/apt/keyrings
curl -fsSL https://cli.github.com/packages/githubcli-archive-keyring.gpg | sudo tee /etc/apt/keyrings/githubcli-archive-keyring.gpg >/dev/null
sudo chmod go+r /etc/apt/keyrings/githubcli-archive-keyring.gpg
echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/githubcli-archive-keyring.gpg] https://cli.github.com/packages stable main" | sudo tee /etc/apt/sources.list.d/github-cli.list >/dev/null`

func WantsTailscale(adding bool, m state.Machine) bool { return adding || m.TailscaleNode() }

func Machine(withTailscale bool) []Step {
	steps := []Step{packages()}
	if withTailscale {
		steps = append(steps, tailscaleApp(), tailscaleOperator())
	}
	steps = append(steps, linger())
	if withTailscale {
		steps = append(steps, tailscaleLogin(), tailnetHTTPS())
	}
	steps = append(steps, sshKey(), githubLogin(), githubKey(), gitIdentity(), commitSigning(), claudeCode(), claudeLogin(), ghStack(), revmux())
	for _, p := range install.Plugins {
		steps = append(steps, plugin(p))
	}
	return steps
}

func missingPackages() []string {
	var missing []string
	for _, tool := range basePackages {
		binary := tool
		if tool == "node" {
			binary = "npx"
		}
		if !install.HasCommand(binary) {
			missing = append(missing, tool)
		}
	}
	return missing
}

func packages() Step {
	return Step{
		Name: "packages",
		Needed: func(context.Context, *Session) string {
			if missing := missingPackages(); len(missing) > 0 {
				return "installs " + strings.Join(missing, ", ")
			}
			if tmux := install.Tmux(); tmux.Mark != install.Pass {
				return "tmux " + tmux.Detail + " is older than 3.3"
			}
			return ""
		},
		Root: func(s *Session) []string {
			m, ok := install.PackageManager()
			if !ok || m.Name == "brew" {
				return nil
			}
			var commands []string
			missing := missingPackages()
			if m.Name == "apt-get" && contains(missing, "gh") {
				commands = append(commands, strings.Split(ghAptRepo, "\n")[1:]...)
			}
			if len(m.Refresh) > 0 {
				commands = append(commands, strings.Join(m.Refresh, " "))
			}
			return append(commands, strings.Join(m.Command(missing...), " "))
		},
		Apply: func(ctx context.Context, s *Session) error {
			m, ok := install.PackageManager()
			missing := missingPackages()
			switch {
			case !ok && s.OS == "darwin":
				return errors.New("Homebrew is not installed. Install it from https://brew.sh, then run factory setup again")
			case !ok:
				return fmt.Errorf("no apt-get, dnf, pacman or zypper here; install %s with your package manager, then run factory setup again", strings.Join(missing, ", "))
			case len(missing) == 0:
				return fmt.Errorf("this system's tmux is older than 3.3, which factory needs; install a newer tmux (Ubuntu 24.04 and Debian 13 ship one), then run factory setup again")
			}
			if m.Name == "apt-get" && contains(missing, "gh") {
				if err := s.shell(ctx, ghAptRepo); err != nil {
					return fmt.Errorf("could not add GitHub's apt repository for gh: %w", err)
				}
			}
			if len(m.Refresh) > 0 {
				if err := s.Interact(ctx, m.Refresh[0], m.Refresh[1:]...); err != nil {
					return err
				}
			}
			command := m.Command(missing...)
			return s.Interact(ctx, command[0], command[1:]...)
		},
	}
}

func contains(list []string, item string) bool {
	for _, x := range list {
		if x == item {
			return true
		}
	}
	return false
}

func onLinux(s *Session) bool { return s.OS == "linux" }

func tailscaleApp() Step {
	return Step{
		Name: "tailscale",
		Needed: func(context.Context, *Session) string {
			if install.HasCommand("tailscale") {
				return ""
			}
			return "installs Tailscale, which connects your nodes and your phone"
		},
		Root: func(s *Session) []string {
			if onLinux(s) {
				return []string{tailscaleInstall}
			}
			return nil
		},
		Apply: func(ctx context.Context, s *Session) error {
			if !onLinux(s) {
				fmt.Fprintln(s.Out, "Install the Tailscale app from https://tailscale.com/download and open it.")
				s.confirm("Press Enter once it is installed. ", true)
				return nil
			}
			return s.shell(ctx, tailscaleInstall)
		},
	}
}

func tailscaleOperator() Step {
	operator := func(ctx context.Context, s *Session) string {
		out, err := s.output(ctx, "tailscale", "debug", "prefs")
		if err != nil {
			return ""
		}
		var prefs struct{ OperatorUser string }
		json.Unmarshal([]byte(out), &prefs)
		return prefs.OperatorUser
	}
	return Step{
		Name: "tailscale user",
		Needed: func(ctx context.Context, s *Session) string {
			if !onLinux(s) || os.Getuid() == 0 || (install.HasCommand("tailscale") && operator(ctx, s) == s.User) {
				return ""
			}
			return "lets " + s.User + " run tailscale serve without sudo"
		},
		Root: func(s *Session) []string { return []string{"sudo tailscale set --operator=" + s.User} },
		Apply: func(ctx context.Context, s *Session) error {
			return s.Interact(ctx, "sudo", "tailscale", "set", "--operator="+s.User)
		},
	}
}

func linger() Step {
	lingering := func(ctx context.Context, s *Session) bool {
		out, err := s.output(ctx, "loginctl", "show-user", s.User, "--property=Linger", "--value")
		return err == nil && out == "yes"
	}
	return Step{
		Name: "start at boot",
		Needed: func(ctx context.Context, s *Session) string {
			if !onLinux(s) || lingering(ctx, s) {
				return ""
			}
			return "keeps " + s.User + "'s node running without a login and after a reboot"
		},
		Root: func(s *Session) []string { return []string{"sudo loginctl enable-linger " + s.User} },
		Apply: func(ctx context.Context, s *Session) error {
			return s.Interact(ctx, "sudo", "loginctl", "enable-linger", s.User)
		},
	}
}

func tailscaleStatus(ctx context.Context, s *Session) (node.Self, error) {
	return node.Tailscale{Run: s.Run}.Status(ctx)
}

func tailscaleLogin() Step {
	return Step{
		Name: "tailscale login",
		Needed: func(ctx context.Context, s *Session) string {
			if _, err := tailscaleStatus(ctx, s); err != nil {
				return "logs this machine in to your tailnet"
			}
			return ""
		},
		Apply: func(ctx context.Context, s *Session) error {
			if !onLinux(s) {
				fmt.Fprintln(s.Out, "Open the Tailscale app and log in as yourself.")
				s.confirm("Press Enter once it shows you as connected. ", true)
				return nil
			}
			return s.Interact(ctx, "tailscale", "up")
		},
	}
}

func tailnetHTTPS() Step {
	return Step{
		Name: "tailnet HTTPS",
		Needed: func(ctx context.Context, s *Session) string {
			self, err := tailscaleStatus(ctx, s)
			if err != nil || self.HTTPS() {
				return ""
			}
			return "turns on HTTPS certificates, once for your whole tailnet"
		},
		Apply: func(ctx context.Context, s *Session) error {
			fmt.Fprintln(s.Out, strings.ToUpper(node.HTTPSFix[:1])+node.HTTPSFix[1:]+".")
			deadline := time.Now().Add(httpsWait)
			for time.Now().Before(deadline) {
				if s.Yes {
					time.Sleep(10 * time.Second)
				} else {
					s.confirm("Press Enter once it is on. ", true)
				}
				if self, err := tailscaleStatus(ctx, s); err == nil && self.HTTPS() {
					return nil
				}
			}
			return errors.New("HTTPS certificates are still off for this tailnet")
		},
	}
}

const (
	keyName           = "~/.ssh/id_ed25519"
	passphraseRemoval = `ssh-keygen -p -f ~/.ssh/id_ed25519 -N ""`
)

type Manual struct{ Reason, Command string }

func (m Manual) Error() string { return m.Reason + "\n  " + m.Command }

func (s *Session) keyPath() string { return filepath.Join(s.Home, ".ssh", "id_ed25519") }

func (s *Session) hasKey() bool {
	_, err := os.Stat(s.keyPath())
	return err == nil
}

func hasPassphrase(ctx context.Context, s *Session) bool {
	_, err := s.Run(ctx, "ssh-keygen", "-y", "-P", "", "-f", s.keyPath())
	return err != nil
}

func sshKey() Step {
	return Step{
		Name: "ssh key",
		Needed: func(ctx context.Context, s *Session) string {
			if s.hasKey() {
				if hasPassphrase(ctx, s) {
					return keyName + " has a passphrase, so the node cannot use it"
				}
				return ""
			}
			if authenticated, _ := sshAuthenticated(ctx, s); authenticated {
				return ""
			}
			return "creates the SSH key this machine's node clones and pushes with"
		},
		Apply: func(ctx context.Context, s *Session) error {
			if s.hasKey() {
				return removePassphrase(ctx, s)
			}
			if err := os.MkdirAll(filepath.Dir(s.keyPath()), 0o700); err != nil {
				return err
			}
			_, err := s.output(ctx, "ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", "factory@"+s.Host, "-f", s.keyPath())
			return err
		},
	}
}

func removePassphrase(ctx context.Context, s *Session) error {
	_, answer := sshAuthenticated(ctx, s, "-o", "StrictHostKeyChecking=accept-new")
	fmt.Fprintf(s.Out, "%s has a passphrase. The node clones and pushes with nobody at the\nkeyboard, so ssh can never ask for it. ssh answered: %s\n", keyName, answer)
	if s.Yes {
		return Manual{Reason: "--yes never types a passphrase. Remove it with", Command: passphraseRemoval}
	}
	if !s.confirm("\nRemove the passphrase now? ssh-keygen asks for it once. The key stays the same key,\nso GitHub needs no change. It runs:\n  "+passphraseRemoval+"\n[Y/n] ", true) {
		return nil
	}
	return s.Interact(ctx, "ssh-keygen", "-p", "-f", s.keyPath(), "-N", "")
}

func githubLogin() Step {
	return Step{
		Name: "github login",
		Needed: func(ctx context.Context, s *Session) string {
			if _, err := s.output(ctx, "gh", "auth", "status"); err != nil {
				return "logs gh in to GitHub"
			}
			return ""
		},
		Apply: func(ctx context.Context, s *Session) error {
			fmt.Fprintln(s.Out, "gh shows a one-time code. Open the link on any device and enter it.")
			return s.Interact(ctx, "gh", "auth", "login", "--hostname", "github.com", "--git-protocol", "ssh", "--web", "--skip-ssh-key", "--scopes", "admin:public_key,admin:ssh_signing_key")
		},
	}
}

func publicKey(s *Session) string {
	data, err := os.ReadFile(s.keyPath() + ".pub")
	if err != nil {
		return ""
	}
	fields := strings.Fields(string(data))
	if len(fields) < 2 {
		return ""
	}
	return fields[0] + " " + fields[1]
}

func sshAuthenticated(ctx context.Context, s *Session, options ...string) (bool, string) {
	args := append(append([]string{"-o", "BatchMode=yes"}, options...), "-T", "git@github.com")
	out, err := s.Run(ctx, "ssh", args...)
	answer := strings.TrimSpace(string(out) + "\n" + stderrOf(err))
	return strings.Contains(answer, "successfully authenticated"), answer
}

func stderrOf(err error) string {
	var exit *exec.ExitError
	switch {
	case errors.As(err, &exit):
		return string(exit.Stderr)
	case err != nil:
		return err.Error()
	}
	return ""
}

func githubKey() Step {
	return Step{
		Name: "github ssh",
		Needed: func(ctx context.Context, s *Session) string {
			if authenticated, _ := sshAuthenticated(ctx, s); authenticated {
				return ""
			}
			return "adds this machine's SSH key to your GitHub account"
		},
		Apply: func(ctx context.Context, s *Session) error {
			keys, _ := s.output(ctx, "gh", "api", "user/keys", "--jq", ".[].key")
			if key := publicKey(s); key != "" && !strings.Contains(keys, key) {
				if err := s.addKey(ctx, "authentication", "admin:public_key"); err != nil {
					return err
				}
				install.WriteCheck(s.Out, install.Check{Name: "key", Mark: install.Pass, Detail: "added to GitHub as factory " + s.Host})
			}
			authenticated, answer := sshAuthenticated(ctx, s, "-o", "StrictHostKeyChecking=accept-new")
			if authenticated {
				return nil
			}
			install.WriteCheck(s.Out, install.Check{Name: "github ssh", Mark: install.Fail, Detail: "GitHub still refuses this machine's key. ssh answered:", Lines: strings.Split(answer, "\n"), Fix: cmp.Or(install.GitHint(answer), "ssh -v -T git@github.com  shows which keys ssh offered")})
			return errors.New("GitHub refuses " + keyName)
		},
	}
}

func (s *Session) addKey(ctx context.Context, kind, scope string) error {
	args := []string{"ssh-key", "add", s.keyPath() + ".pub", "--title", "factory " + s.Host, "--type", kind}
	if _, err := s.output(ctx, "gh", args...); err == nil {
		return nil
	}
	if err := s.Interact(ctx, "gh", "auth", "refresh", "--hostname", "github.com", "--scopes", scope); err != nil {
		return err
	}
	_, err := s.output(ctx, "gh", args...)
	return err
}

func gitConfig(ctx context.Context, s *Session, key string) string {
	out, _ := s.output(ctx, "git", "config", "--global", "--get", key)
	return out
}

func gitIdentity() Step {
	return Step{
		Name: "git identity",
		Needed: func(ctx context.Context, s *Session) string {
			if gitConfig(ctx, s, "user.name") != "" && gitConfig(ctx, s, "user.email") != "" {
				return ""
			}
			return "sets the name and email on this machine's commits"
		},
		Apply: func(ctx context.Context, s *Session) error {
			out, _ := s.output(ctx, "gh", "api", "user", "--jq", `[.name // .login, .email // "", .login, (.id|tostring)] | join("\n")`)
			profile := append(strings.Split(out, "\n"), "", "", "", "")
			name, email := profile[0], profile[1]
			if email == "" && profile[2] != "" {
				email = profile[3] + "+" + profile[2] + "@users.noreply.github.com"
			}
			for key, value := range map[string]string{"user.name": s.ask("Name on commits", name), "user.email": s.ask("Email on commits", email)} {
				if _, err := s.output(ctx, "git", "config", "--global", key, value); err != nil {
					return err
				}
			}
			return nil
		},
	}
}

func commitSigning() Step {
	return Step{
		Name: "commit signing",
		Needed: func(ctx context.Context, s *Session) string {
			if gitConfig(ctx, s, "commit.gpgsign") != "" {
				return ""
			}
			return "asks whether to sign commits with this machine's SSH key"
		},
		Apply: func(ctx context.Context, s *Session) error {
			if !s.confirm("Sign commits with this machine's SSH key? [y/N] ", false) {
				_, err := s.output(ctx, "git", "config", "--global", "commit.gpgsign", "false")
				return err
			}
			if err := s.addKey(ctx, "signing", "admin:ssh_signing_key"); err != nil {
				return err
			}
			for _, kv := range [][2]string{{"gpg.format", "ssh"}, {"user.signingkey", s.keyPath() + ".pub"}, {"commit.gpgsign", "true"}} {
				if _, err := s.output(ctx, "git", "config", "--global", kv[0], kv[1]); err != nil {
					return err
				}
			}
			return nil
		},
	}
}

func claudeCode() Step {
	return Step{
		Name: "claude code",
		Needed: func(context.Context, *Session) string {
			if c := install.ClaudeCode(); c.Mark != install.Pass {
				return "installs Claude Code (" + c.Detail + ")"
			}
			return ""
		},
		Apply: func(ctx context.Context, s *Session) error { return s.shell(ctx, install.ClaudeInstall) },
	}
}

func claudeLogin() Step {
	return Step{
		Name: "claude login",
		Needed: func(_ context.Context, s *Session) string {
			if install.ClaudeLogin(s.Options()).Mark == install.Pass {
				return ""
			}
			return "logs Claude Code in, so agents never stop at a login screen"
		},
		Apply: func(ctx context.Context, s *Session) error {
			fmt.Fprintln(s.Out, "Claude Code opens next. Type /login, finish in the browser, then /exit to come back here.")
			return s.Interact(ctx, "claude")
		},
	}
}

func ghStack() Step {
	return Step{
		Name: "gh-stack",
		Needed: func(context.Context, *Session) string {
			if install.GhStack().Mark == install.Pass {
				return ""
			}
			return "installs the gh extension the engineer stacks PRs with"
		},
		Apply: func(ctx context.Context, s *Session) error {
			_, err := s.output(ctx, "gh", "extension", "install", "github/gh-stack")
			return err
		},
	}
}

func revmux() Step {
	return Step{
		Name: "revmux",
		Needed: func(context.Context, *Session) string {
			if install.HasCommand("revmux") {
				return ""
			}
			return "installs the review tool the reviewer runs"
		},
		Apply: func(ctx context.Context, s *Session) error {
			_, err := s.output(ctx, "sh", "-c", install.RevmuxInstall())
			return err
		},
	}
}

func plugin(p install.Plugin) Step {
	return Step{
		Name: p.Name + " plugin",
		Needed: func(_ context.Context, s *Session) string {
			if install.PluginCheck(s.Options(), p).Mark == install.Pass {
				return ""
			}
			return "installs the Claude Code plugin the " + p.Purpose + " needs"
		},
		Apply: func(ctx context.Context, s *Session) error {
			if !install.KnownMarketplaces(s.Options().ClaudeDir)[p.Marketplace] {
				if _, err := s.output(ctx, "claude", "plugin", "marketplace", "add", p.Repo); err != nil {
					return err
				}
			}
			_, err := s.output(ctx, "claude", "plugin", "install", p.ID)
			return err
		},
	}
}
