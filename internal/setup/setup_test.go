package setup

import (
	"bufio"
	"bytes"
	"context"
	"errors"
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
