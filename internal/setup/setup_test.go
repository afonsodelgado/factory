package setup

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/afonsodelgado/factory/internal/state"
)

type fakeMachine struct {
	missing map[string]bool
	applied []string
}

func (f *fakeMachine) step(name string, root bool) Step {
	s := Step{
		Name: name,
		Needed: func(context.Context, *Session) string {
			if f.missing[name] {
				return "installs " + name
			}
			return ""
		},
		Apply: func(context.Context, *Session) error {
			f.applied = append(f.applied, name)
			delete(f.missing, name)
			return nil
		},
	}
	if root {
		s.Root = func(*Session) []string { return []string{"sudo install " + name} }
	}
	return s
}

func session(sudo bool) (*Session, *bytes.Buffer) {
	var out bytes.Buffer
	return &Session{Out: &out, In: bufio.NewReader(strings.NewReader("\n")), Host: "homebox", CanSudo: func(context.Context) bool { return sudo }}, &out
}

func TestSetupRunsOnlyWhatIsMissingInOrderAndASecondRunChangesNothing(t *testing.T) {
	f := &fakeMachine{missing: map[string]bool{"packages": true, "claude login": true}}
	steps := []Step{f.step("packages", true), f.step("tailscale", true), f.step("claude login", false)}

	s, out := session(true)
	if _, err := Run(context.Background(), s, steps); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(f.applied, []string{"packages", "claude login"}) {
		t.Errorf("setup applied %v; want only the missing steps, in order", f.applied)
	}
	if strings.Contains(out.String(), "tailscale ") {
		t.Errorf("the plan lists a step that is already done:\n%s", out)
	}

	f.applied = nil
	s, out = session(true)
	changed, err := Run(context.Background(), s, steps)
	if err != nil || changed || len(f.applied) > 0 || !strings.Contains(out.String(), "Nothing changed") {
		t.Errorf("a second run changed %v (%v, %v):\n%s", f.applied, changed, err, out)
	}
}

func TestSetupWithoutSudoNamesTheRootCommandsAndChangesNothing(t *testing.T) {
	f := &fakeMachine{missing: map[string]bool{"packages": true, "claude login": true}}
	s, _ := session(false)
	_, err := Run(context.Background(), s, []Step{f.step("packages", true), f.step("claude login", false)})
	var root NeedsRoot
	if !errors.As(err, &root) || !slices.Equal(root.Commands, []string{"sudo install packages"}) || len(f.applied) > 0 {
		t.Errorf("an account without sudo got %v and applied %v; want the root commands named before anything changes", err, f.applied)
	}
}

func TestSetupStopsWhenAStepIsStillNotDoneAfterItRan(t *testing.T) {
	f := &fakeMachine{missing: map[string]bool{"github login": true, "gh-stack": true}}
	stuck := f.step("github login", false)
	stuck.Apply = func(context.Context, *Session) error { return nil }
	s, _ := session(true)
	_, err := Run(context.Background(), s, []Step{stuck, f.step("gh-stack", false)})
	if err == nil || !strings.Contains(err.Error(), "github login is still not done") || len(f.applied) > 0 {
		t.Errorf("a login that did not take got %v and then ran %v; want setup to stop there", err, f.applied)
	}
}

func TestOnlyATailscaleNodeOrTheFlagGetsTheTailscaleSteps(t *testing.T) {
	tailscaleSteps := []string{"tailscale", "tailscale user", "tailscale login", "tailnet HTTPS"}
	tailnetNode := state.Machine{Node: state.Node{Owner: "alice@example.com", Hostname: "macbook.tail1234.ts.net"}}
	cases := []struct {
		name    string
		adding  bool
		machine state.Machine
		want    bool
	}{
		{name: "a machine with no record", want: false},
		{name: "an existing Tailscale node", machine: tailnetNode, want: true},
		{name: "factory setup --tailscale", adding: true, want: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var names []string
			for _, step := range Machine(WantsTailscale(c.adding, c.machine)) {
				names = append(names, step.Name)
			}
			for _, step := range tailscaleSteps {
				if slices.Contains(names, step) != c.want {
					t.Errorf("steps %v; want %q present=%v", names, step, c.want)
				}
			}
		})
	}
}

func keySession(t *testing.T, sshAnswer string) (*Session, *bytes.Buffer, *[]string) {
	t.Helper()
	s, out := session(true)
	s.Home = t.TempDir()
	if err := os.MkdirAll(filepath.Join(s.Home, ".ssh"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.keyPath(), []byte("encrypted"), 0o600); err != nil {
		t.Fatal(err)
	}
	s.Run = func(_ context.Context, name string, args ...string) ([]byte, error) {
		switch name {
		case "ssh-keygen":
			return nil, fmt.Errorf("ssh-keygen: %w", &exec.ExitError{Stderr: []byte("incorrect passphrase supplied to decrypt private key")})
		case "ssh":
			return nil, fmt.Errorf("ssh: %w", &exec.ExitError{Stderr: []byte(sshAnswer)})
		}
		return nil, nil
	}
	var interacted []string
	s.Interact = func(_ context.Context, name string, args ...string) error {
		interacted = append(interacted, name+" "+strings.Join(args, " "))
		return nil
	}
	return s, out, &interacted
}

func TestSetupNamesTheSSHKeyPassphraseAndYesStopsWithTheCommandThatRemovesIt(t *testing.T) {
	s, out, interacted := keySession(t, "git@github.com: Permission denied (publickey).")
	s.Yes = true
	_, err := Run(context.Background(), s, []Step{sshKey()})
	var manual Manual
	if !errors.As(err, &manual) || manual.Command != `ssh-keygen -p -f ~/.ssh/id_ed25519 -N ""` || len(*interacted) > 0 {
		t.Errorf("--yes with a passphrase key got %v and ran %v; want it to stop and name the ssh-keygen command", err, *interacted)
	}
	if said := out.String(); !strings.Contains(said, "~/.ssh/id_ed25519 has a passphrase, so the node cannot use it") || !strings.Contains(said, "ssh answered: git@github.com: Permission denied (publickey).") {
		t.Errorf("setup skipped or hid the passphrase problem:\n%s", said)
	}
}

func TestGitHubSSHFailureShowsSSHsAnswerAndTheSSOFixOnlyWhenItNamesSAML(t *testing.T) {
	cases := []struct {
		ssh string
		sso bool
	}{
		{"git@github.com: Permission denied (publickey).", false},
		{"ERROR: The 'acme' organization has enabled or enforced SAML SSO. To access this repository, you must re-authorize your SSH key.", true},
	}
	for _, c := range cases {
		s, out, _ := keySession(t, c.ssh)
		_, err := Run(context.Background(), s, []Step{githubKey()})
		said := out.String()
		if err == nil || !strings.Contains(said, c.ssh) || strings.Contains(said, "Configure SSO") != c.sso || c.sso && !strings.Contains(said, "authorise it for acme") {
			t.Errorf("GitHub answering %q gave %v and printed:\n%s\nwant ssh's answer, and the SSO fix only for SAML", c.ssh, err, said)
		}
	}
}
