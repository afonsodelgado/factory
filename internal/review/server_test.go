package review

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestServerServesOnlyTheFactoryDirWithTheToken(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "factory")
	if err := os.MkdirAll(filepath.Join(dir, "design"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dir, "design", "variant-a.html"), "<html><body><h1>A</h1></body></html>")
	write(t, filepath.Join(root, "secret.txt"), "secret")
	if err := os.Symlink(filepath.Join(root, "secret.txt"), filepath.Join(dir, "link.txt")); err != nil {
		t.Fatal(err)
	}

	s, err := Listen(0, "tok", []byte("// overlay"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	s.SetFactories([]Factory{{Key: "repo-1-csv", Label: "csv", Dir: dir}})

	page, ok := s.PageURL("file://" + filepath.Join(dir, "design", "variant-a.html"))
	if !ok {
		t.Fatal("PageURL did not map a file inside the factory dir")
	}
	status, body := get(t, page)
	if status != http.StatusOK || !strings.Contains(body, `src="/_review/overlay.js"`) || !strings.HasSuffix(body, "</body></html>") {
		t.Fatalf("page: %d %q", status, body)
	}

	for name, path := range map[string]string{
		"wrong token":       "/r/nope/repo-1-csv/design/variant-a.html",
		"parent directory":  "/r/tok/repo-1-csv/..%2Fsecret.txt",
		"symlink out":       "/r/tok/repo-1-csv/link.txt",
		"unknown factory":   "/r/tok/other/design/variant-a.html",
		"outside any route": "/secret.txt",
	} {
		if status, body := get(t, s.Origin()+path); status == http.StatusOK || strings.Contains(body, "secret") {
			t.Errorf("%s: %s answered %d %q", name, path, status, body)
		}
	}

	req, _ := http.NewRequest("GET", page, nil)
	req.Host = "evil.example:80"
	if resp, err := http.DefaultClient.Do(req); err != nil || resp.StatusCode != http.StatusForbidden {
		t.Errorf("a foreign Host header was served: %v %v", resp, err)
	}
}

func TestEditingAServedPageNotifiesOpenTabs(t *testing.T) {
	dir := t.TempDir()
	page := filepath.Join(dir, "plan.html")
	write(t, page, "<body>v1</body>")
	s, err := Listen(0, "tok", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	s.SetFactories([]Factory{{Key: "k", Label: "csv", Dir: dir}})
	url, _ := s.PageURL("file://" + page)
	get(t, url)

	resp, err := http.Get(s.Origin() + "/r/tok/events")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	changes := make(chan string)
	go func() {
		lines := bufio.NewScanner(resp.Body)
		for lines.Scan() {
			if data, ok := strings.CutPrefix(lines.Text(), "data: "); ok {
				changes <- data
				return
			}
		}
	}()
	write(t, page, "<body>v2</body>")
	select {
	case got := <-changes:
		if got != "k/plan.html" {
			t.Fatalf("change for %q, want k/plan.html", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no change event after the page was rewritten")
	}
}

func TestCommentsSurviveAHubRestart(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "plan.html"), "<body>plan</body>")
	s, err := Listen(0, "tok", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	s.SetFactories([]Factory{{Key: "k", Label: "csv", Dir: dir}})

	body := `{"page":"plan.html","intent":"question","text":"Why two layers?","anchor":{"selector":"#q1","text":"Q1"}}`
	req, _ := http.NewRequest("POST", s.Origin()+"/r/tok/k/_api/threads", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("create: %v %v", resp, err)
	}

	reopened, err := OpenStore(dir, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	threads := reopened.Threads("plan.html")
	if len(threads) != 1 || threads[0].Anchor.Selector != "#q1" || threads[0].Messages[0].Text != "Why two layers?" || threads[0].Status != Draft {
		t.Fatalf("after reopening: %+v", threads)
	}
	if open, _ := s.OpenCount("file://" + filepath.Join(dir, "plan.html")); open != 1 {
		t.Fatalf("open count %d, want 1", open)
	}

	os.RemoveAll(filepath.Join(dir, "review"))
	s.SetFactories(nil)
	s.SetFactories([]Factory{{Key: "k", Label: "csv", Dir: dir}})
	if st, _ := s.Store(Factory{Key: "k", Dir: dir}); len(st.Threads("")) != 0 {
		t.Error("a factory deleted and created again with the same id still shows the old factory's comments")
	}
}

func TestForwardedReviewPreservesHostAndOriginChecks(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "plan.html"), "<body>plan</body>")
	s, err := Listen(0, "tok", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	s.SetFactories([]Factory{{Key: "k", Label: "csv", Dir: dir}})
	s.SetActions(func(a Action) (any, error) { return map[string]string{"did": a.Kind}, nil })
	tunnel, local := forwardTo(t, s.ln.Addr().String()), forwardTo(t, s.ln.Addr().String())
	s.Allow(tunnel)
	origin := "http://127.0.0.1:" + strconv.Itoa(tunnel)

	call := func(method, url, host, from, body string) int {
		t.Helper()
		req, _ := http.NewRequest(method, url, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if host != "" {
			req.Host = host
		}
		if from != "" {
			req.Header.Set("Origin", from)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	api := origin + "/r/tok/k/_api"
	for name, status := range map[string]int{
		"render":  call("GET", origin+"/r/tok/k/plan.html", "", "", ""),
		"reload":  call("GET", origin+"/r/tok/k/plan.html", "localhost:"+strconv.Itoa(tunnel), "", ""),
		"comment": call("POST", api+"/threads", "", origin, `{"page":"plan.html","intent":"question","text":"Why?","anchor":{"selector":"#q1"}}`),
		"reply":   call("POST", api+"/threads/t-1/replies", "", origin, `{"text":"Because."}`),
		"approve": call("POST", api+"/approve", "", origin, `{"page":"plan.html"}`),
	} {
		if status != http.StatusOK {
			t.Errorf("%s through the tunnel answered %d; want 200", name, status)
		}
	}
	for name, status := range map[string]int{
		"an unregistered forwarded port": call("GET", "http://127.0.0.1:"+strconv.Itoa(local)+"/r/tok/k/plan.html", "", "", ""),
		"a forged Host":                  call("GET", origin+"/r/tok/k/plan.html", "evil.example:"+strconv.Itoa(tunnel), "", ""),
		"a forged Origin":                call("POST", api+"/approve", "", "http://evil.example", `{"page":"plan.html"}`),
		"another local port as Origin":   call("POST", api+"/approve", "", "http://127.0.0.1:"+strconv.Itoa(local), `{"page":"plan.html"}`),
		"a path escape":                  call("GET", origin+"/r/tok/k/..%2F..%2Fetc%2Fpasswd", "", "", ""),
	} {
		if status == http.StatusOK {
			t.Errorf("%s was accepted through the tunnel", name)
		}
	}
}

func forwardTo(t *testing.T, dest string) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				d, err := net.Dial("tcp", dest)
				if err != nil {
					return
				}
				defer d.Close()
				go io.Copy(d, c)
				io.Copy(c, d)
			}()
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func get(t *testing.T, url string) (int, string) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}
