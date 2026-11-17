package node

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
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
	jobDirPrefix   = "factory-browser-"
)

var errNodeStopping = errors.New("the node is stopping, so the QA browser was closed")

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
	Evidence  string
	Chrome    *review.Headless
	stack     *exec.Cmd
	stackProc *process
	started   func(*BrowserJob)
}

type process struct {
	PID     int    `json:"pid"`
	Started string `json:"started"`
}

func (b *Browsers) StartJob(ctx context.Context, r Recipe, evidence string, started func(*BrowserJob)) (*BrowserJob, error) {
	dir, err := os.MkdirTemp("", jobDirPrefix)
	if err != nil {
		return nil, err
	}
	job := &BrowserJob{Dir: dir, MCPConfig: filepath.Join(dir, "mcp.json"), Evidence: evidence, started: started}
	job.journal()
	if err := job.prepare(ctx, r, b.Chrome, b.NoSandbox); err != nil {
		job.Close()
		return nil, err
	}
	return job, nil
}

func (j *BrowserJob) journal() {
	if j.started != nil {
		j.started(j)
	}
}

func failed(name string, err error) error { return host.BrowserFailure{Name: name, Err: err} }

func (j *BrowserJob) prepare(ctx context.Context, r Recipe, chrome string, noSandbox bool) error {
	if _, err := exec.LookPath("npx"); err != nil {
		return failed(host.FailTools, errors.New("npx is not installed on this node, so Chrome DevTools MCP cannot run; install Node.js"))
	}
	if err := j.startStack(r); err != nil {
		return failed(host.FailStack, err)
	}
	ready, cancel := context.WithTimeout(ctx, r.Startup)
	defer cancel()
	if err := host.WaitFor(ready, r.Health, func(status int) bool { return status == r.HealthStatus }); err != nil {
		return failed(host.FailHealth, fmt.Errorf("the stack did not answer %s with %d within %s: %w", r.Health, r.HealthStatus, r.Startup, err))
	}
	if _, err := step(ctx, "qa.seed", r.Seed, r.Dir); err != nil {
		return failed(host.FailSeed, err)
	}
	cookies, err := signIn(ctx, r)
	if err != nil {
		return failed(host.FailSignIn, err)
	}
	if j.Chrome, err = review.LaunchHeadless(filepath.Join(j.Dir, "profile"), chrome, noSandbox); err != nil {
		return failed(host.FailBrowser, err)
	}
	j.journal()
	if j.Evidence != "" {
		if err := j.Chrome.Watch(j.Evidence); err != nil {
			return failed(host.FailBrowser, fmt.Errorf("the browser did not report its console and network: %w", err))
		}
	}
	if err := j.Chrome.SetCookies(r.URL, cookies); err != nil {
		return failed(host.FailSignIn, fmt.Errorf("the browser refused the sign-in cookies: %w", err))
	}
	loading, cancelLoad := context.WithTimeout(ctx, r.Startup)
	defer cancelLoad()
	if err := j.Chrome.Navigate(loading, r.URL); err != nil {
		return failed(host.FailBrowser, fmt.Errorf("the browser could not open %s: %w", r.URL, err))
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
	j.stackProc = processOf(j.stack.Process.Pid)
	j.journal()
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
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Cancel = func() error { return killGroup(cmd.Process.Pid) }
	cmd.WaitDelay = time.Second
	return cmd
}

func killGroup(pgid int) error { return syscall.Kill(-pgid, syscall.SIGKILL) }

func signIn(ctx context.Context, r Recipe) ([]review.Cookie, error) {
	out, err := step(ctx, "qa.sign_in", r.SignIn, r.Dir)
	if err != nil {
		return nil, fmt.Errorf("%w. qa.sign_in must finish unattended: nobody can answer a captcha, a 2FA prompt or a password prompt on a node", err)
	}
	if len(bytes.TrimSpace(out)) == 0 {
		return nil, nil
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
		killGroup(j.stack.Process.Pid)
		j.stack.Wait()
	}
	return errors.Join(append(errs, os.RemoveAll(j.Dir))...)
}

type Browsers struct {
	Chrome    string
	NoSandbox bool
	Max       int
	StateDir  string
	Ctx       context.Context

	mu       sync.Mutex
	leased   map[string]*leasedJob
	slots    chan struct{}
	stopping bool
}

type leasedJob struct {
	token   string
	job     *BrowserJob
	release func()
}

type jobRecord struct {
	host.BrowserLease
	Dir    string   `json:"dir,omitempty"`
	Stack  *process `json:"stack,omitempty"`
	Chrome *process `json:"chrome,omitempty"`
}

func (b *Browsers) Check(profile state.Profile, worktree string) error {
	_, err := RecipeOf(profile, worktree)
	return err
}

func (b *Browsers) Prepare(ctx context.Context, lease host.BrowserLease, profile state.Profile, waiting func(string)) (string, error) {
	recipe, err := RecipeOf(profile, lease.Worktree)
	if err != nil {
		return "", failed(host.FailRecipe, err)
	}
	record := jobRecord{BrowserLease: lease}
	if err := b.journal(record); err != nil {
		return "", err
	}
	release, err := b.acquire(ctx, waiting)
	if err != nil {
		b.forget(lease.Token)
		return "", err
	}
	job, err := b.StartJob(ctx, recipe, lease.Evidence, func(j *BrowserJob) { b.journal(record.of(j)) })
	if err == nil && !b.lease(ctx, lease, job, release) {
		job.Close()
		err = cmp.Or(ctx.Err(), errNodeStopping)
	}
	if err != nil {
		release()
		b.forget(lease.Token)
		return "", err
	}
	return job.MCPConfig, nil
}

func (r jobRecord) of(j *BrowserJob) jobRecord {
	r.Dir, r.Stack = j.Dir, j.stackProc
	if j.Chrome != nil {
		r.Chrome = processOf(j.Chrome.PID)
	}
	return r
}

func (b *Browsers) acquire(ctx context.Context, waiting func(string)) (func(), error) {
	b.mu.Lock()
	if b.slots == nil {
		b.slots = make(chan struct{}, max(b.Max, 1))
	}
	slots := b.slots
	b.mu.Unlock()
	select {
	case slots <- struct{}{}:
	default:
		waiting(fmt.Sprintf("all %d this node runs at once are in use; it starts when one finishes", cap(slots)))
		select {
		case slots <- struct{}{}:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	var once sync.Once
	return func() { once.Do(func() { <-slots }) }, nil
}

func (b *Browsers) lease(ctx context.Context, lease host.BrowserLease, job *BrowserJob, release func()) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if ctx.Err() != nil || b.nodeStopping() {
		return false
	}
	if b.leased == nil {
		b.leased = map[string]*leasedJob{}
	}
	b.leased[lease.Factory] = &leasedJob{token: lease.Token, job: job, release: release}
	return true
}

func (b *Browsers) Release(key, token string) {
	b.mu.Lock()
	held := b.leased[key]
	if held == nil || held.token != token {
		b.mu.Unlock()
		return
	}
	delete(b.leased, key)
	b.mu.Unlock()
	held.job.Close()
	held.release()
	b.forget(token)
}

func (b *Browsers) Lost(key, token string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	held := b.leased[key]
	return held != nil && held.token == token && held.job.Chrome.Lost()
}

func (b *Browsers) Close() error {
	b.mu.Lock()
	b.stopping = true
	held := slices.Collect(maps.Values(b.leased))
	b.leased = nil
	b.mu.Unlock()
	for _, h := range held {
		h.job.Close()
		h.release()
	}
	return nil
}

func (b *Browsers) journalDir() string { return filepath.Join(b.StateDir, "browser-jobs") }

func (b *Browsers) journal(r jobRecord) error {
	if b.StateDir == "" {
		return nil
	}
	if err := os.MkdirAll(b.journalDir(), 0o700); err != nil {
		return err
	}
	data, _ := json.Marshal(r)
	path := filepath.Join(b.journalDir(), r.Token+".json")
	if err := os.WriteFile(path+".tmp", data, 0o600); err != nil {
		return err
	}
	return os.Rename(path+".tmp", path)
}

func (b *Browsers) forget(token string) {
	b.mu.Lock()
	stopping := b.nodeStopping()
	b.mu.Unlock()
	if b.StateDir != "" && !stopping {
		os.Remove(filepath.Join(b.journalDir(), token+".json"))
	}
}

func (b *Browsers) nodeStopping() bool {
	return b.stopping || (b.Ctx != nil && b.Ctx.Err() != nil)
}

func (b *Browsers) Recover() []host.BrowserLease {
	paths, _ := filepath.Glob(filepath.Join(b.journalDir(), "*.json"))
	var leases []host.BrowserLease
	for _, path := range paths {
		var r jobRecord
		if data, err := os.ReadFile(path); err == nil && json.Unmarshal(data, &r) == nil {
			r.stop()
			leases = append(leases, r.BrowserLease)
		}
		os.Remove(path)
	}
	return leases
}

func (r jobRecord) stop() {
	if r.Stack != nil && r.Stack.ours() {
		killGroup(r.Stack.PID)
	}
	if !strings.HasPrefix(filepath.Base(r.Dir), jobDirPrefix) {
		return
	}
	profile := filepath.Join(r.Dir, "profile")
	for range 20 {
		if killUsing(profile) == 0 && os.RemoveAll(r.Dir) == nil {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func processOf(pid int) *process {
	started, alive := startTime(pid)
	if !alive {
		return nil
	}
	return &process{PID: pid, Started: started}
}

func startTime(pid int) (string, bool) {
	out, err := exec.Command("ps", "-o", "lstart=", "-p", strconv.Itoa(pid)).Output()
	started := strings.TrimSpace(string(out))
	return started, err == nil && started != ""
}

func (p process) ours() bool {
	started, alive := startTime(p.PID)
	return !alive || started == p.Started
}

func killUsing(profile string) int {
	out, err := exec.Command("ps", "-axww", "-o", "pid=,command=").Output()
	if err != nil {
		return 0
	}
	arg := "--user-data-dir=" + profile + " "
	killed := 0
	for line := range strings.SplitSeq(string(out), "\n") {
		pid, command, _ := strings.Cut(strings.TrimSpace(line), " ")
		n, err := strconv.Atoi(pid)
		if err == nil && strings.Contains(command+" ", arg) && syscall.Kill(n, syscall.SIGKILL) == nil {
			killed++
		}
	}
	return killed
}
