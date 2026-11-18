package node

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/afonsodelgado/factory/internal/factory"
	"github.com/afonsodelgado/factory/internal/host"
	"github.com/afonsodelgado/factory/internal/phone"
	"github.com/afonsodelgado/factory/internal/review"
	"github.com/afonsodelgado/factory/internal/state"
	"github.com/afonsodelgado/factory/internal/tmux"
)

var testNow = time.Date(2026, 11, 16, 9, 0, 0, 0, time.UTC)

const factoryKey = "/r/app:csv"

type typist struct {
	mu    sync.Mutex
	typed []string
}

func (t *typist) Type(pane, text string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.typed = append(t.typed, pane+": "+text)
	return nil
}

func (t *typist) all() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return slices.Clone(t.typed)
}

func asking(t *testing.T, typed host.Typist) *host.Runtime {
	t.Helper()
	pane := tmux.Pane{ID: "%1", Command: "claude", Token: "sup"}
	question := state.Attention{ID: "q1", Kind: "question", Questions: []string{"Which region?"}, CreatedAt: state.Time{Time: testNow.Add(-2 * time.Minute)}}
	f := factory.Factory{Repo: "/r/app", Slug: "csv", State: &state.State{SchemaVersion: 2, ID: "csv", Stage: "plan", Attention: []state.Attention{question}}}
	idle := state.HookEvent{Event: "Stop", At: testNow.Add(-time.Minute)}
	r := host.New(host.Runtime{Config: &state.Config{StateDir: t.TempDir()}, TM: tmux.Client{Socket: "factory-test-" + uuid.NewString(), ConfigFile: "/dev/null"}, Typist: typed, Now: func() time.Time { return testNow }})
	r.SetSnapshot(host.Snapshot{At: testNow, Panes: []tmux.Pane{pane}, Items: []host.Item{{Factory: f, Status: factory.NeedsYou, Hook: &state.HookFile{Main: &idle, Last: idle}, Pane: &pane}}})
	return r
}

func serveNode(t *testing.T, r *host.Runtime) (Queue, *Client) {
	t.Helper()
	queue := NewQueue()
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan error, 1)
	go func() { stopped <- r.Run(ctx, queue.Events()) }()
	dir, err := os.MkdirTemp("/tmp", "fnode-")
	if err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(dir, "node.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	api := &API{Commands: Commands{Runtime: queue}, Now: func() time.Time { return testNow }}
	server := &http.Server{Handler: api.With(http.NotFoundHandler(), func(*http.Request) (string, bool) { return "owner", true })}
	go server.Serve(listener)
	go api.Poll(ctx)
	t.Cleanup(func() {
		server.Close()
		cancel()
		if err := <-stopped; err != nil {
			t.Error(err)
		}
		os.RemoveAll(dir)
	})
	return queue, Unix(socket)
}

func answerQ1() ActionBody {
	return ActionBody{Operation: uuid.NewString(), Incarnation: factoryKey, Action: host.ActAnswer, Item: "q1", Since: testNow.Add(-2 * time.Minute), Answers: []string{"eu-west"}}
}

func TestTheHTTPDoorReachesTheSameRuntimeChecksAsTheSocket(t *testing.T) {
	ctx := context.Background()
	actions := "/api/factories/" + url.PathEscape(factoryKey) + "/actions"

	t.Run("an answer over HTTP types exactly what the same answer through the queue types", func(t *testing.T) {
		overHTTP, overQueue := &typist{}, &typist{}
		_, client := serveNode(t, asking(t, overHTTP))
		var receipt host.Receipt
		if err := client.Do(ctx, http.MethodPost, actions, answerQ1(), &receipt); err != nil || receipt.Status == host.StatusStale {
			t.Fatalf("the HTTP answer gave %+v, %v", receipt, err)
		}
		queue, _ := serveNode(t, asking(t, overQueue))
		body := answerQ1()
		data, _ := json.Marshal(body.Request("owner", factoryKey))
		if reply, err := queue.Call(ctx, host.Message{T: "act", Op: body.Operation, Data: data}); err != nil || reply.T != "done" {
			t.Fatalf("the queued answer gave %+v, %v", reply, err)
		}
		if len(overHTTP.all()) != 1 || !slices.Equal(overHTTP.all(), overQueue.all()) {
			t.Fatalf("HTTP typed %q and the queue typed %q; every client must reach the same text and checks", overHTTP.all(), overQueue.all())
		}
	})

	t.Run("the snapshot over HTTP is the runtime's own view of its factories", func(t *testing.T) {
		queue, client := serveNode(t, asking(t, &typist{}))
		reply, err := queue.Call(ctx, host.Message{T: "local"})
		var local host.Local
		if err != nil || json.Unmarshal(reply.Data, &local) != nil || len(local.Factories) != 1 {
			t.Fatalf("local gave %+v, %v", reply, err)
		}
		var snap Snapshot
		for deadline := time.Now().Add(5 * time.Second); client.Do(ctx, http.MethodGet, "/api/snapshot", nil, &snap) != nil; time.Sleep(20 * time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatal("the snapshot never became available over HTTP")
			}
		}
		want, _ := json.Marshal(local.Factories)
		got, _ := json.Marshal(snap.Factories)
		if !bytes.Equal(got, want) {
			t.Fatalf("HTTP snapshot %s; want the runtime's %s", got, want)
		}
	})

	t.Run("an action accepted before a crash answers 409 unknown and is never typed again", func(t *testing.T) {
		typed := &typist{}
		r := asking(t, typed)
		body := answerQ1()
		data, _ := json.Marshal(body.Request("owner", factoryKey))
		accepted, _ := json.Marshal(host.Operation{ID: body.Operation, Action: "act", Data: data, Status: "accepted"})
		if err := host.WriteFile(filepath.Join(r.Config.StateDir, "operations", body.Operation+".json"), string(accepted)); err != nil {
			t.Fatal(err)
		}
		_, client := serveNode(t, r)
		var uncertain struct {
			Status string `json:"status"`
		}
		var failure StatusError
		err := client.Do(ctx, http.MethodPost, actions, body, &uncertain)
		if !errors.As(err, &failure) || failure.Status != http.StatusConflict || uncertain.Status != host.StatusUnknown || len(typed.all()) != 0 {
			t.Fatalf("a retry of an interrupted answer gave %v, status %q, typed %q; want 409 unknown with nothing typed", err, uncertain.Status, typed.all())
		}
	})
}

func TestASupersededCheckNeverPostsItsResultAndNeverHoldsUpAnswers(t *testing.T) {
	queue := NewQueue()
	jobs := &Jobs{Queue: queue, Ctx: context.Background()}
	posted := make(chan string, 4)
	go func() {
		for e := range queue.Events() {
			if e.Reply != nil {
				e.Respond(host.Message{T: "done"})
				continue
			}
			posted <- e.Text
		}
	}()
	release := make(chan struct{})
	jobs.Start("ci", func(context.Context) host.Message {
		<-release
		return host.Message{T: "checked", Text: "stale"}
	})
	if reply, err := queue.Call(context.Background(), host.Message{T: "local"}); err != nil || reply.T != "done" {
		t.Fatalf("a running check held up an answer: %+v, %v", reply, err)
	}
	jobs.Start("ci", func(context.Context) host.Message { return host.Message{T: "checked", Text: "fresh"} })
	close(release)
	select {
	case text := <-posted:
		if text != "fresh" {
			t.Fatalf("the queue received %q first; want only the newer check's result", text)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the newer check never posted its result")
	}
	select {
	case text := <-posted:
		t.Fatalf("the superseded check posted %q", text)
	case <-time.After(200 * time.Millisecond):
	}
}

func funnelOff(t *testing.T) *Exposure {
	t.Helper()
	e := &Exposure{Tailscale: Tailscale{Run: func(context.Context, string, ...string) ([]byte, error) { return []byte("{}"), nil }}}
	e.Check(context.Background())
	return e
}

func TestOnlyTheOwnersLoginThroughLocalServeReachesTheAPI(t *testing.T) {
	gate := Gate{Owner: "alice@example.com", Hosts: []string{"devbox.tail1234.ts.net"}, Origin: "https://devbox.tail1234.ts.net", OpaquePath: "/rt/", Exposure: funnelOff(t)}
	reached := false
	handler := gate.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached = true }))
	cases := []struct {
		name   string
		peer   string
		logins []string
		method string
		path   string
		origin string
		status int
	}{
		{"the owner through Serve", "127.0.0.1:50000", []string{"alice@example.com"}, http.MethodGet, "/api/snapshot", "", http.StatusOK},
		{"the owner's encoded login", "[::1]:50000", []string{"=?utf-8?q?alice=40example.com?="}, http.MethodGet, "/api/snapshot", "", http.StatusOK},
		{"no login header", "127.0.0.1:50000", nil, http.MethodGet, "/api/snapshot", "", http.StatusUnauthorized},
		{"two login headers", "127.0.0.1:50000", []string{"alice@example.com", "alice@example.com"}, http.MethodGet, "/api/snapshot", "", http.StatusUnauthorized},
		{"a malformed login", "127.0.0.1:50000", []string{"=?utf-8?q?alice=ZZ?="}, http.MethodGet, "/api/snapshot", "", http.StatusUnauthorized},
		{"another tailnet user", "127.0.0.1:50000", []string{"mallory@example.com"}, http.MethodGet, "/api/snapshot", "", http.StatusForbidden},
		{"the owner's headers from the network", "100.101.102.103:50000", []string{"alice@example.com"}, http.MethodGet, "/api/snapshot", "", http.StatusForbidden},
		{"a mutation from another origin", "127.0.0.1:50000", []string{"alice@example.com"}, http.MethodPost, "/api/commands/wake", "https://evil.example", http.StatusForbidden},
		{"an opaque origin outside the review grant", "127.0.0.1:50000", []string{"alice@example.com"}, http.MethodPost, "/api/commands/wake", "null", http.StatusForbidden},
		{"an opaque origin on the review grant", "127.0.0.1:50000", []string{"alice@example.com"}, http.MethodPost, "/rt/grant/r/k/_api/threads", "null", http.StatusOK},
		{"a native client without an origin", "127.0.0.1:50000", []string{"alice@example.com"}, http.MethodPost, "/api/commands/wake", "", http.StatusOK},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			reached = false
			r := httptest.NewRequest(c.method, "https://devbox.tail1234.ts.net"+c.path, strings.NewReader("{}"))
			r.RemoteAddr = c.peer
			r.Header.Set("Content-Type", "application/json")
			for _, login := range c.logins {
				r.Header.Add(LoginHeader, login)
			}
			if c.origin != "" {
				r.Header.Set("Origin", c.origin)
			}
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != c.status || reached != (c.status == http.StatusOK) {
				t.Fatalf("got %d (handler reached %v); want %d", w.Code, reached, c.status)
			}
		})
	}
}

func TestAStreamOpenedBeforeFunnelTurnsOnIsCutOff(t *testing.T) {
	serve := []byte("{}")
	exposure := &Exposure{Tailscale: Tailscale{Run: func(context.Context, string, ...string) ([]byte, error) { return serve, nil }}}
	exposure.Check(context.Background())
	gate := Gate{Owner: "alice@example.com", Hosts: []string{"devbox.tail1234.ts.net"}, Exposure: exposure}
	streaming := make(chan struct{})
	ended := make(chan struct{})
	handler := gate.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(streaming)
		<-r.Context().Done()
		close(ended)
	}))
	r := httptest.NewRequest(http.MethodGet, "https://devbox.tail1234.ts.net/api/events", nil)
	r.RemoteAddr = "127.0.0.1:50000"
	r.Header.Set(LoginHeader, "alice@example.com")
	go handler.ServeHTTP(httptest.NewRecorder(), r)
	<-streaming
	serve = []byte(`{"AllowFunnel":{"devbox.tail1234.ts.net:443":true}}`)
	exposure.Check(context.Background())
	select {
	case <-ended:
	case <-time.After(2 * time.Second):
		t.Fatal("a stream opened while Funnel was off kept sending after Funnel turned on")
	}
}

func TestOpeningTheAppWhileFunnelIsOnShowsAPageWhileTheAPIStillNamesTheFix(t *testing.T) {
	exposure := &Exposure{Tailscale: Tailscale{Run: func(context.Context, string, ...string) ([]byte, error) {
		return []byte(`{"AllowFunnel":{"devbox.tail1234.ts.net:443":true}}`), nil
	}}}
	exposure.Check(context.Background())
	gate := Gate{Owner: "alice@example.com", Hosts: []string{"devbox.tail1234.ts.net"}, ExposedPage: phone.ExposedPage("devbox"), Exposure: exposure}
	handler := gate.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("a request reached the node while Funnel was on")
	}))
	open := func(path string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "https://devbox.tail1234.ts.net"+path, nil)
		r.RemoteAddr = "127.0.0.1:50000"
		r.Header.Set(LoginHeader, "alice@example.com")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	page := open("/")
	if page.Code != http.StatusForbidden || !strings.HasPrefix(page.Header().Get("Content-Type"), "text/html") || !strings.Contains(page.Body.String(), "Funnel is on for devbox") || !strings.Contains(page.Body.String(), "tailscale funnel") {
		t.Fatalf("opening the app got %d %s %q; want an HTML page naming the fix", page.Code, page.Header().Get("Content-Type"), page.Body.String())
	}
	api := open("/api/me")
	if api.Code != http.StatusForbidden || !strings.Contains(api.Body.String(), `"funnel":true`) {
		t.Fatalf("the API got %d %q; want JSON with funnel true for the installed app", api.Code, api.Body.String())
	}
}

func TestTheTailnetListenerServesThePhoneAppToItsOwnerWithoutPairing(t *testing.T) {
	const host = "devbox.tail1234.ts.net"
	api := &API{Node: "node-1", Version: "test"}
	app := (&phone.Server{Store: phone.Open(t.TempDir())}).Handler()
	gate := Gate{Owner: "alice@example.com", Hosts: []string{host}, Origin: "https://" + host, OpaquePath: phone.GrantPrefix, RefusalPage: phone.RefusalPage("devbox"), Exposure: funnelOff(t)}
	handler := gate.Wrap(api.With(app, gate.Principal, NetworkKinds...))
	cases := []struct {
		name, method, path, login, origin, media string
		headers                                  []string
		status                                   int
		shows                                    string
	}{
		{"the owner's identity", "GET", "/api/me", "alice@example.com", "", "", nil, 200, `"node":"node-1"`},
		{"a paired phone's old cookie without a login", "GET", "/api/me", "", "", "", []string{"Cookie", phone.DatabaseName + "=x; factory_device=secret"}, 401, ""},
		{"the old pairing status route", "POST", "/api/pair/status", "alice@example.com", "https://" + host, "application/json", nil, 404, ""},
		{"the old pairing exchange route", "POST", "/api/pair/exchange", "alice@example.com", "https://" + host, "application/json", nil, 404, ""},
		{"another account's API call", "GET", "/api/snapshot", "mallory@example.com", "", "", nil, 403, `"owner":"alice@example.com"`},
		{"another account opening the app", "GET", "/", "mallory@example.com", "", "", nil, 403, "devbox answers alice@example.com only"},
		{"a text/plain approval through a review grant", "POST", "/rt/grant/r/k/_api/approve", "alice@example.com", "null", "text/plain", nil, 415, ""},
		{"an approval from another origin through a review grant", "POST", "/rt/grant/r/k/_api/approve", "alice@example.com", "https://evil.example", "application/json", nil, 403, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest(c.method, "https://"+host+c.path, strings.NewReader("{}"))
			r.RemoteAddr = "127.0.0.1:50000"
			for header, value := range map[string]string{LoginHeader: c.login, "Origin": c.origin, "Content-Type": c.media} {
				if value != "" {
					r.Header.Set(header, value)
				}
			}
			for i := 0; i+1 < len(c.headers); i += 2 {
				r.Header.Set(c.headers[i], c.headers[i+1])
			}
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != c.status || !strings.Contains(w.Body.String(), c.shows) {
				t.Fatalf("got %d %q; want %d showing %q", w.Code, w.Body.String(), c.status, c.shows)
			}
		})
	}
}

func TestAnOpenAppStreamMarksOnlyItsInstallationConnected(t *testing.T) {
	api := &API{}
	server := httptest.NewServer(api.With(http.NotFoundHandler(), func(*http.Request) (string, bool) { return "owner", true }))
	defer server.Close()
	resp, err := http.Get(server.URL + "/api/events?installation=pixel")
	if err != nil {
		t.Fatal(err)
	}
	lines := bufio.NewScanner(resp.Body)
	for lines.Scan() && !strings.HasPrefix(lines.Text(), "event: ") {
	}
	if !api.Connected("pixel") || api.Connected("ipad") || api.Connected("") {
		t.Fatal("the open stream did not mark exactly its own installation connected, so resolutions would be pushed to an open app or withheld from a closed one")
	}
	resp.Body.Close()
	deadline := time.Now().Add(5 * time.Second)
	for api.Connected("pixel") {
		if time.Now().After(deadline) {
			t.Fatal("the closed app still counts as connected, so its phone would miss the resolved notification")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestTheNodeSocketIsOwnerOnlyAndNeverReplacesSomeoneElsesFile(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "fnode-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	os.Chmod(dir, 0o755)
	socket := filepath.Join(dir, "node.sock")
	target := filepath.Join(dir, "elsewhere")
	os.WriteFile(target, []byte("keep"), 0o644)
	if err := os.Symlink(target, socket); err != nil {
		t.Fatal(err)
	}
	if _, err := ListenUnix(socket); err == nil {
		t.Fatal("listened through a symlink at the socket path")
	}
	os.Remove(socket)
	os.WriteFile(socket, []byte("not a socket"), 0o600)
	if _, err := ListenUnix(socket); err == nil {
		t.Fatal("replaced a regular file at the socket path")
	}
	if data, _ := os.ReadFile(target); string(data) != "keep" {
		t.Fatal("the symlink's target was changed")
	}
	os.Remove(socket)
	ln, err := ListenUnix(socket)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	dirInfo, _ := os.Stat(dir)
	sockInfo, _ := os.Lstat(socket)
	if dirInfo.Mode().Perm() != 0o700 || sockInfo.Mode().Perm() != 0o600 {
		t.Fatalf("state dir %v, socket %v; want 0700 and 0600 so no other local user reaches the owner principal", dirInfo.Mode().Perm(), sockInfo.Mode().Perm())
	}
}

func TestASecondNodeOrAnOldOwnerProcessIsRefusedWithoutTakingOver(t *testing.T) {
	dir := t.TempDir()
	nodeLock, oldHub := filepath.Join(dir, "node.lock"), filepath.Join(dir, "hub.lock")
	release, err := Lock(nodeLock, oldHub)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Lock(nodeLock, oldHub); !errors.Is(err, ErrRunning) {
		t.Fatalf("a second node got %v; want ErrRunning", err)
	}
	release()
	held, err := flock(oldHub)
	if err != nil || held == nil {
		t.Fatal(err)
	}
	defer held()
	var old OldOwnerError
	if _, err := Lock(nodeLock, oldHub); !errors.As(err, &old) || !strings.Contains(err.Error(), "never stops them") {
		t.Fatalf("a node beside an old hub got %v; want a refusal naming the old process to stop", err)
	}
	if again, err := flock(nodeLock); err != nil || again == nil {
		t.Error("the refused node kept its lock, so a later node could never start")
	} else {
		again()
	}
}

type recordingCaller struct{ calls []string }

func (c *recordingCaller) Call(_ context.Context, m host.Message) (host.Message, error) {
	c.calls = append(c.calls, m.T)
	return host.Message{T: "done"}, nil
}

type unreachableHost struct{}

func (unreachableHost) Call(context.Context, host.Message) (host.Message, error) {
	return host.Message{}, errors.New("host is down")
}

func TestTheAlreadyRunsCheckKeepsRefusingWhenTheHostCannotBeReached(t *testing.T) {
	api := &API{Commands: Commands{Runtime: unreachableHost{}}}
	if !api.HoldsIncarnation("inc-1") {
		t.Error("with no snapshot and no host the node answered that it does not hold the factory; a move would then skip the already-runs check")
	}
	api.publish([]host.LocalFactory{{Incarnation: "inc-1"}})
	if !api.HoldsIncarnation("inc-1") || api.HoldsIncarnation("inc-2") {
		t.Error("with the host down the node must answer from its last snapshot: refuse inc-1, which it holds, and not inc-2")
	}
}

func TestAReviewPageScriptWithALiveGrantCannotReachTheControlAPI(t *testing.T) {
	const name = "devbox.tail1234.ts.net"
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "plan.html"), []byte("<body>plan</body>"), 0o644); err != nil {
		t.Fatal(err)
	}
	pages, err := review.New("token", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pages.Close() })
	pages.SetFactories([]review.Factory{{Key: "k", Label: "csv", Dir: dir}})
	runtime := &recordingCaller{}
	api := &API{Commands: Commands{Runtime: runtime}, Node: "node-1", Version: "test"}
	app := (&phone.Server{Store: phone.Open(t.TempDir()), Review: pages.MobileHandler()}).Handler()
	gate := Gate{Owner: "alice@example.com", Hosts: []string{name}, Origin: "https://" + name, OpaquePath: phone.GrantPrefix, Exposure: funnelOff(t)}
	handler := gate.Wrap(api.With(app, gate.Principal, NetworkKinds...))
	request := func(method, path, origin string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "https://"+name+path, strings.NewReader(`{"key":"/r/app:csv"}`))
		r.RemoteAddr = "127.0.0.1:50000"
		r.Header.Set(LoginHeader, "alice@example.com")
		r.Header.Set("Content-Type", "application/json")
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	grant := request("GET", "/r/k/plan.html", "").Header().Get("Location")
	scope, granted := strings.CutSuffix(grant, "/r/k/plan.html")
	if !strings.HasPrefix(grant, phone.GrantPrefix) || !granted {
		t.Fatalf("opening the page granted %q; want a scoped review path", grant)
	}
	comment := `{"page":"plan.html","intent":"fix","text":"Bigger.","anchor":{"selector":"h1"}}`
	r := httptest.NewRequest("POST", "https://"+name+scope+"/r/k/_api/threads", strings.NewReader(comment))
	r.RemoteAddr = "127.0.0.1:50000"
	r.Header.Set(LoginHeader, "alice@example.com")
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", "null")
	commented := httptest.NewRecorder()
	if handler.ServeHTTP(commented, r); commented.Code != http.StatusOK {
		t.Fatalf("the page's own comment through its grant answered %d %s; want 200", commented.Code, commented.Body)
	}
	for what, path := range map[string]string{
		"the control route directly":        "/api/commands/wake",
		"the control route under the grant": scope + "/api/commands/wake",
		"dot segments out of the grant":     scope + "/../../api/commands/wake",
		"a factory action out of the grant": scope + "/../../api/factories/k/actions",
		"dot segments inside the grant":     scope + "/r/k/../../api/commands/wake",
	} {
		if w := request("POST", path, "null"); w.Code < 300 {
			t.Errorf("%s answered %d to the page's opaque origin", what, w.Code)
		}
	}
	if snapshot := request("GET", "/api/snapshot", "null"); snapshot.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Errorf("the snapshot let the page's opaque origin %q read it", snapshot.Header().Get("Access-Control-Allow-Origin"))
	}
	if len(runtime.calls) != 0 {
		t.Fatalf("a review page script ran %v on the node", runtime.calls)
	}
}

func TestAnOwnerlessNodeOpensOnlyHubLinkedReviewPagesToThisMachinesBrowser(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "plan.html"), []byte("<body>plan</body>"), 0o644); err != nil {
		t.Fatal(err)
	}
	pages, err := review.New("token", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pages.Close() })
	pages.SetFactories([]review.Factory{{Key: "k", Label: "csv", Dir: dir}})
	door := &LocalDoor{Port: 7418, Review: pages.MobileHandler()}
	const here = "127.0.0.1:7418"
	get := func(host, path string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "http://"+host+path, nil)
		r.RemoteAddr = "127.0.0.1:50000"
		for _, c := range cookies {
			r.AddCookie(c)
		}
		w := httptest.NewRecorder()
		door.ServeHTTP(w, r)
		return w
	}
	link, err := door.Link("/r/k/plan.html")
	if err != nil || !strings.HasPrefix(link, "http://"+here+"/open/") {
		t.Fatalf("the hub got the link %q, %v; want a one-use loopback link", link, err)
	}
	ticket := strings.TrimPrefix(link, "http://"+here)
	entered := get(here, ticket)
	cookies := entered.Result().Cookies()
	if entered.Code != http.StatusSeeOther || entered.Header().Get("Location") != "/r/k/plan.html" || len(cookies) != 1 || !cookies[0].HttpOnly {
		t.Fatalf("the link answered %d to %q with cookies %v; want an HttpOnly session and the page", entered.Code, entered.Header().Get("Location"), cookies)
	}
	grant := get(here, "/r/k/plan.html", cookies...).Header().Get("Location")
	if opened := get(here, grant); opened.Code != http.StatusOK || !strings.Contains(opened.Body.String(), "data-mobile") {
		t.Fatalf("the page through its link answered %d %q; want it with the comment overlay", opened.Code, opened.Body)
	}
	for _, refused := range []struct {
		name string
		got  *httptest.ResponseRecorder
	}{
		{"a reused link", get(here, ticket)},
		{"the page without the cookie", get(here, "/r/k/plan.html")},
		{"the page under another Host", get("macbook.tail1234.ts.net", "/r/k/plan.html", cookies...)},
		{"the snapshot", get(here, "/api/snapshot", cookies...)},
		{"another node's hello", get(here, "/api/node/hello", cookies...)},
		{"the phone app", get(here, "/", cookies...)},
	} {
		if refused.got.Code < 400 {
			t.Errorf("%s answered %d; an ownerless node must refuse it", refused.name, refused.got.Code)
		}
	}
}

func TestKeepAwakeHoldsOnlyWhileFactoriesAreActiveOnExternalPowerAndReleasesOnShutdown(t *testing.T) {
	onPower, holds, releases := true, 0, 0
	a := &Awake{
		Enabled: true,
		OnPower: func(context.Context) (bool, error) { return onPower, nil },
		Hold: func() (Assertion, error) {
			holds++
			return Assertion{Release: func() { releases++ }, Ended: make(chan struct{})}, nil
		},
	}
	running, waiting := host.LocalFactory{Status: "running"}, host.LocalFactory{Status: "needs you"}
	paused, done := host.LocalFactory{Status: "running", Paused: true}, host.LocalFactory{Status: "done"}
	for _, step := range []struct {
		name      string
		power     bool
		factories []host.LocalFactory
		held      bool
	}{
		{"one factory waiting for the developer, on power", true, []host.LocalFactory{waiting}, true},
		{"the same, polled again", true, []host.LocalFactory{waiting}, true},
		{"on battery", false, []host.LocalFactory{running, waiting}, false},
		{"back on power", true, []host.LocalFactory{running, waiting}, true},
		{"only paused and done factories", true, []host.LocalFactory{paused, done}, false},
		{"a factory active again", true, []host.LocalFactory{running, paused}, true},
	} {
		onPower = step.power
		a.Set(context.Background(), ActiveFactories(step.factories))
		if held := holds > releases; held != step.held {
			t.Fatalf("%s: holding %v (%s); want %v", step.name, held, a.Status().Detail, step.held)
		}
	}
	if holds != 3 {
		t.Errorf("took the sleep assertion %d times over three holding spells; want one per spell, not one per poll", holds)
	}
	ctx, shutdown := context.WithCancel(context.Background())
	shutdown()
	a.Run(ctx, func() int { return 1 })
	if holds != releases {
		t.Error("the node stopped and left its sleep assertion held")
	}
	off := &Awake{OnPower: a.OnPower, Hold: a.Hold}
	if off.Set(context.Background(), 2); holds != 3 {
		t.Error("node.keep_awake: false still took the sleep assertion")
	}
}

func TestKeepAwakeIsNotApplicableOnLinuxNodes(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("this checks the Linux node; macOS holds the assertion")
	}
	a := NewAwake(true)
	a.Set(context.Background(), 3)
	if a.held != nil || a.Status().Detail != "not applicable on Linux nodes" {
		t.Fatalf("a Linux node with active factories reports %q; want keep-awake not applied", a.Status().Detail)
	}
}

func TestAHungPowerProbeCannotBlockNodeIdentityAndTimesOut(t *testing.T) {
	started := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a := &Awake{Enabled: true, OnPower: func(ctx context.Context) (bool, error) {
		close(started)
		<-ctx.Done()
		return false, ctx.Err()
	}}
	finished := make(chan struct{})
	go func() { a.Set(ctx, 1); close(finished) }()
	<-started
	answer := make(chan struct{})
	go func() {
		api := &API{Awake: a}
		api.me(httptest.NewRecorder(), httptest.NewRequest("GET", "/api/me", nil), "owner")
		close(answer)
	}()
	select {
	case <-answer:
	case <-time.After(200 * time.Millisecond):
		t.Error("node identity waits for the power probe")
	}
	select {
	case <-finished:
		if !a.Status().Problem {
			t.Error("a timed-out power probe must report a problem")
		}
	case <-time.After(3 * time.Second):
		t.Error("power probe has no bounded timeout")
	}
}
