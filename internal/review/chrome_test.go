package review

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	goruntime "runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

func TestHeadlessChromeCommentsOnARunningAppThroughTheReviewSocketWithAScreenshotAndItsErrors(t *testing.T) {
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", "script-src 'self' 'unsafe-inline'")
		fmt.Fprint(w, `<!doctype html><html><body><h1>Contacts</h1><button id="export" style="width:120px;height:40px">Export</button><script>console.error("export failed to load")</script></body></html>`)
	}))
	t.Cleanup(app.Close)

	overlay, err := os.ReadFile("../../bundle/review/overlay.js")
	if err != nil {
		t.Fatal(err)
	}
	s, socket := serveSocket(t, "tok", overlay)
	f := Factory{Key: "k", Label: "csv", Dir: t.TempDir()}
	s.SetFactories([]Factory{f})
	var mu sync.Mutex
	var sentConsole []string
	s.SetActions(func(a Action) (any, error) {
		if a.Kind == "send" {
			st, _ := s.Store(f)
			mu.Lock()
			sentConsole = st.TakeConsole()
			mu.Unlock()
		}
		return map[string]string{}, nil
	})

	chrome := s.NewBrowser(socket, t.TempDir(), "")
	chrome.noSandbox = goruntime.GOOS == "linux"
	t.Cleanup(chrome.Close)
	tab, err := chrome.OpenApp(app.URL, f, App, nil)
	if err != nil {
		if os.Getenv("CI") == "" {
			t.Skip("no Chrome here: ", err)
		}
		t.Fatal(err)
	}

	waitFor(t, "the overlay on the app page", func() bool {
		loaded, _ := chromedp.Run(tab.ctx, chromedp.Evaluate[bool](`!!document.querySelector("factory-review")`))
		return loaded
	})
	call := func(js string) {
		if _, err := chromedp.Run(tab.ctx, chromedp.Evaluate[any](js)); err != nil {
			t.Fatal(err)
		}
	}
	call(`window.__factoryReview(JSON.stringify({id: 1, api: "/r/tok/k/_api", method: "POST", path: "/threads", body: {page: "app", intent: "fix", text: "Make Export primary.", anchor: {url: location.href, selector: "#export", rect: {x: 8, y: 60, w: 120, h: 40}}}}))`)

	st, _ := s.Store(f)
	waitFor(t, "the comment's screenshot", func() bool {
		threads := st.Threads(App)
		return len(threads) == 1 && threads[0].Shot != ""
	})
	png, err := os.ReadFile(filepath.Join(f.Dir, "review", st.Threads(App)[0].Shot))
	if err != nil || !bytes.HasPrefix(png, []byte("\x89PNG")) {
		t.Fatalf("the screenshot is not a PNG: %v", err)
	}

	call(`window.__factoryReview(JSON.stringify({id: 2, api: "/r/tok/k/_api", method: "POST", path: "/send", body: {page: "app"}}))`)
	waitFor(t, "the console error to reach the send", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return strings.Contains(strings.Join(sentConsole, "\n"), "export failed to load")
	})
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func TestHeadlessChromeStartsWithoutAWindowExtensionsOrKeychainPromptsOnMacAndLinux(t *testing.T) {
	for _, goos := range []string{"darwin", "linux"} {
		t.Run(goos, func(t *testing.T) {
			dir := t.TempDir()
			argv, profile := filepath.Join(dir, "argv"), filepath.Join(dir, "profile")
			fake := filepath.Join(dir, "chrome")
			if err := os.WriteFile(fake, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > '"+argv+"'\n"), 0o755); err != nil {
				t.Fatal(err)
			}
			alloc, cancelAlloc := chromedp.NewExecAllocator(context.Background(), launchOptions(goos, profile, fake, false)...)
			defer cancelAlloc()
			ctx, cancel := chromedp.NewContext(alloc)
			defer cancel()
			chromedp.Do(ctx)
			data, err := os.ReadFile(argv)
			if err != nil {
				t.Fatal(err)
			}
			args := strings.Split(strings.TrimSpace(string(data)), "\n")
			want := []string{"--headless", "--user-data-dir=" + profile, "--remote-debugging-port=0", "--disable-extensions", map[string]string{"darwin": "--use-mock-keychain", "linux": "--password-store=basic"}[goos]}
			for _, arg := range want {
				if !slices.Contains(args, arg) {
					t.Errorf("Chrome on %s starts without %s:\n%s", goos, arg, data)
				}
			}
			for _, arg := range args {
				if strings.HasPrefix(arg, "--load-extension") || strings.HasPrefix(arg, "--disable-extensions-except") || strings.HasPrefix(arg, "--start-") || strings.HasPrefix(arg, "--window-") || (arg == "--no-sandbox" && os.Getuid() != 0) {
					t.Errorf("Chrome on %s starts with %s, which a headless, sandboxed QA browser never needs:\n%s", goos, arg, data)
				}
			}
		})
	}
}
