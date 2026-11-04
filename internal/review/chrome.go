package review

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
)

const (
	bindingName  = "__factoryReview"
	consoleLimit = 40
	callTimeout  = 15 * time.Second
	shotPadding  = 24
)

type Browser struct {
	server   *Server
	profile  string
	exec     string
	headless bool

	mu     sync.Mutex
	root   context.Context
	cancel context.CancelFunc
	tabs   []*Tab
}

type Tab struct {
	ctx     context.Context
	cancel  context.CancelFunc
	factory Factory
	app     bool

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

func (b *Browser) OpenPage(url string) error {
	f, ok := b.server.factoryOfPage(url)
	if !ok {
		return fmt.Errorf("%s is not a review page", url)
	}
	_, err := b.open(url, f, false, nil)
	return err
}

func (b *Browser) OpenApp(url string, f Factory, cookies []Cookie) (*Tab, error) {
	return b.open(url, f, true, cookies)
}

type Cookie struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Domain string `json:"domain,omitempty"`
	Path   string `json:"path,omitempty"`
}

func (b *Browser) open(url string, f Factory, app bool, cookies []Cookie) (*Tab, error) {
	root, err := b.start()
	if err != nil {
		return nil, err
	}
	ctx, cancel := chromedp.NewContext(root, chromedp.WithNewWindow(false))
	tab := &Tab{ctx: ctx, cancel: cancel, factory: f, app: app, requests: map[network.RequestID]string{}}
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
		steps = append(steps, call(page.AddScriptToEvaluateOnNewDocument, page.AddScriptToEvaluateOnNewDocumentParams{Source: b.server.appPrelude(f) + string(b.server.overlay)}))
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

func (s *Server) appPrelude(f Factory) string {
	config := map[string]string{
		"api":  "/r/" + s.token + "/" + f.Key + "/_api",
		"page": f.Key + "/" + App,
		"file": App,
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
	prefix := "/r/" + b.server.token + "/"
	if !strings.HasPrefix(c.API, prefix) || strings.Contains(c.Path, "..") {
		return http.StatusForbidden, "not a review API"
	}
	if c.Method == http.MethodPost && c.Path == "/send" {
		b.attachConsole(t, c.API)
	}
	req, err := http.NewRequest(c.Method, b.server.Origin()+c.API+c.Path, strings.NewReader(string(c.Body)))
	if err != nil {
		return http.StatusBadRequest, err.Error()
	}
	if len(c.Body) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return http.StatusBadGateway, err.Error()
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == http.StatusOK && c.Method == http.MethodPost && c.Path == "/threads" {
		b.shoot(t, c.API, data)
	}
	return resp.StatusCode, string(data)
}

func (b *Browser) attachConsole(t *Tab, api string) {
	f, ok := b.server.factoryOfAPI(api)
	if !ok {
		return
	}
	lines := t.takeConsole()
	if len(lines) == 0 {
		return
	}
	if st, err := b.server.Store(f); err == nil {
		st.AddConsole(lines)
	}
}

func (b *Browser) shoot(t *Tab, api string, created []byte) {
	var thread Thread
	if json.Unmarshal(created, &thread) != nil || thread.ID == "" {
		return
	}
	f, ok := b.server.factoryOfAPI(api)
	if !ok {
		return
	}
	png, err := b.capture(t, thread.Anchor)
	if err != nil {
		return
	}
	rel := "shots/" + thread.ID + ".png"
	path := filepath.Join(f.Dir, "review", filepath.FromSlash(rel))
	if os.MkdirAll(filepath.Dir(path), 0o755) != nil || os.WriteFile(path, png, 0o644) != nil {
		return
	}
	if st, err := b.server.Store(f); err == nil && st.SetShot(thread.ID, rel) == nil {
		b.server.ThreadsChanged(f)
	}
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
