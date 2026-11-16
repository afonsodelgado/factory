package review

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
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
	profile   string
	exec      string
	headless  bool
	noSandbox bool

	mu     sync.Mutex
	root   context.Context
	cancel context.CancelFunc
	tabs   []*Tab
}

type Endpoint struct {
	Origin string
	Token  string
}

type Tab struct {
	ctx      context.Context
	cancel   context.CancelFunc
	factory  Factory
	app      bool
	endpoint Endpoint

	mu       sync.Mutex
	console  []string
	requests map[network.RequestID]string
}

func (s *Server) NewBrowser(profile, exec string, headless bool) *Browser {
	b := &Browser{server: s, profile: profile, exec: exec, headless: headless}
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
	if !b.headless {
		opts = append(opts, chromedp.VisibleWindow, chromedp.WindowSize(1440, 960))
	}
	if b.exec != "" {
		opts = append(opts, chromedp.ExecPath(b.exec))
	}
	if b.noSandbox {
		opts = append(opts, chromedp.NoSandbox)
	}
	alloc, cancelAlloc := chromedp.NewExecAllocator(context.Background(), opts...)
	root, cancelRoot := chromedp.NewContext(alloc)
	if err := chromedp.Do(root); err != nil {
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

type Headless struct {
	Profile string
	URL     string
	PID     int

	ctx    context.Context
	cancel context.CancelFunc
}

func LaunchHeadless(profile, exec string, noSandbox bool) (*Headless, error) {
	opts := append(chromedp.DefaultExecAllocatorOptions[:], chromedp.UserDataDir(profile), remote.WebSocket)
	if exec != "" {
		opts = append(opts, chromedp.ExecPath(exec))
	}
	if noSandbox {
		opts = append(opts, chromedp.NoSandbox)
	}
	alloc, cancelAlloc := chromedp.NewExecAllocator(context.Background(), opts...)
	ctx, cancelCtx := chromedp.NewContext(alloc)
	h := &Headless{Profile: profile, ctx: ctx, cancel: func() { cancelCtx(); cancelAlloc() }}
	if err := chromedp.Do(ctx); err != nil {
		h.Close()
		return nil, fmt.Errorf("headless Chrome did not start: %w", err)
	}
	h.PID = chromedp.FromContext(ctx).Browser.Process().Pid
	port, err := devToolsPort(profile)
	if err != nil {
		h.Close()
		return nil, err
	}
	h.URL = "http://127.0.0.1:" + port
	return h, nil
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

func (h *Headless) Navigate(ctx context.Context, url string) error {
	run, cancel := context.WithCancel(h.ctx)
	defer cancel()
	defer context.AfterFunc(ctx, cancel)()
	return chromedp.Do(run, chromedp.Navigate(url))
}

func (h *Headless) SetCookies(url string, cookies []Cookie) error {
	steps := make([]chromedp.Action[chromedp.Void], 0, len(cookies))
	for _, c := range cookies {
		steps = append(steps, call(network.SetCookie, network.SetCookieParams{Name: c.Name, Value: c.Value, URL: url, Domain: c.Domain, Path: c.Path}))
	}
	return chromedp.Do(h.ctx, steps...)
}

func (h *Headless) Close() error {
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

func (b *Browser) OpenPage(url string) error {
	f, ok := b.server.factoryOfPage(url)
	if !ok {
		return fmt.Errorf("%s is not a review page", url)
	}
	return b.OpenPageAt(b.server.endpoint(), url, f)
}

func (b *Browser) OpenPageAt(e Endpoint, url string, f Factory) error {
	_, err := b.open(e, url, f, "", nil)
	return err
}

func (b *Browser) OpenApp(url string, f Factory, page string, cookies []Cookie) (*Tab, error) {
	return b.OpenAppAt(b.server.endpoint(), url, f, page, cookies)
}

func (b *Browser) OpenAppAt(e Endpoint, url string, f Factory, page string, cookies []Cookie) (*Tab, error) {
	if !Live(page) {
		return nil, fmt.Errorf("%q is not a live page; use %s or %s", page, App, Prototype)
	}
	return b.open(e, url, f, page, cookies)
}

func (s *Server) endpoint() Endpoint { return Endpoint{Origin: s.Origin(), Token: s.token} }

type Cookie struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Domain string `json:"domain,omitempty"`
	Path   string `json:"path,omitempty"`
}

func (b *Browser) open(e Endpoint, url string, f Factory, livePage string, cookies []Cookie) (*Tab, error) {
	app := livePage != ""
	root, err := b.start()
	if err != nil {
		return nil, err
	}
	ctx, cancel := chromedp.NewContext(root, chromedp.WithNewWindow(false))
	tab := &Tab{ctx: ctx, cancel: cancel, factory: f, app: app, endpoint: e, requests: map[network.RequestID]string{}}
	if err := chromedp.Listen(ctx, tab.listener(b)); err != nil {
		cancel()
		return nil, err
	}
	steps := []chromedp.Action[chromedp.Void]{
		call(runtime.Enable, cdp.Empty{}),
		call(network.Enable, network.EnableParams{}),
		call(runtime.AddBinding, runtime.AddBindingParams{Name: bindingName}),
	}
	if app {
		steps = append(steps, call(page.AddScriptToEvaluateOnNewDocument, page.AddScriptToEvaluateOnNewDocumentParams{Source: appPrelude(e, f, livePage) + string(b.server.overlay)}))
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

func appPrelude(e Endpoint, f Factory, page string) string {
	config := map[string]string{
		"api":  "/r/" + e.Token + "/" + f.Key + "/_api",
		"page": f.Key + "/" + page,
		"file": page,
	}
	data, _ := json.Marshal(config)
	return "window.__factoryReviewConfig = " + string(data) + ";\n"
}

func (t *Tab) listener(b *Browser) func(ev any) {
	return func(ev any) {
		switch e := ev.(type) {
		case *runtime.EventBindingCalled:
			if e.Name == bindingName {
				go b.handleCall(t, e.Payload)
			}
		case *runtime.EventConsoleAPICalled:
			if e.Type == runtime.ConsoleAPICalledTypeError {
				t.note("console.error: " + consoleText(e.Args))
			}
		case *runtime.EventExceptionThrown:
			if d := e.ExceptionDetails; d != nil {
				text := d.Text
				if d.Exception != nil && d.Exception.Description != "" {
					text = firstLine(d.Exception.Description)
				}
				t.note("uncaught: " + text)
			}
		case *network.EventRequestWillBeSent:
			if e.Request != nil {
				t.remember(e.RequestID, e.Request.Method+" "+e.Request.URL)
			}
		case *network.EventResponseReceived:
			if e.Response != nil && e.Response.Status >= 400 {
				t.note(fmt.Sprintf("%s → %d", t.request(e.RequestID, e.Response.URL), e.Response.Status))
			}
		case *network.EventLoadingFailed:
			if !e.Canceled {
				t.note(fmt.Sprintf("%s failed: %s", t.request(e.RequestID, ""), e.ErrorText))
			}
		}
	}
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

func (t *Tab) remember(id network.RequestID, what string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.requests) > 500 {
		t.requests = map[network.RequestID]string{}
	}
	t.requests[id] = what
}

func (t *Tab) request(id network.RequestID, fallback string) string {
	t.mu.Lock()
	defer t.mu.Unlock()
	if what, ok := t.requests[id]; ok {
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
	prefix := "/r/" + t.endpoint.Token + "/"
	if !strings.HasPrefix(c.API, prefix) || strings.Contains(c.Path, "..") {
		return http.StatusForbidden, "not a review API"
	}
	if c.Method == http.MethodPost && c.Path == "/send" {
		b.attachConsole(t, c.API)
	}
	status, data := t.call(c.Method, c.API+c.Path, c.Body)
	if status == http.StatusOK && c.Method == http.MethodPost && c.Path == "/threads" {
		b.shoot(t, c.API, data)
	}
	return status, string(data)
}

func (t *Tab) call(method, path string, body []byte) (int, []byte) {
	req, err := http.NewRequest(method, t.endpoint.Origin+path, bytes.NewReader(body))
	if err != nil {
		return http.StatusBadRequest, []byte(err.Error())
	}
	if len(body) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
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
	t.call(http.MethodPost, api+"/console", body)
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
	t.call(http.MethodPost, api+"/threads/"+url.PathEscape(thread.ID)+"/shot", body)
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
		if t.app && t.factory.Key == e.data && t.ctx.Err() == nil {
			go b.evaluate(t, fmt.Sprintf("window.__factoryReviewEvent && window.__factoryReviewEvent(%s, %s)", jsString(e.name), jsString(e.data)))
		}
	}
}

func jsString(s string) string {
	data, _ := json.Marshal(s)
	return string(data)
}
