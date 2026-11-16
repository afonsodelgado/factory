package phone

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/afonsodelgado/factory/internal/host"
	"github.com/afonsodelgado/factory/internal/review"
)

const testHost = "devbox.tail1234.ts.net"

type fakeOwner struct {
	mu   sync.Mutex
	down bool
	acts []host.ActionRequest
}

func (o *fakeOwner) Call(_ context.Context, m host.Message) (host.Message, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.down {
		return host.Message{}, ErrUnavailable
	}
	switch m.T {
	case "local":
		return done(host.Local{Factories: []host.LocalFactory{{Key: "/repo:csv", ID: "csv", Status: "needs you"}}}), nil
	case "act":
		var r host.ActionRequest
		json.Unmarshal(m.Data, &r)
		o.acts = append(o.acts, r)
		if r.Incarnation != "csv-1" {
			return done(host.Receipt{Operation: r.Operation, Status: host.StatusStale}), nil
		}
		return done(host.Receipt{Operation: r.Operation, Status: host.StatusQueued}), nil
	case "receipt":
		return host.Message{T: "error", Error: host.ErrNoOperation.Error()}, nil
	}
	return host.Message{T: "error", Error: "unexpected " + m.T}, nil
}

type gateway struct {
	t      *testing.T
	server *Server
	owner  *fakeOwner
	http   *httptest.Server
	now    time.Time
}

func newGateway(t *testing.T) *gateway {
	g := &gateway{t: t, owner: &fakeOwner{}, now: time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)}
	g.server = &Server{Store: Open(t.TempDir()), Owner: g.owner, Host: testHost, Version: "test", Now: func() time.Time { return g.now }}
	g.server.refresh(context.Background())
	g.http = httptest.NewServer(g.server.Handler())
	t.Cleanup(g.http.Close)
	return g
}

func (g *gateway) do(method, path, body string, headers ...string) *http.Response {
	g.t.Helper()
	req, _ := http.NewRequest(method, g.http.URL+path, strings.NewReader(body))
	req.Host = testHost
	if method != http.MethodGet {
		req.Header.Set("Origin", "https://"+testHost)
		req.Header.Set("Content-Type", "application/json")
	}
	for i := 0; i+1 < len(headers); i += 2 {
		if headers[i] == "Host" {
			req.Host = headers[i+1]
			continue
		}
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

func (g *gateway) pair() (string, Device) {
	g.t.Helper()
	code, _, err := g.server.Store.NewCode(g.now)
	if err != nil {
		g.t.Fatal(err)
	}
	resp := g.do("POST", "/api/pair/exchange", `{"code":"`+code+`","name":"Pixel"}`)
	var body struct{ Device Device }
	json.NewDecoder(resp.Body).Decode(&body)
	for _, c := range resp.Cookies() {
		if c.Name == CookieName && c.Secure && c.HttpOnly && c.SameSite == http.SameSiteStrictMode && c.Domain == "" {
			return CookieName + "=" + c.Value, body.Device
		}
	}
	g.t.Fatalf("pairing set no Secure, HttpOnly, SameSite=Strict, host-only cookie: %v", resp.Header["Set-Cookie"])
	return "", Device{}
}

func TestOnlyAPairedPhoneFromTheAppsOwnOriginReachesFactoryData(t *testing.T) {
	g := newGateway(t)
	cookie, _ := g.pair()
	other, _ := g.pair()
	op := uuid.NewString()
	act := `{"operation":"` + op + `","incarnation":"csv-1","action":"message","text":"ship it"}`
	actions := "/api/factories/%2Frepo:csv/actions"
	cases := []struct {
		name, method, path, body string
		headers                  []string
		want                     int
	}{
		{"snapshot without a cookie", "GET", "/api/snapshot", "", nil, 401},
		{"stream without a cookie", "GET", "/api/events", "", nil, 401},
		{"me without a cookie", "GET", "/api/me", "", nil, 401},
		{"receipt without a cookie", "GET", "/api/operations/" + op, "", nil, 401},
		{"action without a cookie", "POST", actions, act, nil, 401},
		{"push subscription without a cookie", "POST", "/api/push/subscription", `{}`, nil, 401},
		{"push unsubscribe without a cookie", "DELETE", "/api/push/subscription", `{}`, nil, 401},
		{"a notification link without a cookie", "GET", "/api/links/k1", "", nil, 401},
		{"an unsubscribe that is not JSON", "DELETE", "/api/push/subscription", "", []string{"Cookie", cookie, "Content-Type", ""}, 415},
		{"a subscription to a plain http endpoint", "POST", "/api/push/subscription", `{"endpoint":"http://push.example/x","keys":{"p256dh":"x","auth":"y"}}`, []string{"Cookie", cookie}, 400},
		{"a wrong credential", "GET", "/api/snapshot", "", []string{"Cookie", CookieName + "=forged"}, 401},
		{"two credentials", "GET", "/api/snapshot", "", []string{"Cookie", cookie + "; " + other}, 401},
		{"a credential beside a bearer token", "GET", "/api/snapshot", "", []string{"Cookie", cookie, "Authorization", "Bearer x"}, 401},
		{"an unknown Host", "GET", "/api/snapshot", "", []string{"Cookie", cookie, "Host", "100.101.102.103:7418"}, 421},
		{"a mutation from another origin", "POST", actions, act, []string{"Cookie", cookie, "Origin", "", "Origin", "https://evil.example"}, 403},
		{"a mutation with no origin", "POST", actions, act, []string{"Cookie", cookie, "Origin", ""}, 403},
		{"a mutation over plain http", "POST", actions, act, []string{"Cookie", cookie, "Origin", "", "Origin", "http://" + testHost}, 403},
		{"a mutation that is not JSON", "POST", actions, act, []string{"Cookie", cookie, "Content-Type", "", "Content-Type", "text/plain"}, 415},
		{"pairing from another origin", "POST", "/api/pair/status", `{"code":"x"}`, []string{"Origin", "", "Origin", "https://evil.example"}, 403},
		{"an action on a replaced incarnation", "POST", actions, `{"operation":"` + uuid.NewString() + `","incarnation":"csv-0","action":"message","text":"x"}`, []string{"Cookie", cookie}, 409},
		{"an operation another phone sent", "GET", "/api/operations/" + op, "", []string{"Cookie", other}, 404},
		{"the paired phone's snapshot", "GET", "/api/snapshot", "", []string{"Cookie", cookie}, 200},
		{"the paired phone's action", "POST", actions, act, []string{"Cookie", cookie}, 200},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			resp := g.do(c.method, c.path, c.body, c.headers...)
			if resp.StatusCode != c.want {
				t.Fatalf("status %d; want %d", resp.StatusCode, c.want)
			}
			if strings.HasPrefix(c.path, "/api/") && resp.Header.Get("Cache-Control") != "no-store" && c.want != 421 {
				t.Errorf("Cache-Control %q; want no-store on every API answer", resp.Header.Get("Cache-Control"))
			}
			if resp.Header.Get("Access-Control-Allow-Origin") != "" {
				t.Errorf("CORS header %q; want none", resp.Header.Get("Access-Control-Allow-Origin"))
			}
		})
	}
	if len(g.owner.acts) != 2 || g.owner.acts[1].Device == "" || g.owner.acts[1].Factory != "/repo:csv" {
		t.Fatalf("owner saw %+v; want only the two accepted actions, tagged with the phone and the factory from the path", g.owner.acts)
	}
	g.owner.down = true
	g.server.refresh(context.Background())
	if resp := g.do("GET", "/api/snapshot", "", "Cookie", cookie); resp.StatusCode != 503 || resp.Header.Get("Retry-After") == "" {
		t.Fatalf("snapshot while the runtime changes hands: %d; want a retryable 503", resp.StatusCode)
	}
}

func TestAPairingCodeWorksOnceWithinItsTTL(t *testing.T) {
	g := newGateway(t)
	code, _, _ := g.server.Store.NewCode(g.now)
	if resp := g.do("POST", "/api/pair/status", `{"code":"`+code+`"}`); resp.StatusCode != 200 {
		t.Fatalf("status check %d; want 200 without spending the code", resp.StatusCode)
	}
	var wg sync.WaitGroup
	statuses := make(chan int, 8)
	for range 8 {
		wg.Go(func() {
			statuses <- g.do("POST", "/api/pair/exchange", `{"code":"`+code+`"}`).StatusCode
		})
	}
	wg.Wait()
	close(statuses)
	paired := 0
	for s := range statuses {
		if s == 200 {
			paired++
		} else if s != 410 {
			t.Errorf("a losing exchange answered %d; want 410", s)
		}
	}
	if devices, _ := g.server.Store.Devices(); paired != 1 || len(devices) != 1 {
		t.Fatalf("%d exchanges paired and %d devices exist; want exactly one consumer of the code", paired, len(devices))
	}
	late, _, _ := g.server.Store.NewCode(g.now)
	g.now = g.now.Add(CodeTTL)
	if resp := g.do("POST", "/api/pair/exchange", `{"code":"`+late+`"}`); resp.StatusCode != 410 {
		t.Fatalf("an expired code answered %d; want 410", resp.StatusCode)
	}
}

func TestRevokingAPhoneClosesItsOpenStream(t *testing.T) {
	g := newGateway(t)
	cookie, device := g.pair()
	req, _ := http.NewRequest("GET", g.http.URL+"/api/events", nil)
	req.Host = testHost
	req.Header.Set("Cookie", cookie)
	resp, err := http.DefaultTransport.RoundTrip(req)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("stream: %v %v", err, resp)
	}
	defer resp.Body.Close()
	lines := bufio.NewScanner(resp.Body)
	for lines.Scan() && !strings.HasPrefix(lines.Text(), "data: ") {
	}
	if !strings.Contains(lines.Text(), `"csv"`) {
		t.Fatalf("first event %q; want the snapshot", lines.Text())
	}
	if err := g.server.Store.Revoke(device.ID); err != nil {
		t.Fatal(err)
	}
	g.server.Revoke(device.ID)
	ended := make(chan bool)
	go func() {
		for lines.Scan() {
		}
		ended <- true
	}()
	select {
	case <-ended:
	case <-time.After(5 * time.Second):
		t.Fatal("the revoked phone's stream stayed open")
	}
	if resp := g.do("GET", "/api/snapshot", "", "Cookie", cookie); resp.StatusCode != 401 {
		t.Fatalf("the revoked credential got %d; want 401", resp.StatusCode)
	}
}

func TestReviewPagesNeedThePhoneAndCloseWithItsPairing(t *testing.T) {
	g := newGateway(t)
	cookie, device := g.pair()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "plan.html"), []byte("<body>plan</body>"), 0o644); err != nil {
		t.Fatal(err)
	}
	pages, err := review.Listen(0, "desktop-token", []byte("// overlay"))
	if err != nil {
		t.Fatal(err)
	}
	pages.SetFactories([]review.Factory{{Key: "k", Label: "csv", Dir: dir}})
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

	if resp := g.do("GET", "/r/k/plan.html", ""); resp.StatusCode != 401 || !strings.Contains(body(resp), DatabaseName) {
		t.Fatalf("an unpaired page load answered %d; want the 401 page that clears the phone's saved data", resp.StatusCode)
	}
	if resp := g.do("GET", "/rt/nope/r/k/plan.html", ""); resp.StatusCode != 401 || !strings.Contains(body(resp), "expired") {
		t.Fatalf("a page link nobody granted answered %d; want the expired page", resp.StatusCode)
	}
	opened := g.do("GET", "/r/k/plan.html", "", "Cookie", cookie)
	grantPath := opened.Header.Get("Location")
	if opened.StatusCode != 303 || !strings.HasPrefix(grantPath, "/rt/") || !strings.HasSuffix(grantPath, "/r/k/plan.html") {
		t.Fatalf("the paired phone opening a page answered %d to %q; want a redirect into its sandboxed grant", opened.StatusCode, grantPath)
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
	if resp := g.do("GET", "/api/snapshot", ""); resp.StatusCode != 401 {
		t.Fatalf("the phone API without the cookie answered %d; a grant must not open it", resp.StatusCode)
	}
	if resp := g.do("POST", "/rt/"+token+"/r/k/_api/threads", `{"page":"plan.html","intent":"fix","text":"Bigger."}`, "Origin", "", "Origin", "null"); resp.StatusCode != 200 {
		t.Fatalf("the granted page's comment answered %d", resp.StatusCode)
	}
	if resp := g.do("POST", "/r/k/_api/threads", `{"page":"plan.html","intent":"fix","text":"Bigger."}`, "Cookie", cookie); resp.StatusCode != 405 {
		t.Fatalf("a comment sent straight to the cookie-protected review path answered %d; want it refused", resp.StatusCode)
	}

	req, _ := http.NewRequest("GET", g.http.URL+"/rt/"+token+"/events/review", nil)
	req.Host = testHost
	stream, err := http.DefaultTransport.RoundTrip(req)
	if err != nil || stream.StatusCode != 200 {
		t.Fatalf("review stream: %v %v", err, stream)
	}
	defer stream.Body.Close()
	ended := make(chan bool)
	go func() {
		io.Copy(io.Discard, stream.Body)
		ended <- true
	}()
	g.server.Store.Revoke(device.ID)
	g.server.Revoke(device.ID)
	select {
	case <-ended:
	case <-time.After(5 * time.Second):
		t.Fatal("the revoked phone's review stream stayed open")
	}
	if resp := g.do("GET", "/rt/"+token+"/_access", ""); resp.StatusCode != 401 {
		t.Fatalf("a revoked phone's grant answered %d; want 401", resp.StatusCode)
	}

	again, _ := g.pair()
	pages.Close()
	reopened := strings.Split(g.do("GET", "/r/k/plan.html", "", "Cookie", again).Header.Get("Location"), "/")[2]
	if resp := g.do("GET", "/rt/"+reopened+"/r/k/_api/threads", ""); resp.StatusCode != 503 {
		t.Fatalf("with the runtime handing over, the review API answered %d; want a retryable 503, never the 502 that means factory serve is down", resp.StatusCode)
	}
}

func body(resp *http.Response) string {
	data, _ := io.ReadAll(resp.Body)
	return string(data)
}

func TestThePlainTailnetAddressServesOnlyTheHTTPSHelp(t *testing.T) {
	help := httptest.NewServer(HelpHandler("https://" + testHost))
	defer help.Close()
	for path, want := range map[string]int{"/": 200, "/api/snapshot": 404, "/api/pair/exchange": 404, "/api/events": 404, "/r/x/plan.html": 404} {
		resp, err := http.Get(help.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != want {
			t.Errorf("%s answered %d on the raw tailnet address; want %d", path, resp.StatusCode, want)
		}
	}
}
