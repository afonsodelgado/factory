package review

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/remote"
)

const (
	bindingName  = "__factoryReview"
	consoleLimit = 40
	callTimeout  = 15 * time.Second
	shotPadding  = 24
)

type Browser struct {
	server    *Server
	client    *http.Client
	profile   string
	exec      string
	noSandbox bool

	mu     sync.Mutex
	root   context.Context
	cancel context.CancelFunc
	tabs   []*Tab
}

type Tab struct {
	ctx     context.Context
	cancel  context.CancelFunc
	factory Factory

	log     pageLog
	mu      sync.Mutex
	console []string
}

type pageLog struct {
	mu       sync.Mutex
	requests map[network.RequestID]string
}

func (s *Server) NewBrowser(socket, profile, exec string) *Browser {
	b := &Browser{server: s, client: SocketClient(socket), profile: profile, exec: exec}
	s.mu.Lock()
	s.browser = b
	s.mu.Unlock()
	return b
}

func (b *Browser) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.cancel != nil {
		b.cancel()
		b.cancel, b.root, b.tabs = nil, nil, nil
	}
}

func (b *Browser) CloseFactory(key string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.tabs = slices.DeleteFunc(b.tabs, func(t *Tab) bool {
		if t.factory.Key != key {
			return false
		}
		t.cancel()
		return true
	})
}

func (b *Browser) start() (context.Context, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.root != nil && b.root.Err() == nil {
		return b.root, nil
	}
	if err := os.MkdirAll(b.profile, 0o700); err != nil {
		return nil, err
	}
	opts := append(chromedp.DefaultExecAllocatorOptions[:], chromedp.UserDataDir(b.profile), chromedp.Flag("disable-popup-blocking", false))
	if b.exec != "" {
		opts = append(opts, chromedp.ExecPath(b.exec))
	}
	if b.noSandbox {
		opts = append(opts, chromedp.NoSandbox)
	}
	alloc, cancelAlloc := chromedp.NewExecAllocator(context.Background(), opts...)
	root, cancelRoot := chromedp.NewContext(alloc)
	if err := startLocked(root); err != nil {
		cancelRoot()
		cancelAlloc()
		return nil, fmt.Errorf("starting Chrome: %w", err)
	}
	b.root = root
	b.cancel = func() {
		cancelRoot()
		cancelAlloc()
	}
	b.tabs = nil
	return root, nil
}

const (
	launchAttempts = 2
	evidenceWait   = 5 * time.Second
	ConsoleLog     = "console.log"
	FinalShot      = "screenshot.png"
)

type Headless struct {
	Profile string
	URL     string
	PID     int
	Exec    string

	ctx      context.Context
	cancel   context.CancelFunc
	closed   atomic.Bool
	log      pageLog
	evidence string
}

func ChromeBinary(configured string) (string, bool) {
	if configured != "" {
		_, err := os.Stat(configured)
		return configured, err == nil
	}
	for _, app := range []string{"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome", "/Applications/Chromium.app/Contents/MacOS/Chromium"} {
		if _, err := os.Stat(app); err == nil && goruntime.GOOS == "darwin" {
			return app, true
		}
	}
	for _, binary := range []string{"google-chrome", "google-chrome-stable", "chromium", "chromium-browser", "chrome", "headless_shell"} {
		if path, err := exec.LookPath(binary); err == nil {
			return path, true
		}
	}
	return "", false
}

func headlessFlags(goos string, noSandbox bool) map[string]any {
	flags := map[string]any{
		"headless": true, "hide-scrollbars": true, "mute-audio": true, "no-first-run": true, "no-default-browser-check": true,
		"disable-background-networking": true, "disable-background-timer-throttling": true, "disable-backgrounding-occluded-windows": true,
		"disable-renderer-backgrounding": true, "disable-breakpad": true, "disable-client-side-phishing-detection": true,
		"disable-component-update": true, "disable-default-apps": true, "disable-extensions": true, "disable-sync": true,
		"disable-features": "Translate", "disable-hang-monitor": true, "disable-popup-blocking": true, "disable-prompt-on-repost": true,
		"metrics-recording-only": true, "force-color-profile": "srgb", "enable-automation": true,
	}
	switch goos {
	case "darwin":
		flags["use-mock-keychain"] = true
	case "linux":
		flags["password-store"] = "basic"
		flags["disable-dev-shm-usage"] = true
	}
	if noSandbox {
		flags["no-sandbox"] = true
	}
	return flags
}

func launchOptions(goos, profile, exec string, noSandbox bool) []chromedp.ExecAllocatorOption {
	opts := []chromedp.ExecAllocatorOption{chromedp.UserDataDir(profile), remote.WebSocket}
	if exec != "" {
		opts = append(opts, chromedp.ExecPath(exec))
	}
	flags := headlessFlags(goos, noSandbox)
	for _, name := range slices.Sorted(maps.Keys(flags)) {
		opts = append(opts, chromedp.Flag(name, flags[name]))
	}
	return opts
}

func LaunchHeadless(profile, exec string, noSandbox bool) (*Headless, error) {
	if exec == "" {
		exec, _ = ChromeBinary("")
	}
	var errs []error
	for range launchAttempts {
		h, err := launchHeadless(profile, exec, noSandbox)
		if err == nil {
			return h, nil
		}
		errs = append(errs, err)
		os.RemoveAll(profile)
	}
	return nil, fmt.Errorf("headless Chrome %s did not start in %d tries: %w", exec, launchAttempts, errors.Join(errs...))
}

func launchHeadless(profile, exec string, noSandbox bool) (*Headless, error) {
	alloc, cancelAlloc := chromedp.NewExecAllocator(context.Background(), launchOptions(goruntime.GOOS, profile, exec, noSandbox)...)
	ctx, cancelCtx := chromedp.NewContext(alloc)
	h := &Headless{Profile: profile, Exec: exec, ctx: ctx, cancel: func() { cancelCtx(); cancelAlloc() }}
	if err := startLocked(ctx); err != nil {
		h.cancel()
		_, portErr := devToolsPort(profile)
		return nil, errors.Join(err, portErr)
	}
	h.PID = chromedp.FromContext(ctx).Browser.Process().Pid
	port, err := devToolsPort(profile)
	if err != nil {
		h.cancel()
		return nil, err
	}
	h.URL = "http://127.0.0.1:" + port
	return h, nil
}

func startLocked(ctx context.Context) error {
	if lock, err := os.OpenFile(filepath.Join(os.TempDir(), "factory-chrome-launch.lock"), os.O_CREATE|os.O_RDWR, 0o600); err == nil {
		defer lock.Close()
		syscall.Flock(int(lock.Fd()), syscall.LOCK_EX)
	}
	return chromedp.Do(ctx)
}

func devToolsPort(profile string) (string, error) {
	data, err := os.ReadFile(filepath.Join(profile, "DevToolsActivePort"))
	if err != nil {
		return "", fmt.Errorf("headless Chrome did not report its debugging port: %w", err)
	}
	port, _, _ := strings.Cut(string(data), "\n")
	if _, err := strconv.Atoi(port); err != nil {
		return "", fmt.Errorf("headless Chrome reported the debugging port %q", port)
	}
	return port, nil
}

func (h *Headless) Watch(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	h.evidence = dir
	if err := chromedp.Listen(h.ctx, h.record); err != nil {
		return err
	}
	return chromedp.Do(h.ctx, call(runtime.Enable, cdp.Empty{}), call(network.Enable, network.EnableParams{}))
}

func (h *Headless) record(ev any) {
	line, ok := h.log.line(ev)
	if !ok {
		return
	}
	f, err := os.OpenFile(filepath.Join(h.evidence, ConsoleLog), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	f.WriteString(time.Now().Format(time.RFC3339) + " " + line + "\n")
}

func (h *Headless) Navigate(ctx context.Context, url string) error {
	run, cancel := context.WithCancel(h.ctx)
	defer cancel()
	defer context.AfterFunc(ctx, cancel)()
	return chromedp.Do(run, chromedp.Navigate(url))
}

func (h *Headless) Screenshot(ctx context.Context) ([]byte, error) {
	run, cancel := context.WithCancel(h.ctx)
	defer cancel()
	defer context.AfterFunc(ctx, cancel)()
	shot, err := chromedp.Call(run, page.CaptureScreenshot, page.CaptureScreenshotParams{Format: page.CaptureScreenshotFormatPng})
	if err != nil {
		return nil, err
	}
	return shot.Data, nil
}

func (h *Headless) SetCookies(url string, cookies []Cookie) error {
	steps := make([]chromedp.Action[chromedp.Void], 0, len(cookies))
	for _, c := range cookies {
		steps = append(steps, call(network.SetCookie, network.SetCookieParams{Name: c.Name, Value: c.Value, URL: url, Domain: c.Domain, Path: c.Path}))
	}
	return chromedp.Do(h.ctx, steps...)
}

func (h *Headless) Lost() bool {
	return !h.closed.Load() && h.ctx.Err() != nil
}

func (h *Headless) Close() error {
	if h.evidence != "" && !h.Lost() {
		h.keepFinalScreenshot()
	}
	h.closed.Store(true)
	h.cancel()
	var err error
	for range 50 {
		if err = os.RemoveAll(h.Profile); err == nil {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return err
}

func (h *Headless) keepFinalScreenshot() {
	ctx, cancel := context.WithTimeout(context.Background(), evidenceWait)
	defer cancel()
	if png, err := h.Screenshot(ctx); err == nil {
		os.WriteFile(filepath.Join(h.evidence, FinalShot), png, 0o644)
	}
}

func (b *Browser) OpenApp(url string, f Factory, page string, cookies []Cookie) (*Tab, error) {
	if !Live(page) {
		return nil, fmt.Errorf("%q is not a live page; use %s or %s", page, App, Prototype)
	}
	return b.open(url, f, page, cookies)
}

func SocketClient(socket string) *http.Client {
	dial := func(ctx context.Context, _, _ string) (net.Conn, error) {
		var dialer net.Dialer
		return dialer.DialContext(ctx, "unix", socket)
	}
	return &http.Client{Transport: &http.Transport{DialContext: dial}, Timeout: callTimeout}
}

type Cookie struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Domain string `json:"domain,omitempty"`
	Path   string `json:"path,omitempty"`
}

func (b *Browser) open(url string, f Factory, livePage string, cookies []Cookie) (*Tab, error) {
	root, err := b.start()
	if err != nil {
		return nil, err
	}
	ctx, cancel := chromedp.NewContext(root, chromedp.WithNewWindow(false))
	tab := &Tab{ctx: ctx, cancel: cancel, factory: f}
	if err := chromedp.Listen(ctx, tab.listener(b)); err != nil {
		cancel()
		return nil, err
	}
	steps := []chromedp.Action[chromedp.Void]{
		call(runtime.Enable, cdp.Empty{}),
		call(network.Enable, network.EnableParams{}),
		call(runtime.AddBinding, runtime.AddBindingParams{Name: bindingName}),
		call(page.AddScriptToEvaluateOnNewDocument, page.AddScriptToEvaluateOnNewDocumentParams{Source: appPrelude(b.server.token, f, livePage) + string(b.server.overlay)}),
	}
	for _, c := range cookies {
		steps = append(steps, call(network.SetCookie, network.SetCookieParams{Name: c.Name, Value: c.Value, URL: url, Domain: c.Domain, Path: c.Path}))
	}
	steps = append(steps, call(page.Navigate, page.NavigateParams{URL: url}), call(page.BringToFront, cdp.Empty{}))
	runCtx, done := context.WithTimeout(ctx, callTimeout)
	defer done()
	if err := chromedp.Do(runCtx, steps...); err != nil {
		cancel()
		return nil, fmt.Errorf("opening %s in Chrome: %w", url, err)
	}
	b.mu.Lock()
	b.tabs = append(b.tabs, tab)
	b.mu.Unlock()
	return tab, nil
}

func call[P, R any](cmd cdp.Command[P, R], params P) chromedp.Action[chromedp.Void] {
	return chromedp.Func(func(ctx context.Context, t *chromedp.Target) error {
		_, err := cdp.Call(ctx, t, cmd, params)
		return err
	})
}

func appPrelude(token string, f Factory, page string) string {
	config := map[string]string{
		"api":  "/r/" + token + "/" + f.Key + "/_api",
		"page": f.Key + "/" + page,
		"file": page,
	}
	data, _ := json.Marshal(config)
	return "window.__factoryReviewConfig = " + string(data) + ";\n"
}

func (t *Tab) listener(b *Browser) func(ev any) {
	return func(ev any) {
		if e, ok := ev.(*runtime.EventBindingCalled); ok && e.Name == bindingName {
			go b.handleCall(t, e.Payload)
			return
		}
		if line, ok := t.log.line(ev); ok {
			t.note(line)
		}
	}
}

func (l *pageLog) line(ev any) (string, bool) {
	switch e := ev.(type) {
	case *runtime.EventConsoleAPICalled:
		if e.Type == runtime.ConsoleAPICalledTypeError {
			return "console.error: " + consoleText(e.Args), true
		}
	case *runtime.EventExceptionThrown:
		if d := e.ExceptionDetails; d != nil {
			text := d.Text
			if d.Exception != nil && d.Exception.Description != "" {
				text = firstLine(d.Exception.Description)
			}
			return "uncaught: " + text, true
		}
	case *network.EventRequestWillBeSent:
		if e.Request != nil {
			l.remember(e.RequestID, e.Request.Method+" "+withoutQuery(e.Request.URL))
		}
	case *network.EventResponseReceived:
		if e.Response != nil && e.Response.Status >= 400 {
			return fmt.Sprintf("%s → %d", l.request(e.RequestID, withoutQuery(e.Response.URL)), e.Response.Status), true
		}
	case *network.EventLoadingFailed:
		if !e.Canceled {
			return fmt.Sprintf("%s failed: %s", l.request(e.RequestID, ""), e.ErrorText), true
		}
	}
	return "", false
}

func withoutQuery(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	u.RawQuery, u.Fragment = "", ""
	return u.String()
}

func consoleText(args []*runtime.RemoteObject) string {
	parts := make([]string, 0, len(args))
	for _, a := range args {
		switch {
		case a.Description != "":
			parts = append(parts, firstLine(a.Description))
		case len(a.Value) > 0:
			var s string
			if json.Unmarshal(a.Value, &s) == nil {
				parts = append(parts, s)
			} else {
				parts = append(parts, string(a.Value))
			}
		}
	}
	return strings.Join(parts, " ")
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}

func (t *Tab) note(line string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.console = append(t.console, time.Now().Format("15:04:05")+" "+line)
	if len(t.console) > consoleLimit {
		t.console = t.console[len(t.console)-consoleLimit:]
	}
}

func (l *pageLog) remember(id network.RequestID, what string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.requests == nil || len(l.requests) > 500 {
		l.requests = map[network.RequestID]string{}
	}
	l.requests[id] = what
}

func (l *pageLog) request(id network.RequestID, fallback string) string {
	l.mu.Lock()
	defer l.mu.Unlock()
	if what, ok := l.requests[id]; ok {
		return what
	}
	return fallback
}

func (t *Tab) takeConsole() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	lines := t.console
	t.console = nil
	return lines
}

type bindingCall struct {
	ID     int             `json:"id"`
	API    string          `json:"api"`
	Method string          `json:"method"`
	Path   string          `json:"path"`
	Body   json.RawMessage `json:"body,omitempty"`
}

func (b *Browser) handleCall(t *Tab, payload string) {
	var c bindingCall
	if err := json.Unmarshal([]byte(payload), &c); err != nil {
		return
	}
	status, body := b.forward(t, c)
	reply := fmt.Sprintf("window.__factoryReviewReceive && window.__factoryReviewReceive(%d, %d, %s)", c.ID, status, jsString(body))
	b.evaluate(t, reply)
}

func (b *Browser) forward(t *Tab, c bindingCall) (int, string) {
	prefix := "/r/" + b.server.token + "/"
	if !strings.HasPrefix(c.API, prefix) || strings.Contains(c.Path, "..") {
		return http.StatusForbidden, "not a review API"
	}
	if c.Method == http.MethodPost && c.Path == "/send" {
		b.attachConsole(t, c.API)
	}
	status, data := b.call(c.Method, c.API+c.Path, c.Body)
	if status == http.StatusOK && c.Method == http.MethodPost && c.Path == "/threads" {
		b.shoot(t, c.API, data)
	}
	return status, string(data)
}

func (b *Browser) call(method, path string, body []byte) (int, []byte) {
	req, err := http.NewRequest(method, "http://"+SocketHost+path, bytes.NewReader(body))
	if err != nil {
		return http.StatusBadRequest, []byte(err.Error())
	}
	if len(body) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := b.client.Do(req)
	if err != nil {
		return http.StatusBadGateway, []byte(err.Error())
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, data
}

func (b *Browser) attachConsole(t *Tab, api string) {
	lines := t.takeConsole()
	if len(lines) == 0 {
		return
	}
	body, _ := json.Marshal(map[string][]string{"lines": lines})
	b.call(http.MethodPost, api+"/console", body)
}

func (b *Browser) shoot(t *Tab, api string, created []byte) {
	var thread Thread
	if json.Unmarshal(created, &thread) != nil || thread.ID == "" {
		return
	}
	png, err := b.capture(t, thread.Anchor)
	if err != nil {
		return
	}
	body, _ := json.Marshal(map[string][]byte{"png": png})
	b.call(http.MethodPost, api+"/threads/"+url.PathEscape(thread.ID)+"/shot", body)
}

func (b *Browser) capture(t *Tab, a Anchor) ([]byte, error) {
	clip := clipFor(a)
	if clip == nil {
		return nil, errors.New("nothing to capture")
	}
	b.evaluate(t, "window.__factoryReviewHide && window.__factoryReviewHide(true)")
	defer b.evaluate(t, "window.__factoryReviewHide && window.__factoryReviewHide(false)")
	ctx, done := context.WithTimeout(t.ctx, callTimeout)
	defer done()
	beyond := true
	shot, err := chromedp.Call(ctx, page.CaptureScreenshot, page.CaptureScreenshotParams{Format: page.CaptureScreenshotFormatPng, Clip: clip, CaptureBeyondViewport: &beyond})
	if err != nil {
		return nil, err
	}
	return shot.Data, nil
}

func clipFor(a Anchor) *page.Viewport {
	r := a.Rect
	if r == nil || r.W <= 0 || r.H <= 0 {
		r = a.Viewport
	}
	if r == nil || r.W <= 0 || r.H <= 0 {
		return nil
	}
	x, y := max(0, r.X-shotPadding), max(0, r.Y-shotPadding)
	return &page.Viewport{X: x, Y: y, Width: r.W + (r.X - x) + shotPadding, Height: min(r.H+(r.Y-y)+shotPadding, 2400), Scale: 1}
}

func (b *Browser) evaluate(t *Tab, js string) {
	ctx, done := context.WithTimeout(t.ctx, callTimeout)
	defer done()
	chromedp.Call(ctx, runtime.Evaluate, runtime.EvaluateParams{Expression: js})
}

func (b *Browser) push(e event) {
	if e.name != "threads" {
		return
	}
	b.mu.Lock()
	tabs := append([]*Tab(nil), b.tabs...)
	b.mu.Unlock()
	for _, t := range tabs {
		if t.factory.Key == e.data && t.ctx.Err() == nil {
			go b.evaluate(t, fmt.Sprintf("window.__factoryReviewEvent && window.__factoryReviewEvent(%s, %s)", jsString(e.name), jsString(e.data)))
		}
	}
}

func jsString(s string) string {
	data, _ := json.Marshal(s)
	return string(data)
}
