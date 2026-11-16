package review

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
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
	resp, err := http.Get(origin + "/r/tok/")
	if err != nil {
		t.Fatal(err)
	}
	index, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(index), `href="/r/tok/k/plan.html"`) {
		t.Errorf("the index through the tunnel links:\n%s\nwant its pages on the tunnel's own origin, not the server's port", index)
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

func TestThePhoneAndTheDesktopShareThreadsWithoutTheDesktopToken(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "factory")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dir, "plan.html"), "<html><body><a href='https://example.com'>plan</a></body></html>")
	write(t, filepath.Join(root, "secret.txt"), "secret")
	if err := os.Symlink(filepath.Join(root, "secret.txt"), filepath.Join(dir, "link.txt")); err != nil {
		t.Fatal(err)
	}
	s, err := Listen(0, "desktop-token", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	s.SetFactories([]Factory{{Key: "k", Label: "csv", Dir: dir}})
	phone := httptest.NewServer(s.MobileHandler())
	t.Cleanup(phone.Close)

	resp, err := http.Get(phone.URL + "/r/k/plan.html")
	if err != nil {
		t.Fatal(err)
	}
	page, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || strings.Contains(string(page), "desktop-token") || !strings.Contains(string(page), `data-api="/r/k/_api"`) || resp.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("phone page %d, Cache-Control %q:\n%s\nwant it served with token-free API paths and no-store", resp.StatusCode, resp.Header.Get("Cache-Control"), page)
	}

	desktopEvents, err := http.Get(s.Origin() + "/r/desktop-token/events")
	if err != nil {
		t.Fatal(err)
	}
	defer desktopEvents.Body.Close()
	seen := make(chan string, 4)
	go func() {
		lines := bufio.NewScanner(desktopEvents.Body)
		for lines.Scan() {
			if data, ok := strings.CutPrefix(lines.Text(), "data: "); ok {
				seen <- data
			}
		}
	}()
	create := `{"page":"plan.html","intent":"fix","text":"Bigger.","anchor":{"selector":"a"},"operation":"op-1"}`
	for range 2 {
		resp, err := http.Post(phone.URL+"/r/k/_api/threads", "application/json", strings.NewReader(create))
		if err != nil || resp.StatusCode != 200 {
			t.Fatalf("phone comment: %v %v", resp, err)
		}
		resp.Body.Close()
	}
	if _, desktop := get(t, s.Origin()+"/r/desktop-token/k/_api/threads?page=plan.html"); strings.Count(desktop, `"id"`) != 1 {
		t.Fatalf("desktop threads %s; want the phone's comment once, even though its lost answer made it post twice", desktop)
	}
	for heard := ""; heard != "k"; {
		select {
		case heard = <-seen:
		case <-time.After(3 * time.Second):
			t.Fatal("the desktop page heard no thread change when the phone commented")
		}
	}

	for name, path := range map[string]string{
		"the desktop token route": "/r/desktop-token/k/plan.html",
		"a parent directory":      "/r/k/..%2Fsecret.txt",
		"a symlink out":           "/r/k/link.txt",
		"an unknown factory":      "/r/other/plan.html",
	} {
		if status, body := get(t, phone.URL+path); status == http.StatusOK || strings.Contains(body, "secret") {
			t.Errorf("%s: %s answered %d %q on the phone", name, path, status, body)
		}
	}
	for _, path := range []string{"/r/k/_api/console", "/r/k/_api/threads/t-1/shot"} {
		resp, err := http.Post(phone.URL+path, "application/json", strings.NewReader(`{}`))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			t.Errorf("the phone reached the desktop browser endpoint %s", path)
		}
	}
}

func TestAPhonePageAskedForBeforeTheFactoriesAreListedWaitsForThem(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "plan.html"), "<html><body>plan</body></html>")
	s, err := Listen(0, "desktop-token", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	sockets, err := os.MkdirTemp("/tmp", "frm-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(sockets) })
	socket := MobileSocket(sockets)
	if err := s.ServeMobile(socket); err != nil {
		t.Fatal(err)
	}
	phone := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", socket)
	}}}
	answered := make(chan int, 1)
	go func() {
		resp, err := phone.Get("http://review/r/k/plan.html")
		if err != nil {
			answered <- 0
			return
		}
		resp.Body.Close()
		answered <- resp.StatusCode
	}()
	select {
	case status := <-answered:
		t.Fatalf("a phone page asked for while the hub was still listing its factories answered %d; want it held until they are listed", status)
	case <-time.After(200 * time.Millisecond):
	}
	s.SetFactories([]Factory{{Key: "k", Label: "csv", Dir: dir}})
	if status := <-answered; status != 200 {
		t.Fatalf("the held phone page answered %d once the factories were listed; want 200", status)
	}
}

func TestSendHoldsEditsUntilTheBatchIsMarkedSent(t *testing.T) {
	st, err := OpenStore(t.TempDir(), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	thread, err := st.Create(NewThread{Page: "plan.html", Intent: "fix", Text: "first words"})
	if err != nil {
		t.Fatal(err)
	}
	edited := make(chan error, 1)
	var batched string
	if _, err := st.Send("plan.html", func(drafts []Thread, _ int) error {
		batched = drafts[0].Messages[0].Text
		text := "second words"
		go func() {
			_, err := st.Update(thread.ID, Change{Text: &text})
			edited <- err
		}()
		select {
		case <-edited:
			t.Error("a comment changed while its batch was being delivered")
		case <-time.After(100 * time.Millisecond):
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := <-edited; err == nil || batched != "first words" {
		t.Fatalf("edit after send: %v, batch held %q; want the edit refused because the agent already has the first words", err, batched)
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
