package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/afonsodelgado/factory/bundle"
	"github.com/afonsodelgado/factory/internal/host"
	"github.com/afonsodelgado/factory/internal/phone"
	"github.com/afonsodelgado/factory/internal/state"
)

type phoneMachine struct {
	standIn
	repo, stateDir, origin, hostname string
	port                             int
	cookie                           string
}

func newPhoneMachine(t *testing.T) phoneMachine {
	t.Helper()
	s := newStandIn(t)
	m := phoneMachine{standIn: s, hostname: "devbox.tail1234.ts.net", port: freePort(t)}
	m.origin = "https://" + m.hostname
	local := filepath.Join(s.root, "local")
	m.stateDir = filepath.Join(local, "state")
	m.repo = filepath.Join(local, "repo")
	writeFile(t, filepath.Join(s.bin, "tailscale"), "#!/bin/sh\nexit 1\n")
	os.Chmod(filepath.Join(s.bin, "tailscale"), 0o755)
	writeFile(t, filepath.Join(m.repo, ".claude", "factory", "csv", "state.json"), `{"id": "csv", "stage": "requirements"}`)
	writeFile(t, filepath.Join(m.repo, ".claude", "factory", "csv", "plan.html"), "<html><body><h1>plan</h1></body></html>")
	writeFile(t, filepath.Join(local, "config", "config.yml"), fmt.Sprintf("repos:\n  - %s\nreview:\n  port: %d\nphone:\n  enabled: true\n  port: %d\n  hostname: %s\n", m.repo, freePort(t), m.port, m.hostname))
	m.env = append(m.env, "HOME="+filepath.Join(local, "home"), "FACTORY_CONFIG_DIR="+filepath.Join(local, "config"), "FACTORY_STATE_DIR="+m.stateDir,
		"CLAUDE_CONFIG_DIR="+filepath.Join(local, "claude"), "FACTORY_TMUX_SOCKET="+s.tmux)
	return m
}

func (m phoneMachine) command(args ...string) *exec.Cmd {
	cmd := exec.Command(filepath.Join(m.bin, "factory"), args...)
	cmd.Dir, cmd.Env = m.repo, m.env
	return cmd
}

func (m phoneMachine) tmux(t *testing.T, args ...string) string {
	t.Helper()
	out, err := exec.Command("tmux", append([]string{"-L", m.standIn.tmux}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("tmux %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func (m phoneMachine) owner() phone.OwnerInfo {
	cfg := &state.Config{StateDir: m.stateDir}
	info, _ := phone.CurrentOwner(cfg)
	return info
}

func (m phoneMachine) waitForOwner(t *testing.T, mode string) phone.OwnerInfo {
	t.Helper()
	var info phone.OwnerInfo
	waitFor(t, "the runtime to be owned by the "+mode, func() bool {
		info = m.owner()
		return info.Mode == mode
	})
	return info
}

func (m phoneMachine) startHub(t *testing.T) string {
	t.Helper()
	args := []string{"new-window", "-d", "-t", "factory:", "-n", "hub", "-P", "-F", "#{pane_id}"}
	for _, kv := range m.env {
		if name, _, _ := strings.Cut(kv, "="); name == "PATH" || name == "SHELL" || strings.HasPrefix(name, "FACTORY_") || name == "HOME" || name == "CLAUDE_CONFIG_DIR" {
			args = append(args, "-e", kv)
		}
	}
	return m.tmux(t, append(args, filepath.Join(m.bin, "factory")+" hub; exec sleep 600")...)
}

func (m *phoneMachine) call(t *testing.T, method, path, body string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest(method, fmt.Sprintf("http://127.0.0.1:%d%s", m.port, path), strings.NewReader(body))
	req.Host = m.hostname
	if method == http.MethodPost {
		req.Header.Set("Origin", m.origin)
		req.Header.Set("Content-Type", "application/json")
	}
	if m.cookie != "" {
		req.Header.Set("Cookie", m.cookie)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err.Error()
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	for _, c := range resp.Cookies() {
		if c.Name == phone.CookieName {
			m.cookie = c.Name + "=" + c.Value
		}
	}
	return resp.StatusCode, string(data)
}

func TestTheRuntimeChangesHandsBetweenServeAndTheHubWithoutLosingAQueuedMessage(t *testing.T) {
	m := newPhoneMachine(t)
	busy := m.tmux(t, "new-window", "-d", "-t", "scratch:", "-n", "p-csv", "-P", "-F", "#{pane_id}", "exec sleep 600")
	for _, o := range [][2]string{{"@factory_repo", m.repo}, {"@factory_slug", "csv"}, {"@factory_token", "tok-csv"}} {
		m.tmux(t, "set-option", "-p", "-t", busy, o[0], o[1])
	}
	hook := m.command("hook")
	hook.Env = append(hook.Env, "FACTORY_PANE_TOKEN=tok-csv")
	hook.Stdin = strings.NewReader(fmt.Sprintf(`{"hook_event_name":"UserPromptSubmit","session_id":"s-csv","prompt":"work","cwd":%q}`, m.repo))
	if out, err := hook.CombinedOutput(); err != nil {
		t.Fatalf("hook: %v\n%s", err, out)
	}

	serve := m.command("serve")
	if err := serve.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { serve.Process.Signal(syscall.SIGTERM); serve.Wait() })
	fallback := m.waitForOwner(t, phone.ModeLocal)
	if out, err := m.command("serve").CombinedOutput(); err == nil || !strings.Contains(string(out), "already running") {
		t.Fatalf("a second serve: %v\n%s; want it refused while the first keeps its socket", err, out)
	}

	code, _, err := phone.Open(m.stateDir).NewCode(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if status, body := m.call(t, "POST", "/api/pair/exchange", `{"code":"`+code+`"}`); status != 200 {
		t.Fatalf("pairing: %d %s", status, body)
	}
	var snap phone.Snapshot
	waitFor(t, "the gateway to read the fallback's factories", func() bool {
		status, body := m.call(t, "GET", "/api/snapshot", "")
		return status == 200 && json.Unmarshal([]byte(body), &snap) == nil && len(snap.Factories) == 1
	})
	m.auditRoutes(t, snap.Factories[0])
	m.openPlan(t, snap.Factories[0].Route, "the headless fallback")
	op := uuid.NewString()
	action := fmt.Sprintf(`{"operation":%q,"incarnation":%q,"action":"message","text":"also export as tsv"}`, op, snap.Factories[0].Incarnation)
	status, body := m.call(t, "POST", "/api/factories/"+strings.ReplaceAll(snap.Factories[0].Key, "/", "%2F")+"/actions", action)
	if status != 200 || !strings.Contains(body, `"status":"queued"`) {
		t.Fatalf("message to a busy supervisor: %d %s; want it queued", status, body)
	}

	hub := m.startHub(t)
	m.waitForOwner(t, phone.ModeHub)
	if alive(fallback.PID) {
		t.Fatalf("the fallback (pid %d) still runs beside the hub", fallback.PID)
	}
	waitFor(t, "the hub to answer the queued message's receipt", func() bool {
		status, body = m.call(t, "GET", "/api/operations/"+op, "")
		return status == 200
	})
	if !strings.Contains(body, `"status":"queued"`) {
		t.Fatalf("receipt after the hub took over: %s; want the message still queued, not lost or typed twice", body)
	}
	m.openPlan(t, snap.Factories[0].Route, "the hub")

	m.tmux(t, "send-keys", "-t", hub, "q")
	m.waitForOwner(t, phone.ModeLocal)
	if panes := m.tmux(t, "list-panes", "-a", "-F", "#{pane_id}"); !strings.Contains(panes, busy) {
		t.Fatalf("the supervisor pane %s closed when the hub quit; panes %s", busy, panes)
	}

	m.startHub(t)
	crashed := m.waitForOwner(t, phone.ModeHub)
	syscall.Kill(crashed.PID, syscall.SIGKILL)
	again := m.waitForOwner(t, phone.ModeLocal)
	syscall.Kill(again.PID, syscall.SIGKILL)
	waitFor(t, "serve to restart a crashed fallback", func() bool {
		info := m.owner()
		return info.Mode == phone.ModeLocal && info.PID != again.PID
	})
	if status, body = m.call(t, "GET", "/api/operations/"+op, ""); status != 200 || !strings.Contains(body, `"status":"queued"`) {
		t.Fatalf("receipt after two crashes: %d %s; want it still queued", status, body)
	}
}

func TestPhoneStopReleasesAFallbackWhoseServeDied(t *testing.T) {
	m := newPhoneMachine(t)
	serve := m.command("serve")
	if err := serve.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { serve.Process.Kill(); serve.Wait() })
	fallback := m.waitForOwner(t, phone.ModeLocal)
	serve.Process.Kill()
	serve.Wait()
	if out, err := m.command("phone", "stop").CombinedOutput(); err != nil {
		t.Fatalf("phone stop: %v\n%s", err, out)
	}
	waitFor(t, "phone stop to release the fallback its dead serve left behind", func() bool { return !alive(fallback.PID) })
}

func alive(pid int) bool { return syscall.Kill(pid, 0) == nil }

func (m *phoneMachine) openPlan(t *testing.T, route, owner string) {
	t.Helper()
	var body string
	waitFor(t, owner+" to serve the plan to the phone", func() bool {
		var status int
		status, body = m.call(t, "GET", "/r/"+route+"/plan.html", "")
		return status == 200
	})
	if !strings.Contains(body, "data-mobile") {
		t.Fatalf("plan page while %s owns the runtime:\n%s\nwant the phone overlay", owner, body)
	}
}

func (m *phoneMachine) auditRoutes(t *testing.T, f host.LocalFactory) {
	t.Helper()
	actions := "/api/factories/" + strings.ReplaceAll(f.Key, "/", "%2F") + "/actions"
	for _, action := range []string{"delete", "archive", "unarchive", "create", "open"} {
		body := fmt.Sprintf(`{"operation":%q,"incarnation":%q,"action":%q}`, uuid.NewString(), f.Incarnation, action)
		if status, reply := m.call(t, "POST", actions, body); status == 200 {
			t.Errorf("the phone ran %s: %s", action, reply)
		}
	}
	for _, path := range []string{"/api/factories", "/api/remotes", "/api/host", "/api/review", "/r/" + f.Route + "/_api/console", "/r/" + f.Route + "/_api/threads/t-1/shot"} {
		if status, _ := m.call(t, "POST", path, `{}`); status == 200 {
			t.Errorf("%s answered 200; the phone gateway must not expose it", path)
		}
	}
	_, appErr := phone.App(bundle.Files)
	status, body := m.call(t, "GET", "/", "")
	switch {
	case appErr == nil && (status != 200 || !strings.Contains(body, `id="root"`)):
		t.Errorf("the embedded app's shell answered %d %q", status, body)
	case appErr != nil && (status != 503 || !strings.Contains(body, "make build")):
		t.Errorf("a build without the app answered %d %q; want the run make build page", status, body)
	}
}
