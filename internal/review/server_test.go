package review

import (
	"bufio"
	"io"
	"net/http"
	"os"
	"path/filepath"
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
