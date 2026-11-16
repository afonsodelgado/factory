package install

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/afonsodelgado/factory/internal/catalog"
	"github.com/afonsodelgado/factory/internal/phone"
	"github.com/afonsodelgado/factory/internal/state"
)

func TestDoctorChecksToolsPickedOnlyForOneFactory(t *testing.T) {
	cfg := &state.Config{StateDir: t.TempDir()}
	registry := `{"factories":[
		{"repo":"/r/cowork","slug":"csv","supervisor":{"tool":"codex"}},
		{"repo":"/r/cowork","slug":"old","archived":true,"agents":{"reviewer":{"tool":"opencode","model":"big-pickle"}}}]}`
	os.WriteFile(cfg.RegistryPath(), []byte(registry), 0o644)
	used := toolsInUse(Options{Config: cfg})
	if !slices.Equal(used[state.ToolCodex], []string{"/r/cowork"}) {
		t.Errorf("tools in use = %v; want codex in /r/cowork, or a Codex supervisor picked in the dialog gets no login, trust or sandbox check", used)
	}
	if _, ok := used[state.ToolOpenCode]; ok {
		t.Errorf("tools in use = %v; an archived factory's pick must not add OpenCode checks", used)
	}
}

func TestDoctorChecksCodexAndOpenCodeValuesAgainstTheirCatalogs(t *testing.T) {
	codexHome, stateDir := t.TempDir(), t.TempDir()
	t.Setenv("CODEX_HOME", codexHome)
	os.WriteFile(filepath.Join(codexHome, "auth.json"), []byte(`{"auth_mode":"chatgpt"}`), 0o600)
	os.WriteFile(filepath.Join(codexHome, "models_cache.json"), []byte(`{"models":[{"slug":"gpt-5.6-sol","visibility":"list","supported_reasoning_levels":[{"effort":"low"},{"effort":"high"}]}]}`), 0o644)
	freeModels := `[{"id":"big-pickle"},{"id":"flint-40b-free","efforts":["low","high"]}]`
	cases := []struct {
		name    string
		setting state.AgentSetting
		cached  bool
		want    Mark
	}{
		{name: "a listed codex model and effort", setting: state.AgentSetting{Tool: state.ToolCodex, Model: "gpt-5.6-sol", Effort: "high"}, cached: true},
		{name: "a codex model typo", setting: state.AgentSetting{Tool: state.ToolCodex, Model: "gpt-5.6-soul"}, cached: true, want: Fail},
		{name: "an opencode variant the model lacks", setting: state.AgentSetting{Tool: state.ToolOpenCode, Model: "flint-40b-free", Effort: "max"}, cached: true, want: Fail},
		{name: "a paid zen model", setting: state.AgentSetting{Tool: state.ToolOpenCode, Model: "paid-pro"}, cached: true, want: Fail},
		{name: "no opencode catalog yet", setting: state.AgentSetting{Tool: state.ToolOpenCode, Model: "big-pickle"}, want: Warn},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cache := catalog.OpenCodeCachePath(stateDir)
			os.RemoveAll(cache)
			if c.cached {
				os.MkdirAll(filepath.Dir(cache), 0o755)
				os.WriteFile(cache, []byte(freeModels), 0o644)
			}
			cfg := &state.Config{StateDir: stateDir, Agents: map[string]state.AgentSetting{"engineer": c.setting}}
			checks := agentValueChecks(Options{Config: cfg, ConfigFile: "config.yml"})
			got := Mark("")
			if len(checks) > 0 {
				got = checks[0].Mark
			}
			if got != c.want {
				t.Errorf("doctor marks %+v as %q %+v; want %q, or the value passes doctor and open and fails only inside the pane", c.setting, got, checks, c.want)
			}
		})
	}
}

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

	if runtime.GOOS != "darwin" {
		bin := t.TempDir()
		if err := os.WriteFile(filepath.Join(bin, "pacman"), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PATH", bin)
		if fix := find(Doctor(o), "gh").Fix; !strings.HasPrefix(fix, "sudo pacman -S --needed --noconfirm github-cli") {
			t.Errorf("gh fix on Arch = %q; want pacman installing github-cli, not apt-get", fix)
		}
		t.Setenv("PATH", "")
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

func TestDoctorNamesEachPhoneSetupGapAndItsFix(t *testing.T) {
	const host = "devbox.tail1234.ts.net"
	running := `{"BackendState":"Running","CertDomains":["` + host + `"],"Self":{"DNSName":"` + host + `."}}`
	served := `{"Web":{"` + host + `:443":{"Handlers":{"/":{"Proxy":"http://127.0.0.1:7418"}}}}}`
	cases := []struct {
		name, status, serve string
		missing, offline    bool
		failing             []string
	}{
		{name: "everything set up", status: running, serve: served},
		{name: "no tailscale", missing: true, failing: []string{"tailscale"}},
		{name: "logged out", status: `{"BackendState":"NeedsLogin"}`, failing: []string{"tailscale"}},
		{name: "HTTPS off", status: strings.Replace(running, `"CertDomains":["`+host+`"],`, "", 1), serve: served, failing: []string{"https cert"}},
		{name: "no serve mapping", status: running, serve: `{}`, failing: []string{"serve"}},
		{name: "serve status unreadable", status: running, serve: `not json`, failing: []string{"serve", "funnel"}},
		{name: "funnel on", status: running, serve: strings.TrimSuffix(served, "}") + `,"AllowFunnel":{"` + host + `:443":true}}`, failing: []string{"funnel"}},
		{name: "push services unreachable", status: running, serve: served, offline: true, failing: []string{"push reach"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			probe := phone.Probe{
				Tailscale: func(_ context.Context, args ...string) ([]byte, error) {
					switch {
					case c.missing:
						return nil, &exec.Error{Name: "tailscale", Err: exec.ErrNotFound}
					case args[0] == "serve":
						return []byte(c.serve), nil
					}
					return []byte(c.status), nil
				},
				DialTLS: func(context.Context, string) error {
					if c.offline {
						return errors.New("dial tcp: no route to host")
					}
					return nil
				},
			}
			cfg := &state.Config{Machine: state.Machine{Phone: state.Phone{Enabled: true, Hostname: host}}}
			var failing []string
			for _, check := range PhoneChecks(Options{Config: cfg, PhoneProbe: probe}) {
				if check.Mark == Fail {
					failing = append(failing, check.Name)
					if check.Fix == "" {
						t.Errorf("%s fails with no fix", check.Name)
					}
				}
			}
			if !slices.Equal(failing, c.failing) {
				t.Errorf("failing checks %v; want %v", failing, c.failing)
			}
		})
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
