package review

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image/png"
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

func TestADesignIsCapturedHeadlessPerScreenAndViewportAndItsFrozenCopyIgnoresLaterEdits(t *testing.T) {
	if _, ok := ChromeBinary(""); !ok {
		if os.Getenv("CI") == "" {
			t.Skip("no Chrome here")
		}
		t.Fatal("Chrome is missing on CI")
	}
	dir := t.TempDir()
	design := filepath.Join(dir, "design", "variant-a.html")
	os.MkdirAll(filepath.Dir(design), 0o755)
	write(t, design, `<!doctype html><html><head><style>:root{--accent:#0a7cff} body{margin:0} section{height:300px} h2{margin:0;font-size:24px;color:var(--accent)} @media (max-width:500px){h2{font-size:18px}}</style></head><body><section id="list"><h2 data-ref="title">Checks</h2></section><section id="detail" style="height:120px">Detail</section></body></html>`)
	viewports := []Viewport{{1440, 900}, {390, 844}}
	result, err := Capture(context.Background(), "", goruntime.GOOS == "linux", CaptureRequest{Dir: dir, Source: "design/variant-a.html", Screens: []string{"list", "detail"}, Viewports: viewports})
	if err != nil {
		t.Fatal(err)
	}
	refDir := filepath.Join(dir, ReferenceDir)
	for _, screen := range []string{"list", "detail"} {
		for _, v := range viewports {
			f, err := os.Open(filepath.Join(refDir, screen+"@"+v.String()+".png"))
			if err != nil {
				t.Fatal(err)
			}
			config, err := png.DecodeConfig(f)
			f.Close()
			if err != nil || config.Width != v.W {
				t.Errorf("#%s at %s is %d px wide (%v); want the section's own %d px, rendered at that viewport", screen, v, config.Width, err, v.W)
			}
		}
	}
	var tokens Tokens
	data, _ := os.ReadFile(filepath.Join(refDir, TokensFile))
	json.Unmarshal(data, &tokens)
	title := func(v Viewport) string { return tokens.Screens["list"][v.String()].Elements["title"].Style["fontSize"] }
	if tokens.Root["--accent"] != "#0a7cff" || title(viewports[0]) != "24px" || title(viewports[1]) != "18px" {
		t.Fatalf("tokens.json holds root %v and title sizes %q and %q; want the :root token and each viewport's computed size", tokens.Root, title(viewports[0]), title(viewports[1]))
	}

	other := filepath.Join(dir, "design", "figma-frames.html")
	write(t, other, `<!doctype html><html><head><style>:root{--accent:#ff3b30} body{margin:0} section{height:200px}</style></head><body><section id="settings">Settings</section></body></html>`)
	if _, err := Capture(context.Background(), "", goruntime.GOOS == "linux", CaptureRequest{Dir: dir, Source: "design/figma-frames.html", Screens: []string{"settings"}, Viewports: viewports[:1]}); err != nil {
		t.Fatal(err)
	}
	var mixed Tokens
	data, _ = os.ReadFile(filepath.Join(refDir, TokensFile))
	json.Unmarshal(data, &mixed)
	governing := func(screen string) string {
		return mixed.Sources[mixed.Screens[screen][viewports[0].String()].Source].Root["--accent"]
	}
	if governing("list") != "#0a7cff" || governing("settings") != "#ff3b30" {
		t.Fatalf("after capturing a second source, list compares with accent %q and settings with %q; want each screen's own source's tokens", governing("list"), governing("settings"))
	}

	write(t, design, `<!doctype html><section id="../../escaped">edited after approval</section>`)
	if _, err := Capture(context.Background(), "", goruntime.GOOS == "linux", CaptureRequest{Dir: dir, Source: "design/variant-a.html", Screens: []string{"list"}, Viewports: viewports[:1]}); err == nil {
		t.Fatal("capturing a page without #list succeeded")
	}
	_, err = Capture(context.Background(), "", goruntime.GOOS == "linux", CaptureRequest{Dir: dir, Source: "design/variant-a.html", Screens: []string{"../../escaped"}, Viewports: viewports[:1]})
	if escaped, _ := filepath.Glob(filepath.Join(dir, "escaped@*.png")); err == nil || len(escaped) > 0 {
		t.Fatalf("a screen id with a path in it gave %v and wrote %v outside design/reference", err, escaped)
	}
	frozen, err := fileSHA256(filepath.Join(dir, filepath.FromSlash(tokens.Source.Ref)))
	if err != nil || frozen != result.SHA256 || tokens.Source.SHA256 != result.SHA256 || tokens.Source.Ref != ReferenceDir+"/source/variant-a.html" {
		t.Fatalf("after editing the approved variant and a failed recapture, the reference %s hashes to %s (%v), want the printed %s", tokens.Source.Ref, frozen, err, result.SHA256)
	}
}
