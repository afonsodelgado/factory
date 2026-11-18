package main

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/afonsodelgado/factory/internal/install"
	"github.com/afonsodelgado/factory/internal/node"
	"github.com/afonsodelgado/factory/internal/node/nodetest"
	"github.com/afonsodelgado/factory/internal/phone"
	"github.com/afonsodelgado/factory/internal/state"
)

func TestDoctorReportsAConfigThatStopsTheHub(t *testing.T) {
	config := t.TempDir()
	if err := os.WriteFile(filepath.Join(config, "config.yml"), []byte("stalled_after: 30\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FACTORY_CONFIG_DIR", config)
	t.Setenv("FACTORY_STATE_DIR", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PATH", "")

	var out bytes.Buffer
	install.Report(&out, install.Doctor(doctorOptions()), "doctor")
	if !strings.Contains(out.String(), `stalled_after "30"`) {
		t.Errorf("doctor said nothing about the config that stops hub and open:\n%s", out.String())
	}
}

const tailnetHost = "devbox.tail1234.ts.net"

func noTailscale(context.Context, string, ...string) ([]byte, error) {
	return nil, &exec.Error{Name: "tailscale", Err: exec.ErrNotFound}
}

type fakeMachine struct {
	t      *testing.T
	cfg    *state.Config
	status string
	port   int

	mu        sync.Mutex
	commands  []string
	ours      bool
	foreign   bool
	funnel    bool
	loaded    bool
	silent    bool
	serveFail int
	node      []*http.Server
	running   string
}

func (f *fakeMachine) run(_ context.Context, name string, args ...string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	command := strings.Join(append([]string{name}, args...), " ")
	f.commands = append(f.commands, command)
	switch command {
	case "tailscale status --json":
		return []byte(f.status), nil
	case "tailscale serve status --json":
		return []byte(f.serveConfig()), nil
	case "tailscale " + strings.Join(node.ServeArgs(node.HomeHTTPS, f.port), " "):
		if f.serveFail > 0 {
			f.serveFail--
			return nil, errors.New("exit status 1: Access denied: serve config denied")
		}
		f.ours = true
	case "tailscale " + strings.Join(node.UnserveArgs(node.HomeHTTPS), " "):
		f.ours = false
	case "launchctl print gui/" + strconv.Itoa(os.Getuid()) + "/" + node.ServiceLabel, "systemctl --user show " + node.ServiceLabel + " --property=LoadState,ActiveState,MainPID":
		switch {
		case !f.loaded && name == "launchctl":
			return nil, errors.New("exit status 113: Could not find service")
		case !f.loaded:
			return []byte("LoadState=not-found\nActiveState=inactive\nMainPID=0\n"), nil
		case name == "launchctl":
			return []byte("\tstate = running\n\tpid = 4242\n"), nil
		}
		return []byte("LoadState=loaded\nActiveState=active\nMainPID=4242\n"), nil
	case "launchctl bootstrap gui/" + strconv.Itoa(os.Getuid()) + " " + filepath.Join(os.Getenv("HOME"), "Library", "LaunchAgents", node.ServiceLabel+".plist"), "systemctl --user restart " + node.ServiceLabel:
		f.loaded = true
		f.running = "9.9.9"
		f.stopNode()
		f.startNode()
	case "launchctl bootout gui/" + strconv.Itoa(os.Getuid()) + "/" + node.ServiceLabel, "systemctl --user disable --now " + node.ServiceLabel:
		f.loaded = false
		f.stopNode()
	case "loginctl show-user " + loginName() + " --property=Linger --value":
		return []byte("yes\n"), nil
	case "/opt/factory/bin/factory --version":
		return []byte("factory 9.9.9 (test)\n"), nil
	}
	return nil, nil
}

func loginName() string {
	u, err := user.Current()
	if err != nil {
		return os.Getenv("USER")
	}
	return u.Username
}

func (f *fakeMachine) serveConfig() string {
	tcp := []string{`"8443":{"HTTPS":true}`}
	web := []string{`"` + tailnetHost + `:8443":{"Handlers":{"/grafana":{"Proxy":"http://127.0.0.1:3000"}}}`}
	switch {
	case f.ours:
		tcp = append(tcp, `"443":{"HTTPS":true}`)
		web = append(web, `"`+tailnetHost+`:443":{"Handlers":{"/":{"Proxy":"`+node.Target(f.port)+`"}}}`)
	case f.foreign:
		tcp = append(tcp, `"443":{"HTTPS":true}`)
		web = append(web, `"`+tailnetHost+`:443":{"Handlers":{"/":{"Proxy":"http://127.0.0.1:3000"}}}`)
	}
	funnel := ""
	if f.funnel {
		funnel = `,"AllowFunnel":{"` + tailnetHost + `:8443":true}`
	}
	return `{"TCP":{` + strings.Join(tcp, ",") + `},"Web":{` + strings.Join(web, ",") + `}` + funnel + `}`
}

func (f *fakeMachine) startNode() {
	if f.silent {
		return
	}
	me := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		fmt.Fprintf(w, `{"version":%q}`, f.running)
	})
	for _, listen := range []func() (net.Listener, error){
		func() (net.Listener, error) { return node.ListenUnix(f.cfg.NodeSocket()) },
		func() (net.Listener, error) {
			return net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(f.port)))
		},
	} {
		ln, err := listen()
		if err != nil {
			f.t.Fatal(err)
		}
		server := &http.Server{Handler: me}
		go server.Serve(ln)
		f.node = append(f.node, server)
	}
}

func (f *fakeMachine) stopNode() {
	for _, server := range f.node {
		server.Close()
	}
	f.node = nil
}

func (f *fakeMachine) take() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	taken := f.commands
	f.commands = nil
	return taken
}

func newFakeMachine(t *testing.T) (*fakeMachine, nodeSetup, *bytes.Buffer) {
	t.Helper()
	stateDir, err := os.MkdirTemp("/tmp", "fins-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(stateDir) })
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	t.Setenv("FACTORY_CONFIG_DIR", t.TempDir())
	t.Setenv("FACTORY_STATE_DIR", stateDir)
	t.Setenv("PATH", "/usr/bin:/bin")
	t.Setenv("ANTHROPIC_API_KEY", "sk-secret-never-in-a-unit")
	f := &fakeMachine{t: t, port: freePort(t)}
	f.status = `{"BackendState":"Running","CertDomains":["` + tailnetHost + `"],"Self":{"DNSName":"` + tailnetHost + `.","UserID":7},"User":{"7":{"LoginName":"alice@example.com"}},"CurrentTailnet":{"MagicDNSSuffix":"tail1234.ts.net"}}`
	writeFile(t, filepath.Join(os.Getenv("FACTORY_CONFIG_DIR"), "config.yml"), fmt.Sprintf("node:\n  port: %d\n", f.port))
	cfg, err := state.LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	f.cfg = cfg
	t.Cleanup(func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.stopNode()
	})
	var out bytes.Buffer
	setup := nodeSetup{cfg: cfg, tailscale: node.Tailscale{Run: f.run}, service: node.NewService(cfg, "/opt/factory/bin/factory", home, f.run), app: func() error { return nil }, wait: 5 * time.Second, out: &out}
	return f, setup, &out
}

func loadSequence(home string) []string {
	if runtime.GOOS == "linux" {
		return []string{"loginctl show-user " + loginName() + " --property=Linger --value", "systemctl --user daemon-reload", "systemctl --user enable " + node.ServiceLabel, "systemctl --user restart " + node.ServiceLabel}
	}
	target := "gui/" + strconv.Itoa(os.Getuid()) + "/" + node.ServiceLabel
	return []string{"launchctl print " + target, "launchctl enable " + target, "launchctl bootstrap gui/" + strconv.Itoa(os.Getuid()) + " " + filepath.Join(home, "Library", "LaunchAgents", node.ServiceLabel+".plist")}
}

func qrBlock(t *testing.T, url string) string {
	t.Helper()
	code, err := phone.QR(url)
	if err != nil {
		t.Fatal(err)
	}
	return "  " + strings.Join(code, "\n  ") + "\n"
}

func TestNodeInstallStartsAPrivateServiceBeforeServeAndShowsOnlyTheAppURL(t *testing.T) {
	f, setup, out := newFakeMachine(t)
	if err := setup.install(context.Background()); err != nil {
		t.Fatalf("install: %v\n%s", err, out)
	}
	want := append([]string{"tailscale status --json", "tailscale serve status --json"}, loadSequence(os.Getenv("HOME"))...)
	want = append(want, "tailscale "+strings.Join(node.ServeArgs(node.HomeHTTPS, f.port), " "))
	if got := f.take(); !slices.Equal(got, want) {
		t.Fatalf("install ran\n  %s\nwant\n  %s\nso Serve is configured only after the service answers", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
	unit, err := os.ReadFile(setup.service.UnitPath())
	if err != nil {
		t.Fatal(err)
	}
	for _, wanted := range []string{"/opt/factory/bin/factory", "node", "run", os.Getenv("HOME"), "PATH", "/usr/bin:/bin", "FACTORY_STATE_DIR", setup.service.Log} {
		if !strings.Contains(string(unit), wanted) {
			t.Errorf("the unit lacks %q, so the service would not run this binary in this environment:\n%s", wanted, unit)
		}
	}
	if strings.Contains(string(unit), "sk-secret") {
		t.Errorf("the unit carries a secret from the installing shell:\n%s", unit)
	}
	if info, err := os.Stat(setup.service.Log); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("the node's log is %v, %v; want a private 0600 file", info.Mode(), err)
	}
	cfg, _ := state.LoadConfig()
	if cfg.Node.Owner != "alice@example.com" || cfg.NodeHostname() != tailnetHost {
		t.Errorf("install saved owner %q and hostname %q; want Tailscale's login and name so the node answers its own URL", cfg.Node.Owner, cfg.NodeHostname())
	}
	if !strings.Contains(out.String(), qrBlock(t, "https://"+tailnetHost)) {
		t.Errorf("the first install did not print a QR of exactly https://%s:\n%s", tailnetHost, out)
	}

	out.Reset()
	if err := setup.install(context.Background()); err != nil {
		t.Fatalf("reinstall: %v", err)
	}
	if got := f.take(); slices.Contains(got, "tailscale "+strings.Join(node.ServeArgs(node.HomeHTTPS, f.port), " ")) || strings.Contains(out.String(), "█") {
		t.Errorf("a second install ran %v and printed\n%s\nwant the existing mapping adopted and no second QR", got, out)
	}
}

func TestBootstrapRestartsANodeStillRunningAnOlderBinary(t *testing.T) {
	f, setup, out := newFakeMachine(t)
	if err := setup.install(context.Background()); err != nil {
		t.Fatalf("install: %v\n%s", err, out)
	}
	defer func(was string) { version = was }(version)
	version = "9.9.9"
	f.mu.Lock()
	f.running = "9.9.8"
	f.mu.Unlock()
	f.take()

	setup.reconcile(context.Background())
	restart := loadSequence(os.Getenv("HOME"))
	if got := f.take(); !slices.Contains(got, restart[len(restart)-1]) {
		t.Fatalf("a node on 9.9.8 under a 9.9.9 binary was left running old code; reconcile ran %v\n%s", got, out)
	}
	setup.reconcile(context.Background())
	if got := f.take(); slices.Contains(got, restart[len(restart)-1]) {
		t.Errorf("a node already on this binary's version was restarted again: %v", got)
	}
}

func TestAnInterruptedInstallIsStoppedThenRepairedByTheNextOne(t *testing.T) {
	f, setup, out := newFakeMachine(t)
	f.silent, setup.wait = true, 300*time.Millisecond
	err := setup.install(context.Background())
	if err == nil || !strings.Contains(err.Error(), setup.service.Log) {
		t.Fatalf("a node that never answers gave %v; want a failure naming its log", err)
	}
	if got := f.take(); !strings.Contains(got[len(got)-1], "bootout") && !strings.Contains(got[len(got)-1], "disable --now") || f.loaded {
		t.Fatalf("after the node never answered the commands were %v; want the service unloaded so it does not restart forever", got)
	}

	f.silent, f.serveFail, setup.wait = false, 1, 5*time.Second
	if err := setup.install(context.Background()); err == nil || !strings.Contains(err.Error(), "Serve") {
		t.Fatalf("a refused serve gave %v; want a failure naming Serve", err)
	}
	if record, _ := node.LoadInstall(f.cfg.StateDir); record.Complete || record.Binary == "" || !f.loaded {
		t.Fatalf("after Serve failed the record is %+v and the service loaded=%v; want the running service recorded and the install incomplete", record, f.loaded)
	}

	out.Reset()
	if err := setup.install(context.Background()); err != nil {
		t.Fatalf("the retry: %v", err)
	}
	if record, _ := node.LoadInstall(f.cfg.StateDir); !record.Complete || record.Serve != node.Target(f.port) || !f.ours {
		t.Fatalf("after the retry the record is %+v and the mapping ours=%v; want both repaired", record, f.ours)
	}
	if !strings.Contains(out.String(), qrBlock(t, "https://"+tailnetHost)) {
		t.Errorf("the install that completed first printed no QR:\n%s", out)
	}
}

func TestInstallRefusesAnUnsafeTailnetBeforeChangingAnything(t *testing.T) {
	cases := []struct {
		name   string
		change func(*fakeMachine)
		says   string
	}{
		{"another service on 443", func(f *fakeMachine) { f.foreign = true }, "http://127.0.0.1:3000"},
		{"funnel on another port", func(f *fakeMachine) { f.funnel = true }, "tailscale funnel --https=8443 --set-path=/grafana off"},
		{"a tagged device", func(f *fakeMachine) {
			f.status = strings.Replace(f.status, `"UserID":7`, `"UserID":7,"Tags":["tag:server"]`, 1)
		}, node.UntagFix},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f, setup, _ := newFakeMachine(t)
			c.change(f)
			err := setup.install(context.Background())
			if err == nil || !strings.Contains(err.Error(), c.says) {
				t.Fatalf("install gave %v; want a refusal naming %q", err, c.says)
			}
			if got := f.take(); len(got) > 2 {
				t.Errorf("a refused install ran %v; want only the two status reads", got)
			}
			if _, err := os.Stat(setup.service.UnitPath()); err == nil {
				t.Error("a refused install wrote the service unit")
			}
			if cfg, _ := state.LoadConfig(); cfg.Node.Owner != "" {
				t.Errorf("a refused install saved owner %q", cfg.Node.Owner)
			}
		})
	}
}

func TestUninstallRemovesOnlyItsUnitAndMappingAndStatusStillAnswers(t *testing.T) {
	f, setup, out := newFakeMachine(t)
	if err := setup.install(context.Background()); err != nil {
		t.Fatal(err)
	}
	kept := []string{f.cfg.RegistryPath(), filepath.Join(f.cfg.StateDir, "phone", "subscriptions.json"), f.cfg.ConfigFile()}
	for _, path := range kept[:2] {
		writeFile(t, path, "{}")
	}
	f.take()
	if err := setup.uninstall(context.Background()); err != nil {
		t.Fatal(err)
	}
	var serveChanges []string
	for _, command := range f.take() {
		if strings.HasPrefix(command, "tailscale serve") && command != "tailscale serve status --json" || strings.HasPrefix(command, "tailscale funnel") {
			serveChanges = append(serveChanges, command)
		}
	}
	if want := []string{"tailscale " + strings.Join(node.UnserveArgs(node.HomeHTTPS), " ")}; !slices.Equal(serveChanges, want) {
		t.Errorf("uninstall changed Serve with %v; want only %v", serveChanges, want)
	}
	if !strings.Contains(f.serveConfig(), "/grafana") {
		t.Error("uninstall removed an unrelated Serve entry")
	}
	if _, err := os.Stat(setup.service.UnitPath()); err == nil || f.loaded {
		t.Errorf("the unit is still there or loaded (%v)", f.loaded)
	}
	for _, path := range kept {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("uninstall removed %s", path)
		}
	}

	out.Reset()
	setup.status(context.Background())
	for _, wanted := range []string{"not installed", "not answering", "127.0.0.1:" + strconv.Itoa(f.port), "tail1234.ts.net"} {
		if !strings.Contains(out.String(), wanted) {
			t.Errorf("status of a stopped node lacks %q:\n%s", wanted, out)
		}
	}
}

func TestNodeRunLearnsItsOwnerAndNameFromTailscaleAndRunsOfflineOnSavedSettings(t *testing.T) {
	cases := []struct {
		name, saved, owner, wantOwner, wantHost string
		online                                  bool
		refused                                 bool
	}{
		{name: "a fresh machine online", online: true, wantOwner: "alice@example.com", wantHost: tailnetHost},
		{name: "a saved owner online", saved: "node:\n  owner: bob@example.com\n", owner: "bob@example.com", online: true, wantOwner: "bob@example.com", wantHost: tailnetHost},
		{name: "a saved owner offline", saved: "node:\n  owner: bob@example.com\n", owner: "bob@example.com", wantOwner: "bob@example.com"},
		{name: "a fresh machine offline", refused: true},
		{name: "an owner given to a named machine offline", saved: "node:\n  hostname: " + tailnetHost + "\n", owner: "carol@example.com", wantOwner: "carol@example.com", wantHost: tailnetHost},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f, _, _ := newFakeMachine(t)
			writeFile(t, f.cfg.ConfigFile(), "hub:\n  session: factory\n"+c.saved)
			cfg, err := state.LoadConfig()
			if err != nil {
				t.Fatal(err)
			}
			ts := node.Tailscale{Run: noTailscale}
			if c.online {
				ts.Run = f.run
			}
			owner, err := learnIdentity(context.Background(), cfg, c.owner, ts)
			if c.refused {
				if err == nil || !strings.Contains(err.Error(), "--owner") {
					t.Fatalf("got %q, %v; want setup guidance", owner, err)
				}
				return
			}
			saved, _ := state.LoadConfig()
			if err != nil || owner != c.wantOwner || cfg.NodeHostname() != c.wantHost || saved.NodeHostname() != c.wantHost || saved.Node.Owner != c.wantOwner {
				t.Fatalf("got owner %q (saved %q), hostname %q (saved %q), %v; want %q and %q", owner, saved.Node.Owner, cfg.NodeHostname(), saved.NodeHostname(), err, c.wantOwner, c.wantHost)
			}
		})
	}
}

func TestFunnelOnRefusesTheTailnetWhileTheSocketStillAnswers(t *testing.T) {
	config := t.TempDir()
	t.Setenv("FACTORY_CONFIG_DIR", config)
	t.Setenv("HOME", t.TempDir())
	shortStateDir(t)
	funnel := func(_ context.Context, name string, args ...string) ([]byte, error) {
		return []byte(`{"TCP":{"443":{"HTTPS":true}},"Web":{"` + tailnetHost + `:443":{"Handlers":{"/":{"Proxy":"http://127.0.0.1:1"}}}},"AllowFunnel":{"` + tailnetHost + `:443":true}}`), nil
	}
	cfg := scratchNode(t, config, node.Tailscale{Run: funnel})
	var body string
	waitFor(t, "the node to name the Funnel fix over TCP", func() bool {
		r, _ := http.NewRequest(http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/api/me", cfg.NodePort()), nil)
		r.Header.Set(node.LoginHeader, "owner@example.com")
		resp, err := http.DefaultClient.Do(r)
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		var b bytes.Buffer
		b.ReadFrom(resp.Body)
		body = b.String()
		if resp.StatusCode == http.StatusOK {
			t.Fatalf("the owner's request through Serve answered 200 %s while Funnel is on", body)
		}
		return strings.Contains(body, "tailscale funnel --https=443 off")
	})
	if !node.Answers(cfg.NodeSocket()) {
		t.Fatal("the local socket stopped answering while Funnel is on; doctor and the hub need it to explain the fix")
	}
}

func TestInstallJoinsAFoundNodeOnlyOnAYesOrAFlag(t *testing.T) {
	tn := nodetest.New(t)
	macbook := tn.Machine("macbook").Start()
	studio := tn.Machine("studio").Start()
	var out bytes.Buffer
	offer := func(answer string, tty bool, join joinChoice) (bool, error) {
		out.Reset()
		s := nodeSetup{out: &out, in: strings.NewReader(answer), tty: tty, join: join}
		joined, _, err := s.offerJoin(context.Background(), studio.Local, "studio")
		return joined, err
	}

	if joined, err := offer("", false, askToJoin); joined || err == nil || !strings.Contains(err.Error(), "--join") {
		t.Errorf("install without a terminal or flag joined %v, %v; want a refusal naming --join and --no-join", joined, err)
	}
	if joined, err := offer("", false, neverJoin); joined || err != nil {
		t.Errorf("--no-join answered %v, %v", joined, err)
	}
	if joined, err := offer("n\n", true, askToJoin); joined || err != nil || !strings.Contains(out.String(), "Join it? ") {
		t.Errorf("answering n joined %v, %v:\n%s", joined, err, &out)
	}
	if got := studio.Names(); len(got) != 1 {
		t.Fatalf("studio lists %v before any yes; want nothing written", got)
	}
	if joined, err := offer("\n", true, askToJoin); !joined || err != nil || !strings.Contains(out.String(), "copied from macbook over the tailnet") {
		t.Fatalf("pressing Enter joined %v, %v:\n%s\nwant the join and macbook's push key copied", joined, err, &out)
	}
	if joined, err := offer("", false, askToJoin); joined || err != nil || !strings.Contains(out.String(), "already shares") {
		t.Errorf("installing again joined %v, %v:\n%s; want it to find the list it already shares", joined, err, &out)
	}
	for _, n := range []*nodetest.Node{macbook, studio} {
		if got := n.Names(); !slices.Equal(got, []string{"macbook", "studio"}) {
			t.Errorf("%s lists %v after the join", n.Short(), got)
		}
	}
}

func TestPushKeyExportWritesOnlyANewPrivateFileAndImportNeverSilencesPhonesUnasked(t *testing.T) {
	from, to := phone.Open(t.TempDir()), phone.Open(t.TempDir())
	for _, store := range []phone.Store{from, to} {
		if _, err := store.VAPIDKey(""); err != nil {
			t.Fatal(err)
		}
	}
	key, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := to.Subscribe(phone.Subscription{Endpoint: "https://push.example/ipad", P256dh: base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes()), Auth: base64.RawURLEncoding.EncodeToString(make([]byte, 16)), Since: time.Now()}); err != nil {
		t.Fatal(err)
	}
	secret, _ := from.Export()
	own, _ := to.KeyState()
	file := filepath.Join(t.TempDir(), "push-key.json")
	var out bytes.Buffer

	if err := exportPushKey(from, file, &out); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(file); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("the exported key file is %v (%v); want only its owner to read it", info, err)
	}
	if err := exportPushKey(from, file, &out); err == nil {
		t.Error("export overwrote an existing file")
	}
	if err := importPushKey(to, file, false, nil, &out); err == nil {
		t.Error("import replaced a key a phone is subscribed to without --replace")
	}
	if now, _ := to.KeyState(); now.Fingerprint != own.Fingerprint {
		t.Fatal("a refused import still changed the key")
	}
	data, _ := os.ReadFile(file)
	if err := importPushKey(to, "-", true, bytes.NewReader(data), &out); err != nil {
		t.Fatal(err)
	}
	if now, _ := to.KeyState(); now.Fingerprint != secret.Fingerprint() {
		t.Errorf("after import --replace the node sends with %s; want the exported key %s", now.Fingerprint, secret.Fingerprint())
	}
	if strings.Contains(out.String(), secret.Private) {
		t.Errorf("export or import printed the private key:\n%s", &out)
	}
}
