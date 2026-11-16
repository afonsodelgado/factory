package node

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
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
		job, err := StartBrowserJob(context.Background(), recipe(dir), "", false)
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
		job, err := StartBrowserJob(context.Background(), recipe(dir), "", false)
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
	job, err := StartBrowserJob(context.Background(), r, "", goruntime.GOOS == "linux")
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
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "npx"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
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
	job := browsers.leased[f.Key()]
	switch {
	case !bytes.Contains(launch, []byte("--mcp-config "+factory.ShellQuote(b.MCPConfig))) || bytes.Contains(launch, []byte("--chrome")):
		t.Fatalf("QA launched with\n%s\nwant this stage's MCP config and no --chrome", launch)
	case !bytes.Contains(config, []byte("--browser-url="+job.Chrome.URL)):
		t.Fatalf("the stage's MCP config does not attach to its own headless browser:\n%s", config)
	case !strings.HasPrefix(job.Chrome.Profile, os.TempDir()):
		t.Fatalf("Chrome runs on the profile %s; want a temporary one", job.Chrome.Profile)
	}

	host.WriteFile(reply.Result, `{"status":"done"}`)
	r.ReleaseFinishedBrowsers()
	if _, err := os.Stat(job.Dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the finished QA stage left its browser profile %s behind: %v", job.Dir, err)
	}
}
