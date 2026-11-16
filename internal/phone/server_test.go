package phone

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/afonsodelgado/factory/internal/review"
)

const testHost = "devbox.tail1234.ts.net"

type gateway struct {
	t      *testing.T
	server *Server
	http   *httptest.Server
	now    time.Time
}

func newGateway(t *testing.T) *gateway {
	g := &gateway{t: t, now: time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)}
	g.server = &Server{Store: Open(t.TempDir()), VAPIDKey: "key", Now: func() time.Time { return g.now }}
	g.http = httptest.NewServer(g.server.Handler())
	t.Cleanup(g.http.Close)
	return g
}

func (g *gateway) do(method, path, body string, headers ...string) *http.Response {
	g.t.Helper()
	req, _ := http.NewRequest(method, g.http.URL+path, strings.NewReader(body))
	req.Host = testHost
	if method != http.MethodGet {
		req.Header.Set("Content-Type", "application/json")
	}
	for i := 0; i+1 < len(headers); i += 2 {
		if headers[i+1] == "" {
			req.Header.Del(headers[i])
			continue
		}
		req.Header.Add(headers[i], headers[i+1])
	}
	resp, err := http.DefaultTransport.RoundTrip(req)
	if err != nil {
		g.t.Fatal(err)
	}
	g.t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func (g *gateway) pushState(resp *http.Response) PushState {
	g.t.Helper()
	var state PushState
	if err := json.NewDecoder(resp.Body).Decode(&state); err != nil || resp.StatusCode != 200 {
		g.t.Fatalf("push route answered %d: %v", resp.StatusCode, err)
	}
	return state
}

func TestReopeningTheAppReRegistersItsSubscriptionWithoutDroppingOtherPhones(t *testing.T) {
	g := newGateway(t)
	register := func(installation string, sub Subscription) PushState {
		body, _ := json.Marshal(map[string]any{"endpoint": sub.Endpoint, "expirationTime": nil, "keys": map[string]string{"p256dh": sub.P256dh, "auth": sub.Auth}, "installation": installation})
		return g.pushState(g.do("POST", "/api/push/subscription", string(body)))
	}
	pixel, ipad := subscriptionFor(t, "", "https://push.example/pixel"), subscriptionFor(t, "", "https://push.example/ipad")
	register("pixel", pixel)
	register("ipad", ipad)
	if state := register("pixel", pixel); !state.Subscribed || state.Endpoint != pixel.Endpoint {
		t.Fatalf("reopening answered %+v; want the same subscription still on", state)
	}
	if subs, _ := g.server.Store.Subscriptions(); len(subs) != 2 {
		t.Fatalf("subscriptions after reopening %+v; want both phones, once each", subs)
	}
	renewed := subscriptionFor(t, "", "https://push.example/pixel-renewed")
	register("pixel", renewed)
	state := g.pushState(g.do("GET", "/api/push/subscription?installation=pixel", ""))
	if subs, _ := g.server.Store.Subscriptions(); len(subs) != 2 || state.Endpoint != renewed.Endpoint || state.Key != "key" {
		t.Fatalf("after the browser renewed its endpoint the store holds %+v and reports %+v; want the old endpoint replaced and the iPad kept", subs, state)
	}
}

func TestReviewGrantsOpenOnlyTheirOwnFactoryInASandbox(t *testing.T) {
	g := newGateway(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "plan.html"), []byte("<body>plan</body>"), 0o644); err != nil {
		t.Fatal(err)
	}
	pages, err := review.Listen(0, "desktop-token", []byte("// overlay"))
	if err != nil {
		t.Fatal(err)
	}
	other := t.TempDir()
	if err := os.WriteFile(filepath.Join(other, "plan.html"), []byte("<body>pdf plan</body>"), 0o644); err != nil {
		t.Fatal(err)
	}
	pages.SetFactories([]review.Factory{{Key: "k", Label: "csv", Dir: dir}, {Key: "k2", Label: "pdf", Dir: other}})
	sockets, err := os.MkdirTemp("", "fr")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(sockets) })
	socket := review.MobileSocket(sockets)
	if err := pages.ServeMobile(socket); err != nil {
		t.Fatal(err)
	}
	g.server.Review = ReviewProxy(socket)

	if resp := g.do("GET", "/rt/nope/r/k/plan.html", ""); resp.StatusCode != 401 || !strings.Contains(body(resp), "expired") {
		t.Fatalf("a page link nobody granted answered %d; want the expired page", resp.StatusCode)
	}
	opened := g.do("GET", "/r/k/plan.html", "")
	grantPath := opened.Header.Get("Location")
	if opened.StatusCode != 303 || !strings.HasPrefix(grantPath, "/rt/") || !strings.HasSuffix(grantPath, "/r/k/plan.html") {
		t.Fatalf("the owner opening a page answered %d to %q; want a redirect into its sandboxed grant", opened.StatusCode, grantPath)
	}
	token := strings.Split(grantPath, "/")[2]
	granted := g.do("GET", grantPath, "")
	rendered := body(granted)
	if granted.StatusCode != 200 || !strings.Contains(rendered, "data-mobile") || !strings.Contains(rendered, `data-api="/rt/`+token+`/r/k/_api"`) {
		t.Fatalf("the granted page answered %d with %q; want the phone overlay pointed at the grant", granted.StatusCode, rendered)
	}
	if policy := granted.Header.Get("Content-Security-Policy"); !strings.HasPrefix(policy, "sandbox ") || strings.Contains(policy, "allow-same-origin") {
		t.Fatalf("a review page carried the policy %q; its scripts must run in an opaque origin, away from the phone API", policy)
	}
	comment := `{"page":"plan.html","intent":"fix","text":"Bigger.","anchor":{"url":"https://` + testHost + grantPath + `"}}`
	if resp := g.do("POST", "/rt/"+token+"/r/k/_api/threads", comment, "Origin", "", "Origin", "null"); resp.StatusCode != 200 {
		t.Fatalf("the granted page's comment answered %d", resp.StatusCode)
	}
	if stored, _ := os.ReadFile(review.StorePath(dir)); !strings.Contains(string(stored), `/r/k/plan.html"`) || strings.Contains(string(stored), token) {
		t.Fatalf("threads.json holds %s; want the page's address without the grant, which works as a credential", stored)
	}
	if resp := g.do("OPTIONS", "/rt/"+token+"/r/k/_api/approve", "", "Origin", "", "Origin", "null", "Content-Type", ""); resp.StatusCode != 204 {
		t.Fatalf("the sandboxed page's preflight answered %d; want 204 so its JSON comments and approvals can follow", resp.StatusCode)
	}
	for _, path := range []string{"/r/k2/plan.html", "/r/k%2F..%2Fk2/plan.html"} {
		if resp := g.do("GET", "/rt/"+token+path, ""); resp.StatusCode == 200 {
			t.Fatalf("a grant minted for one factory opened %s: %s; want it limited to its own factory", path, body(resp))
		}
	}
	if resp := g.do("POST", "/r/k/_api/threads", `{"page":"plan.html","intent":"fix","text":"Bigger."}`); resp.StatusCode != 405 {
		t.Fatalf("a comment sent straight to the app-origin review path answered %d; want it refused, so only the sandboxed grant writes", resp.StatusCode)
	}

	req, _ := http.NewRequest("GET", g.http.URL+"/rt/"+token+"/r/k/_api/events", nil)
	req.Host = testHost
	stream, err := http.DefaultTransport.RoundTrip(req)
	if err != nil || stream.StatusCode != 200 {
		t.Fatalf("review stream: %v %v", err, stream)
	}
	defer stream.Body.Close()
	seen := make(chan string, 16)
	ended := make(chan bool)
	go func() {
		lines := bufio.NewScanner(stream.Body)
		for lines.Scan() {
			if data, ok := strings.CutPrefix(lines.Text(), "data: "); ok {
				seen <- data
			}
		}
		ended <- true
	}()
	otherGrant := g.do("GET", "/r/k2/plan.html", "").Header.Get("Location")
	otherComment := `{"page":"plan.html","intent":"fix","text":"Smaller.","anchor":{"url":"https://` + testHost + otherGrant + `"}}`
	for _, post := range [][2]string{{otherGrant, otherComment}, {grantPath, comment}} {
		threads := strings.TrimSuffix(post[0], "/plan.html") + "/_api/threads"
		if resp := g.do("POST", threads, post[1], "Origin", "", "Origin", "null"); resp.StatusCode != 200 {
			t.Fatalf("a comment through %s answered %d", threads, resp.StatusCode)
		}
	}
	for data := ""; data != "k"; {
		select {
		case data = <-seen:
			if data == "k2" || strings.HasPrefix(data, "k2/") {
				t.Fatalf("the review stream of a grant for k sent %q; want only its own factory's events", data)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("the review stream never sent the grant's own factory's comment")
		}
	}
	pages.Close()
	if resp := g.do("GET", "/rt/"+token+"/r/k/_api/threads", ""); resp.StatusCode != 503 {
		t.Fatalf("with the review pages restarting, the review API answered %d; want a retryable 503", resp.StatusCode)
	}
}

func body(resp *http.Response) string {
	data, _ := io.ReadAll(resp.Body)
	return string(data)
}
