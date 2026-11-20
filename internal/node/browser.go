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
	"net/url"
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
	"github.com/afonsodelgado/factory/internal/recipe"
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
	Env          []string
	portable     bool
	webPort      int
	apiPort      int
}

const (
	WebPortEnv      = "FACTORY_WEB_PORT"
	APIPortEnv      = "FACTORY_API_PORT"
	PublicOriginEnv = "FACTORY_PUBLIC_ORIGIN"
)

func RecipeOf(profile state.Profile, worktree string) (Recipe, error) {
	if profile.QA.Start == "" {
		resolved, err := recipe.Resolve(worktree)
		if err != nil {
			return Recipe{}, err
		}
		profile.QA.Start = resolved.Profile.QA.Start
	}
	qa := profile.QA
	r := Recipe{Dir: worktree, URL: qa.URL, Start: qa.Start, Health: qa.Health, HealthStatus: qa.HealthStatus, Startup: defaultStartup, Seed: qa.Seed, SignIn: qa.SignIn, portable: qa.URL != "", webPort: qa.WebPort, apiPort: qa.APIPort}
	if r.HealthStatus == 0 {
		r.HealthStatus = http.StatusOK
	}
	var problems []error
	undeclared := func(key, what string) {
		problems = append(problems, fmt.Errorf("qa.%s is not declared in the repo config; write %s", key, what))
	}
	if r.Health == "" {
		undeclared("health", "the URL that answers once the stack is ready")
	}
	if !r.portable && qa.WebPort == 0 {
		undeclared("web_port", "the port the app's web server listens on, or qa.url with ${"+WebPortEnv+"} so the node picks a free one")
	}
	if !r.portable {
		r.URL = "http://localhost:" + strconv.Itoa(qa.WebPort)
	}
	if _, err := upstreamOf(expand(r.URL, map[string]string{WebPortEnv: "1", APIPortEnv: "1"})); err != nil {
		problems = append(problems, err)
	}
	if qa.StartupTimeout != "" {
		startup, err := time.ParseDuration(qa.StartupTimeout)
		if err != nil || startup <= 0 {
			problems = append(problems, fmt.Errorf("qa.startup_timeout %q is not a duration; write it with a unit, such as 2m", qa.StartupTimeout))
		}
		r.Startup = startup
	}
	return r, errors.Join(problems...)
}

func (r Recipe) Bind(publicOrigin string) (Recipe, error) {
	values := map[string]string{WebPortEnv: strconv.Itoa(r.webPort), APIPortEnv: strconv.Itoa(r.apiPort)}
	if r.portable {
		for _, name := range []string{WebPortEnv, APIPortEnv} {
			port, err := FreePort()
			if err != nil {
				return r, failed(host.FailPorts, fmt.Errorf("no free loopback port for %s: %w", name, err))
			}
			values[name] = strconv.Itoa(port)
		}
	}
	r.URL, r.Health = expand(r.URL, values), expand(r.Health, values)
	upstream, err := upstreamOf(r.URL)
	if err != nil {
		return r, failed(host.FailRecipe, err)
	}
	values[PublicOriginEnv] = cmp.Or(publicOrigin, upstream.String())
	for _, name := range []string{WebPortEnv, APIPortEnv, PublicOriginEnv} {
		r.Env = append(r.Env, name+"="+values[name])
	}
	return r, nil
}

func expand(s string, values map[string]string) string {
	return os.Expand(s, func(name string) string {
		if value, ok := values[name]; ok {
			return value
		}
		return "${" + name + "}"
	})
}

func upstreamOf(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	loopback := err == nil && slices.Contains([]string{"localhost", "127.0.0.1", "::1"}, u.Hostname())
	if !loopback || u.Scheme != "http" || u.Port() == "" {
		return nil, fmt.Errorf("qa.url %q must be an http URL on localhost with a port, such as http://localhost:${%s}/", raw, WebPortEnv)
	}
	return &url.URL{Scheme: u.Scheme, Host: u.Host}, nil
}

type BrowserJob struct {
	Dir       string
	MCPConfig string
	Evidence  string
	Chrome    *review.Headless
	stack     *stackProcess
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
	if _, err := bringUp(ctx, r, func(p *stackProcess) { j.stack = p; j.journal() }); err != nil {
		j.stack = nil
		return err
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

type stackProcess struct {
	cmd    *exec.Cmd
	proc   *process
	exited chan struct{}
	err    error
}

func bringUp(ctx context.Context, r Recipe, started func(*stackProcess)) (*stackProcess, error) {
	p, err := startProcess(r)
	if err != nil {
		return nil, failed(host.FailStack, err)
	}
	if p != nil {
		started(p)
	}
	ready, cancel := context.WithTimeout(ctx, r.Startup)
	defer cancel()
	healthy := p.whileRunning(ready, func(c context.Context) error {
		return host.WaitFor(c, r.Health, func(status int) bool { return status == r.HealthStatus })
	})
	switch {
	case p.crashed():
		p.stop()
		return nil, failed(host.FailStack, fmt.Errorf("qa.start failed before %s answered: %w", r.Health, p.err))
	case healthy != nil:
		p.stop()
		return nil, failed(host.FailHealth, fmt.Errorf("the stack did not answer %s with %d within %s: %w", r.Health, r.HealthStatus, r.Startup, healthy))
	}
	if _, err := step(ctx, "qa.seed", r.Seed, r); err != nil {
		p.stop()
		return nil, failed(host.FailSeed, err)
	}
	return p, nil
}

func startProcess(r Recipe) (*stackProcess, error) {
	if r.Start == "" {
		return nil, nil
	}
	p := &stackProcess{cmd: r.shell(context.Background(), r.Start), exited: make(chan struct{})}
	if err := p.cmd.Start(); err != nil {
		return nil, fmt.Errorf("qa.start did not run: %w", err)
	}
	p.proc = processOf(p.cmd.Process.Pid)
	go func() {
		p.err = p.cmd.Wait()
		close(p.exited)
	}()
	return p, nil
}

func (p *stackProcess) whileRunning(ctx context.Context, work func(context.Context) error) error {
	if p == nil {
		return work(ctx)
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		select {
		case <-p.exited:
			if p.err != nil {
				cancel()
			}
		case <-ctx.Done():
		}
	}()
	return work(ctx)
}

func (p *stackProcess) crashed() bool {
	if p == nil {
		return false
	}
	select {
	case <-p.exited:
		return p.err != nil
	default:
		return false
	}
}

func (p *stackProcess) stop() {
	if p == nil {
		return
	}
	if p.proc == nil || p.proc.ours() {
		killGroup(p.cmd.Process.Pid)
	}
	<-p.exited
}

func step(ctx context.Context, name, command string, r Recipe) ([]byte, error) {
	if state.Unused(command) {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(ctx, stepWait)
	defer cancel()
	cmd := r.shell(ctx, command)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("%s failed: %w: %s", name, err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

func (r Recipe) shell(ctx context.Context, command string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", command)
	cmd.Dir = r.Dir
	cmd.Env = append(os.Environ(), r.Env...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Cancel = func() error { return killGroup(cmd.Process.Pid) }
	cmd.WaitDelay = time.Second
	return cmd
}

func killGroup(pgid int) error { return syscall.Kill(-pgid, syscall.SIGKILL) }

func signIn(ctx context.Context, r Recipe) ([]review.Cookie, error) {
	out, err := step(ctx, "qa.sign_in", r.SignIn, r)
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
	j.stack.stop()
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
	if recipe, err = recipe.Bind(""); err != nil {
		return "", err
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
	r.Dir = j.Dir
	if j.stack != nil {
		r.Stack = j.stack.proc
	}
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
		if review.KillUsing(profile) == 0 && os.RemoveAll(r.Dir) == nil {
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
