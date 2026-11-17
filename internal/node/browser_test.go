package node

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"maps"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/remote"

	"github.com/afonsodelgado/factory/internal/factory"
	"github.com/afonsodelgado/factory/internal/host"
	"github.com/afonsodelgado/factory/internal/state"
)

func TestABrowserJobRunsItsRecipeInOrderAndNamesWhatIsMissing(t *testing.T) {
	healthy := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	t.Cleanup(healthy.Close)

	t.Run("an undeclared sign-in is a named failure, not an unattended guess", func(t *testing.T) {
		var p state.Profile
		p.QA.Seed, p.QA.Health, p.QA.WebPort = state.NotNeeded, healthy.URL, 3001
		if _, err := RecipeOf(p, t.TempDir()); err == nil || !strings.Contains(err.Error(), "qa.sign_in") {
			t.Fatalf("a recipe without sign-in gave %v; want the missing key named", err)
		}
	})

	recipe := func(dir string) Recipe {
		return Recipe{Dir: dir, URL: healthy.URL, Health: healthy.URL, HealthStatus: http.StatusOK, Startup: 5 * time.Second, Seed: "echo seed >> steps", SignIn: "echo sign_in >> steps; echo signed in"}
	}

	t.Run("a node without npx stops the job by name before anything is seeded", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("PATH", t.TempDir())
		job, err := (&Browsers{}).StartJob(context.Background(), recipe(dir), "", nil)
		if _, seeded := os.Stat(filepath.Join(dir, "steps")); job != nil || err == nil || !strings.Contains(err.Error(), "npx") || seeded == nil {
			t.Fatalf("the job gave %v; want a failure naming npx with nothing seeded", err)
		}
	})

	t.Run("seed runs before sign-in, and sign-in output that is not cookies stops the job by name", func(t *testing.T) {
		dir, bin := t.TempDir(), t.TempDir()
		if err := os.WriteFile(filepath.Join(bin, "npx"), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PATH", bin)
		job, err := (&Browsers{}).StartJob(context.Background(), recipe(dir), "", nil)
		steps, _ := os.ReadFile(filepath.Join(dir, "steps"))
		if job != nil || err == nil || !strings.Contains(err.Error(), "qa.sign_in") || string(steps) != "seed\nsign_in\n" {
			t.Fatalf("the job gave %v after %q; want seed, then sign-in, then a failure naming qa.sign_in", err, steps)
		}
	})
}

func TestABrowserJobDrivesHeadlessChromeWithoutADisplayAndLeavesNothingBehind(t *testing.T) {
	if _, err := exec.LookPath("npx"); err != nil && os.Getenv("CI") == "" {
		t.Skip("npx not installed")
	}
	var mu sync.Mutex
	var session string
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if c, err := r.Cookie("session"); err == nil {
			mu.Lock()
			session = c.Value
			mu.Unlock()
		}
		w.Write([]byte(`<!doctype html><html><body><h1>Contacts</h1></body></html>`))
	}))
	t.Cleanup(app.Close)
	t.Setenv("DISPLAY", "")
	t.Setenv("WAYLAND_DISPLAY", "")

	r := Recipe{Dir: t.TempDir(), URL: app.URL, Health: app.URL + "/health", HealthStatus: http.StatusNoContent, Startup: 30 * time.Second, Seed: state.NotNeeded, SignIn: `echo '[{"name":"session","value":"s3cret"}]'`}
	job, err := (&Browsers{NoSandbox: goruntime.GOOS == "linux"}).StartJob(context.Background(), r, "", nil)
	if err != nil {
		if os.Getenv("CI") == "" {
			t.Skip("no Chrome here: ", err)
		}
		t.Fatal(err)
	}
	pid := job.Chrome.PID
	t.Cleanup(func() { job.Close() })

	mu.Lock()
	signedIn := session
	mu.Unlock()
	if signedIn != "s3cret" {
		t.Fatalf("the app saw session %q; want the cookie sign-in printed, set in the job's own profile", signedIn)
	}
	config, _ := os.ReadFile(job.MCPConfig)
	if !bytes.Contains(config, []byte(DevToolsMCP)) || !bytes.Contains(config, []byte(`"--browser-url=`+job.Chrome.URL+`"`)) {
		t.Fatalf("the MCP config does not attach the pinned server to this job's browser:\n%s", config)
	}

	alloc, cancelAlloc := remote.NewAllocator(context.Background(), job.Chrome.URL)
	defer cancelAlloc()
	tab, cancelTab := chromedp.NewContext(alloc)
	defer cancelTab()
	ctx, cancel := context.WithTimeout(tab, 20*time.Second)
	defer cancel()
	if err := chromedp.Do(ctx, chromedp.Navigate(app.URL)); err != nil {
		t.Fatalf("navigating through the job's browser URL: %v", err)
	}
	shot, err := chromedp.Call(ctx, page.CaptureScreenshot, page.CaptureScreenshotParams{Format: page.CaptureScreenshotFormatPng})
	if err != nil || !bytes.HasPrefix(shot.Data, []byte("\x89PNG")) {
		t.Fatalf("no screenshot through the job's browser URL: %v", err)
	}
	cancelTab()
	cancelAlloc()

	if err := job.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(job.Dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the job left %s behind: %v", job.Dir, err)
	}
	for deadline := time.Now().Add(5 * time.Second); syscall.Kill(pid, 0) == nil; time.Sleep(50 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("Chrome %d still runs after the job closed", pid)
		}
	}
}

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestThePinnedChromeDevToolsMCPNavigatesAndScreenshotsAJobsBrowserOverStdio(t *testing.T) {
	if os.Getenv("CI") == "" {
		t.Skip("downloads " + DevToolsMCP + " from the npm registry, so it runs only on CI")
	}
	var mu sync.Mutex
	var paths []string
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		w.Write([]byte(`<!doctype html><html><body><h1>Contacts</h1></body></html>`))
	}))
	t.Cleanup(app.Close)
	r := Recipe{Dir: t.TempDir(), URL: app.URL, Health: app.URL, HealthStatus: http.StatusOK, Startup: 30 * time.Second, Seed: state.NotNeeded, SignIn: state.NotNeeded}
	job, err := (&Browsers{NoSandbox: goruntime.GOOS == "linux"}).StartJob(context.Background(), r, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { job.Close() })
	var config struct {
		Servers map[string]struct {
			Command string
			Args    []string
		} `json:"mcpServers"`
	}
	data, _ := os.ReadFile(job.MCPConfig)
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	server := config.Servers["chrome-devtools"]

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	cmd := exec.CommandContext(ctx, server.Command, server.Args...)
	stderr := &lockedBuffer{}
	cmd.Stderr = stderr
	cmd.WaitDelay = 5 * time.Second
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stdin.Close(); cancel(); cmd.Wait() })
	lines := bufio.NewReader(stdout)
	send := func(message map[string]any) {
		message["jsonrpc"] = "2.0"
		line, _ := json.Marshal(message)
		if _, err := stdin.Write(append(line, '\n')); err != nil {
			t.Fatalf("writing to the MCP server: %v\n%s", err, stderr)
		}
	}
	id := 0
	call := func(method string, params any) json.RawMessage {
		t.Helper()
		id++
		send(map[string]any{"id": id, "method": method, "params": params})
		for {
			line, err := lines.ReadBytes('\n')
			if err != nil {
				t.Fatalf("%s got no answer: %v\n%s", method, err, stderr)
			}
			var answer struct {
				ID     *int
				Result json.RawMessage
				Error  *struct{ Message string }
			}
			if json.Unmarshal(line, &answer) != nil || answer.ID == nil || *answer.ID != id {
				continue
			}
			if answer.Error != nil {
				t.Fatalf("%s failed: %s", method, answer.Error.Message)
			}
			return answer.Result
		}
	}
	call("initialize", map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "factory-test", "version": "0"}})
	send(map[string]any{"method": "notifications/initialized"})

	type inputSchema struct {
		Properties map[string]any
		Required   []string
	}
	var listed struct {
		Tools []struct {
			Name        string
			InputSchema inputSchema
		}
	}
	if err := json.Unmarshal(call("tools/list", map[string]any{}), &listed); err != nil {
		t.Fatal(err)
	}
	schemas := map[string]inputSchema{}
	for _, tool := range listed.Tools {
		schemas[tool.Name] = tool.InputSchema
	}
	for _, name := range []string{"list_pages", "navigate_page", "take_screenshot"} {
		if _, ok := schemas[name]; !ok {
			t.Fatalf("%s lists no %s: %v", DevToolsMCP, name, slices.Collect(maps.Keys(schemas)))
		}
	}
	type content struct {
		Type, Text, Data string
	}
	type toolResult struct {
		Content           []content
		IsError           bool
		StructuredContent struct {
			Pages []struct {
				ID  *int
				URL string
			}
		}
	}
	var pageID *int
	toolCall := func(name string, values map[string]any) toolResult {
		t.Helper()
		schema := schemas[name]
		args := map[string]any{}
		for key, value := range values {
			if _, ok := schema.Properties[key]; ok {
				args[key] = value
			}
		}
		if _, ok := schema.Properties["pageId"]; ok && pageID != nil {
			args["pageId"] = *pageID
		}
		for _, key := range schema.Required {
			if _, ok := args[key]; !ok {
				t.Fatalf("%s requires an argument the test has not supplied: %s", name, key)
			}
		}
		var result toolResult
		if err := json.Unmarshal(call("tools/call", map[string]any{"name": name, "arguments": args}), &result); err != nil {
			t.Fatal(err)
		}
		if result.IsError {
			t.Fatalf("%s failed: %+v", name, result)
		}
		return result
	}
	pages := toolCall("list_pages", nil)
	for _, p := range pages.StructuredContent.Pages {
		if strings.TrimRight(p.URL, "/") == app.URL && p.ID != nil {
			pageID = p.ID
			break
		}
	}
	if pageID == nil {
		t.Fatalf("list_pages did not identify the job's app page: %+v", pages)
	}
	result := toolCall("navigate_page", map[string]any{"url": app.URL + "/export", "type": "url"})
	mu.Lock()
	navigated := slices.Contains(paths, "/export")
	mu.Unlock()
	if result.IsError || !navigated {
		t.Fatalf("navigate_page did not reach the app through the job's browser: %+v", result)
	}
	result = toolCall("take_screenshot", map[string]any{"format": "png"})
	shot := slices.IndexFunc(result.Content, func(c content) bool { return c.Type == "image" && strings.HasPrefix(c.Data, "iVBOR") })
	if result.IsError || shot < 0 {
		t.Fatalf("take_screenshot returned no PNG from the job's browser: %+v", result)
	}
}

type stageEngine struct{ typed []string }

func (e *stageEngine) NewFactoryPane(host.NewPane) (string, error) { return "%1", nil }
func (e *stageEngine) Pause(string, string) error                  { return nil }
func (e *stageEngine) NewStagePane(host.StagePane) (string, error) { return "%2", nil }
func (e *stageEngine) KillStages(string, string, string) error     { return nil }
func (e *stageEngine) Type(_, text string) error                   { e.typed = append(e.typed, text); return nil }

func TestBrowserQAStartsOnlyOnHeadlessChromeThroughAPerStageMCPConfig(t *testing.T) {
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`<!doctype html><html><body><h1>Contacts</h1></body></html>`))
	}))
	t.Cleanup(app.Close)
	_, port, _ := net.SplitHostPort(app.Listener.Addr().String())
	fakeNPX(t)
	t.Setenv("DISPLAY", "")
	t.Setenv("WAYLAND_DISPLAY", "")
	repo, stateDir := t.TempDir(), t.TempDir()
	worktree := filepath.Join(repo, "wt")
	f := factory.Factory{Repo: repo, Slug: "csv", Dir: filepath.Join(worktree, ".claude", "factory", "csv"), Worktree: worktree, State: &state.State{ID: "csv"}}
	configure := func(qa string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Join(repo, ".factory"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(state.RepoConfigPath(repo), []byte("qa:\n  mode: browser\n  web_port: "+port+"\n  health: "+app.URL+"\n"+qa), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	engine := &stageEngine{}
	queue := NewQueue()
	browsers := &Browsers{NoSandbox: goruntime.GOOS == "linux"}
	t.Cleanup(func() { browsers.Close() })
	r := host.New(host.Runtime{Config: &state.Config{StateDir: stateDir}, Engine: engine, Jobs: &Jobs{Queue: queue, Ctx: context.Background()}, Browsers: browsers})
	r.SetSnapshot(host.Snapshot{At: time.Now(), Items: []host.Item{{Factory: f}}})
	startQA := func() (host.StageReply, error) {
		return r.Stage(host.StageRequest{Role: "qa", Factory: "csv", Text: "Test the export."})
	}

	configure("  seed: none\n")
	if _, err := startQA(); err == nil || !strings.Contains(err.Error(), "qa.sign_in") || !strings.Contains(err.Error(), "no browser was launched") {
		t.Fatalf("browser QA without a declared sign-in gave %v; want a failure naming qa.sign_in before any browser", err)
	}
	if len(engine.typed) != 0 || len(queue.Events()) != 0 {
		t.Fatal("browser QA without a recipe still started a pane or a browser job")
	}

	configure("  seed: none\n  sign_in: none\n")
	if _, err := startQA(); err != nil {
		t.Fatal(err)
	}
	reply, err := startQA()
	if err != nil {
		t.Fatal(err)
	}
	var ready host.Message
	select {
	case e := <-queue.Events():
		ready = e.Message
	case <-time.After(time.Minute):
		t.Fatal("the browser job never reported back")
	}
	var b host.BrowserReady
	json.Unmarshal(ready.Data, &b)
	if b.Error != "" {
		if os.Getenv("CI") == "" {
			t.Skip("no Chrome here: ", b.Error)
		}
		t.Fatal(b.Error)
	}
	if answer := r.Handle(ready); answer.T != "done" {
		t.Fatalf("opening the QA pane on the ready browser: %+v", answer)
	}
	launch, _ := os.ReadFile(state.LaunchScriptPath(stateDir, repo, "csv", "qa"))
	config, _ := os.ReadFile(b.MCPConfig)
	job := browsers.leased[f.Key()].job
	switch {
	case !bytes.Contains(launch, []byte("--mcp-config "+factory.ShellQuote(b.MCPConfig))) || bytes.Contains(launch, []byte("--chrome")):
		t.Fatalf("QA launched with\n%s\nwant this stage's MCP config and no --chrome", launch)
	case !bytes.Contains(config, []byte("--browser-url="+job.Chrome.URL)):
		t.Fatalf("the stage's MCP config does not attach to its own headless browser:\n%s", config)
	case !strings.HasPrefix(job.Chrome.Profile, os.TempDir()):
		t.Fatalf("Chrome runs on the profile %s; want a temporary one", job.Chrome.Profile)
	}

	runs, _ := state.LoadStageRuns(state.StageRunsPath(stateDir, repo, "csv"))
	superseded, _ := json.Marshal(host.BrowserReady{Factory: f.Key(), Token: runs.Runs[0].Token, Error: "headless Chrome did not start", Failure: host.FailBrowser})
	r.Handle(host.Message{T: "browser", Data: superseded})
	if _, err := os.Stat(job.Dir); err != nil || browsers.leased[f.Key()] == nil {
		t.Fatalf("the superseded QA run's late failure closed the newer run's browser: %v", err)
	}
	if _, err := os.Stat(reply.Result); err == nil {
		t.Fatal("the superseded QA run's late failure wrote the newer run's result")
	}

	host.WriteFile(reply.Result, `{"status":"done"}`)
	r.ReleaseFinishedBrowsers()
	if _, err := os.Stat(job.Dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the finished QA stage left its browser profile %s behind: %v", job.Dir, err)
	}
}

func fakeNPX(t *testing.T) {
	t.Helper()
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "npx"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestABrowserJobOverTheNodeCapWaitsVisiblyAndStartsWhenTheBusyOneEnds(t *testing.T) {
	fakeNPX(t)
	busyAsked := make(chan struct{}, 1)
	busy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		select {
		case busyAsked <- struct{}{}:
		default:
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(busy.Close)
	healthy := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	t.Cleanup(healthy.Close)
	profile := func(health string) state.Profile {
		var p state.Profile
		p.QA.Health, p.QA.WebPort, p.QA.StartupTimeout = health, 3001, "1m"
		p.QA.Seed, p.QA.SignIn = "echo seeded > seeded", "echo not-cookies"
		return p
	}
	nodeCtx, stopNode := context.WithCancel(context.Background())
	defer stopNode()
	browsers := &Browsers{Max: 1, StateDir: t.TempDir(), Ctx: nodeCtx}

	first, cancelFirst := context.WithCancel(nodeCtx)
	firstDone := make(chan error, 1)
	go func() {
		_, err := browsers.Prepare(first, host.BrowserLease{Factory: "a", Token: "t1", Worktree: t.TempDir()}, profile(busy.URL), func(string) {})
		firstDone <- err
	}()
	<-busyAsked

	second := t.TempDir()
	waiting := make(chan string, 1)
	secondDone := make(chan error, 1)
	go func() {
		_, err := browsers.Prepare(context.Background(), host.BrowserLease{Factory: "b", Token: "t2", Worktree: second}, profile(healthy.URL), func(reason string) { waiting <- reason })
		secondDone <- err
	}()
	select {
	case reason := <-waiting:
		if !strings.Contains(reason, "all 1") {
			t.Fatalf("the waiting job said %q; want it to name the node's cap", reason)
		}
	case err := <-secondDone:
		t.Fatalf("a second job ran past a cap of 1 while the first held the slot: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("the second job neither waited visibly nor ran")
	}
	if _, err := os.Stat(filepath.Join(second, "seeded")); err == nil {
		t.Fatal("the waiting job seeded its stack before it had a browser slot")
	}

	cancelFirst()
	<-firstDone
	select {
	case err := <-secondDone:
		if err == nil || !strings.Contains(err.Error(), "qa.sign_in") {
			t.Fatalf("the second job gave %v; want it to run up to its sign-in once the slot freed", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the waiting job never started after the busy one ended")
	}
	if _, err := os.Stat(filepath.Join(second, "seeded")); err != nil {
		t.Fatalf("the second job did not run its recipe after the slot freed: %v", err)
	}

	select {
	case <-busyAsked:
	default:
	}
	thirdDone := make(chan error, 1)
	go func() {
		_, err := browsers.Prepare(nodeCtx, host.BrowserLease{Factory: "c", Token: "t3", Worktree: t.TempDir()}, profile(busy.URL), func(string) {})
		thirdDone <- err
	}()
	<-busyAsked
	stopNode()
	<-thirdDone
	recovered := browsers.Recover()
	if len(recovered) != 1 || recovered[0].Token != "t3" {
		t.Fatalf("after the node stopped mid-startup its next start recovered %+v; want only the interrupted job, so its QA run fails instead of waiting forever", recovered)
	}
}
