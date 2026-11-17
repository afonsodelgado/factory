package node

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/afonsodelgado/factory/internal/host"
	"github.com/afonsodelgado/factory/internal/review"
	"github.com/afonsodelgado/factory/internal/state"
)

const (
	DevToolsMCP    = "chrome-devtools-mcp@1.13.0"
	stepWait       = 5 * time.Minute
	defaultStartup = 2 * time.Minute
)

type Recipe struct {
	Dir          string
	URL          string
	Start        string
	Health       string
	HealthStatus int
	Startup      time.Duration
	Seed         string
	SignIn       string
}

func RecipeOf(profile state.Profile, worktree string) (Recipe, error) {
	qa := profile.QA
	r := Recipe{Dir: worktree, Start: qa.Start, Health: qa.Health, HealthStatus: qa.HealthStatus, Startup: defaultStartup, Seed: qa.Seed, SignIn: qa.SignIn}
	if r.HealthStatus == 0 {
		r.HealthStatus = http.StatusOK
	}
	var problems []error
	undeclared := func(key, what string) {
		problems = append(problems, fmt.Errorf("qa.%s is not declared in the repo config; write %s", key, what))
	}
	if r.Seed == "" {
		undeclared("seed", "the command that seeds an isolated test environment, or "+state.NotNeeded)
	}
	if r.SignIn == "" {
		undeclared("sign_in", "the command that signs in and prints the session cookies as JSON, or "+state.NotNeeded)
	}
	if r.Health == "" {
		undeclared("health", "the URL that answers once the stack is ready")
	}
	if qa.WebPort == 0 {
		undeclared("web_port", "the port the app's web server listens on")
	}
	r.URL = "http://localhost:" + strconv.Itoa(qa.WebPort)
	if qa.StartupTimeout != "" {
		startup, err := time.ParseDuration(qa.StartupTimeout)
		if err != nil || startup <= 0 {
			problems = append(problems, fmt.Errorf("qa.startup_timeout %q is not a duration; write it with a unit, such as 2m", qa.StartupTimeout))
		}
		r.Startup = startup
	}
	return r, errors.Join(problems...)
}

type BrowserJob struct {
	Dir       string
	MCPConfig string
	Chrome    *review.Headless
	stack     *exec.Cmd
}

func StartBrowserJob(ctx context.Context, r Recipe, chrome string, noSandbox bool) (*BrowserJob, error) {
	dir, err := os.MkdirTemp("", "factory-browser-")
	if err != nil {
		return nil, err
	}
	job := &BrowserJob{Dir: dir, MCPConfig: filepath.Join(dir, "mcp.json")}
	if err := job.prepare(ctx, r, chrome, noSandbox); err != nil {
		job.Close()
		return nil, err
	}
	return job, nil
}

func (j *BrowserJob) prepare(ctx context.Context, r Recipe, chrome string, noSandbox bool) error {
	if _, err := exec.LookPath("npx"); err != nil {
		return errors.New("npx is not installed on this node, so Chrome DevTools MCP cannot run; install Node.js")
	}
	if err := j.startStack(r); err != nil {
		return err
	}
	ready, cancel := context.WithTimeout(ctx, r.Startup)
	defer cancel()
	if err := host.WaitFor(ready, r.Health, func(status int) bool { return status == r.HealthStatus }); err != nil {
		return fmt.Errorf("the stack did not answer %s with %d within %s: %w", r.Health, r.HealthStatus, r.Startup, err)
	}
	if _, err := step(ctx, "qa.seed", r.Seed, r.Dir); err != nil {
		return err
	}
	cookies, err := signIn(ctx, r)
	if err != nil {
		return err
	}
	if j.Chrome, err = review.LaunchHeadless(filepath.Join(j.Dir, "profile"), chrome, noSandbox); err != nil {
		return err
	}
	if err := j.Chrome.SetCookies(r.URL, cookies); err != nil {
		return fmt.Errorf("the browser refused the sign-in cookies: %w", err)
	}
	loading, cancelLoad := context.WithTimeout(ctx, r.Startup)
	defer cancelLoad()
	if err := j.Chrome.Navigate(loading, r.URL); err != nil {
		return fmt.Errorf("the browser could not open %s: %w", r.URL, err)
	}
	return os.WriteFile(j.MCPConfig, MCPConfig(j.Chrome.URL), 0o600)
}

func (j *BrowserJob) startStack(r Recipe) error {
	if r.Start == "" {
		return nil
	}
	j.stack = shell(context.Background(), r.Start, r.Dir)
	if err := j.stack.Start(); err != nil {
		return fmt.Errorf("qa.start did not run: %w", err)
	}
	return nil
}

func step(ctx context.Context, name, command, dir string) ([]byte, error) {
	if command == state.NotNeeded {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(ctx, stepWait)
	defer cancel()
	cmd := shell(ctx, command, dir)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("%s failed: %w: %s", name, err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

func shell(ctx context.Context, command, dir string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", command)
	cmd.Dir = dir
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return killGroup(cmd) }
	cmd.WaitDelay = time.Second
	return cmd
}

func killGroup(cmd *exec.Cmd) error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }

func signIn(ctx context.Context, r Recipe) ([]review.Cookie, error) {
	out, err := step(ctx, "qa.sign_in", r.SignIn, r.Dir)
	if err != nil || len(bytes.TrimSpace(out)) == 0 {
		return nil, err
	}
	var cookies []review.Cookie
	if err := json.Unmarshal(out, &cookies); err != nil {
		return nil, fmt.Errorf("qa.sign_in printed something other than a JSON list of cookies: %w", err)
	}
	return cookies, nil
}

func MCPConfig(browserURL string) []byte {
	server := map[string]any{
		"command": "npx",
		"args":    []string{"-y", DevToolsMCP, "--browser-url=" + browserURL, "--no-usage-statistics", "--no-performance-crux"},
	}
	data, _ := json.MarshalIndent(map[string]any{"mcpServers": map[string]any{"chrome-devtools": server}}, "", "  ")
	return data
}

func (j *BrowserJob) Close() error {
	var errs []error
	if j.Chrome != nil {
		errs = append(errs, j.Chrome.Close())
	}
	if j.stack != nil && j.stack.Process != nil {
		killGroup(j.stack)
		j.stack.Wait()
	}
	return errors.Join(append(errs, os.RemoveAll(j.Dir))...)
}
