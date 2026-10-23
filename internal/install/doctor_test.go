package install

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestDoctorReportsWhatIsMissingWithTheFix(t *testing.T) {
	t.Setenv("PATH", "")
	dir := t.TempDir()
	o := Options{ClaudeDir: dir, Binary: "/opt/factory/bin/factory", Home: t.TempDir()}

	var out bytes.Buffer
	problems, _ := Report(&out, Doctor(o), "doctor")
	for _, name := range []string{"claude", "git", "tmux", "gh", "gh-stack", "revmux", "impeccable", "bundle", "hooks"} {
		check := find(Doctor(o), name)
		if check.Mark != Fail || check.Fix == "" {
			t.Errorf("%s on a fresh machine = %s %q, fix %q; want a failure with its fix command", name, check.Mark, check.Detail, check.Fix)
		}
	}
	if problems < 9 || !strings.Contains(out.String(), "fix  gh extension install github/gh-stack") {
		t.Errorf("report:\n%s", out.String())
	}

	plan, err := NewPlan(dir, o.Binary)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(plan, time.Now()); err != nil {
		t.Fatal(err)
	}
	if bundle := find(Doctor(o), "bundle"); bundle.Mark != Pass {
		t.Errorf("after bootstrap the bundle check says %s %q", bundle.Mark, bundle.Detail)
	}
	if hooks := find(Doctor(o), "hooks"); hooks.Mark != Pass {
		t.Errorf("after bootstrap the hooks check says %s %q", hooks.Mark, hooks.Detail)
	}
}

func find(checks []Check, name string) Check {
	for _, c := range checks {
		if c.Name == name {
			return c
		}
	}
	return Check{}
}
