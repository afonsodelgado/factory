package phone

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/afonsodelgado/factory/internal/factory"
	"github.com/afonsodelgado/factory/internal/host"
	"github.com/afonsodelgado/factory/internal/state"
	"github.com/afonsodelgado/factory/internal/tmux"
)

type vendorCall struct {
	path, topic, urgency, ttl, auth string
}

type vendor struct {
	*httptest.Server
	mu      sync.Mutex
	calls   []vendorCall
	answers map[string][]int
}

func newVendor(t *testing.T) *vendor {
	v := &vendor{answers: map[string][]int{}}
	v.Server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		v.mu.Lock()
		defer v.mu.Unlock()
		v.calls = append(v.calls, vendorCall{r.URL.Path, r.Header.Get("Topic"), r.Header.Get("Urgency"), r.Header.Get("TTL"), r.Header.Get("Authorization")})
		status := http.StatusCreated
		if queued := v.answers[r.URL.Path]; len(queued) > 0 {
			status, v.answers[r.URL.Path] = queued[0], queued[1:]
			w.Header().Set("Retry-After", "60")
		}
		w.WriteHeader(status)
	}))
	t.Cleanup(v.Close)
	return v
}

func (v *vendor) took() []vendorCall {
	v.mu.Lock()
	defer v.mu.Unlock()
	calls := v.calls
	v.calls = nil
	return calls
}

const testNode = "3d4c1a2b-node"

func subscriptionFor(t *testing.T, installation, endpoint string) Subscription {
	key, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	auth := make([]byte, 16)
	rand.Read(auth)
	return Subscription{Installation: installation, Endpoint: endpoint, P256dh: base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes()), Auth: base64.RawURLEncoding.EncodeToString(auth)}
}

func factoryWith(paused bool, keys ...string) []host.LocalFactory {
	f := host.LocalFactory{Key: "/Users/me/secret-repo:csv", Route: "secret-repo-1a2b-csv", Incarnation: "csv-1", ID: "csv", Paused: paused}
	for _, key := range keys {
		f.Items = append(f.Items, host.LocalItem{Key: key, AttentionItem: host.AttentionItem{ID: "q-" + key, Kind: "question", Title: "Which secret-token goes in?", Body: "/Users/me/secret-repo/plan.md"}})
	}
	return []host.LocalFactory{f}
}

func TestNotificationsReachEachPhoneOnceAndFollowTheItemsLifecycle(t *testing.T) {
	ctx := context.Background()
	store := Open(t.TempDir())
	if _, err := store.VAPIDKey(""); err != nil {
		t.Fatal(err)
	}
	v := newVendor(t)
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	pusher := &Pusher{Store: store, Origin: "https://" + testHost, Client: v.Client(), Now: func() time.Time { return now }}
	offline := func(string) bool { return false }
	reconcile := func(factories []host.LocalFactory, connected func(string) bool) bool {
		t.Helper()
		queued, err := store.Reconcile(testNode, factories, connected, now)
		if err != nil {
			t.Fatal(err)
		}
		return queued
	}

	if reconcile(factoryWith(false, "old"), offline) {
		t.Fatal("enabling Phone notified about an item that was already open")
	}
	for _, name := range []string{"a", "b"} {
		if _, err := store.Subscribe(subscriptionFor(t, name, v.URL+"/"+name), ""); err != nil {
			t.Fatal(err)
		}
	}

	reconcile(factoryWith(false, "old", "k1"), offline)
	var l ledger
	store.read(pushFile, &l)
	for _, j := range l.Jobs {
		var fields map[string]any
		json.Unmarshal([]byte(j.Payload), &fields)
		for name := range fields {
			if !strings.Contains("node tag title body url revision resolved", name) {
				t.Errorf("payload field %q leaves the machine", name)
			}
		}
		if strings.Contains(j.Payload, "secret") {
			t.Errorf("payload %s carries the question, a path or the repo", j.Payload)
		}
		if fields["node"] != testNode || fields["url"] != "/n/"+testNode+"/k1" || !strings.HasPrefix(fields["tag"].(string), testNode+"/") {
			t.Errorf("payload %s; want the sending node in its tag and in a link the app opens without its home", j.Payload)
		}
	}
	pusher.Deliver(ctx)
	calls := v.took()
	if len(calls) != 2 || calls[0].topic != "k1" || calls[0].urgency != "high" || calls[0].ttl == "" {
		t.Fatalf("vendor saw %+v; want one high-urgency push per phone, tagged with the item", calls)
	}
	if sub := subjectOf(calls[0].auth); sub != "https://"+testHost {
		t.Errorf("VAPID subject %q; want the app's HTTPS origin", sub)
	}

	if queued, _ := Open(store.Dir).Reconcile(testNode, factoryWith(false, "old", "k1"), offline, now); queued {
		t.Fatal("a restarted node notified again about an item it already pushed")
	}

	v.answers["/a"] = []int{http.StatusServiceUnavailable}
	v.answers["/b"] = []int{http.StatusGone}
	reconcile(factoryWith(false, "old", "k1", "k2"), offline)
	pusher.Deliver(ctx)
	pusher.Deliver(ctx)
	if calls := v.took(); len(calls) != 2 {
		t.Fatalf("vendor saw %d pushes; want one each, with the failed one held for its Retry-After", len(calls))
	}
	if subs, _ := store.Subscriptions(); len(subs) != 1 || subs[0].Installation != "a" {
		t.Fatalf("subscriptions %+v; want phone b dropped after its push service answered 410", subs)
	}
	now = now.Add(61 * time.Second)
	pusher.Deliver(ctx)
	if calls := v.took(); len(calls) != 1 || calls[0].path != "/a" {
		t.Fatalf("after Retry-After the vendor saw %+v; want the held push to phone a", calls)
	}

	reconcile(factoryWith(false, "old", "k2"), offline)
	store.read(pushFile, &l)
	if len(l.Jobs) != 1 || !l.Jobs[0].Resolved || !strings.Contains(l.Jobs[0].Payload, `"revision":2`) {
		t.Fatalf("jobs after k1 was answered on the computer: %+v; want one resolved replacement with a newer revision", l.Jobs)
	}
	pusher.Deliver(ctx)
	if calls := v.took(); len(calls) != 1 || calls[0].topic != "k1" {
		t.Fatalf("vendor saw %+v; want the replacement under k1's tag", calls)
	}
	if reconcile(factoryWith(false, "old"), func(installation string) bool { return installation == "a" }) {
		t.Fatal("a resolution was pushed to a phone whose open app clears it itself")
	}

	if reconcile(factoryWith(true, "old", "k3"), offline) || reconcile(factoryWith(false, "old", "k3"), offline) {
		t.Fatal("a paused factory's item was pushed, either while paused or on wake")
	}
	reconcile(factoryWith(false, "old", "k3", "k4"), offline)
	if reconcile(nil, offline) {
		t.Fatal("archiving the factory pushed a resolution")
	}
	store.read(pushFile, &l)
	if len(l.Jobs) != 0 {
		t.Fatalf("jobs %+v; want k4's unsent push cancelled when its factory was archived", l.Jobs)
	}

	reconcile(factoryWith(false, "k5"), offline)
	if err := store.Unsubscribe(v.URL+"/a", now); err != nil {
		t.Fatal(err)
	}
	pusher.Deliver(ctx)
	if calls := v.took(); len(calls) != 0 {
		t.Fatalf("a phone that turned notifications off was still pushed: %+v", calls)
	}

	if _, err := PublicClient().Get(v.URL); err == nil || !strings.Contains(err.Error(), "not public") {
		t.Fatalf("the production client reached a loopback push endpoint: %v", err)
	}
}

func TestAPhoneReinstalledFromTheNewHomeRetiresItsOldCopysSubscription(t *testing.T) {
	store := Open(t.TempDir())
	if _, err := store.VAPIDKey(""); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Subscribe(subscriptionFor(t, "old-copy", "https://push.example/old"), ""); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Subscribe(subscriptionFor(t, "new-copy", "https://push.example/new"), "old-copy"); err != nil {
		t.Fatal(err)
	}
	live, err := store.Subscriptions()
	if err != nil {
		t.Fatal(err)
	}
	if len(live) != 1 || live[0].Installation != "new-copy" {
		t.Fatalf("live subscriptions %+v; want only the new copy, or the phone gets every notification twice", live)
	}
}

func TestAStaleFailureFromAnyNodeNeverRemovesARenewedSubscription(t *testing.T) {
	store := Open(t.TempDir())
	if _, err := store.VAPIDKey(""); err != nil {
		t.Fatal(err)
	}
	v := newVendor(t)
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	pusher := &Pusher{Store: store, Origin: "https://" + testHost, Client: v.Client(), Now: func() time.Time { return now }}
	offline := func(string) bool { return false }
	if _, err := store.Subscribe(subscriptionFor(t, "a", v.URL+"/old"), ""); err != nil {
		t.Fatal(err)
	}
	store.Reconcile(testNode, factoryWith(false, "old"), offline, now)
	store.Reconcile(testNode, factoryWith(false, "old", "k1"), offline, now)
	if _, err := store.Subscribe(subscriptionFor(t, "a", v.URL+"/new"), ""); err != nil {
		t.Fatal(err)
	}

	pusher.Deliver(context.Background())
	store.settle(job{ID: "in-flight", Endpoint: v.URL + "/old"}, http.StatusGone, 0, "", now)
	if sub, ok := store.SubscriptionOf("a"); !ok || sub.Endpoint != v.URL+"/new" {
		t.Fatalf("subscription %+v after the browser's old endpoint failed; want the renewed one kept, or the phone stays silent until the app is opened", sub)
	}
	if calls := v.took(); len(calls) != 0 {
		t.Fatalf("vendor saw %+v; want nothing sent to the replaced endpoint", calls)
	}

	stale, _ := store.SubscriptionOf("a")
	store.Reconcile(testNode, factoryWith(false, "old", "k1", "k2"), offline, now)
	if _, err := store.Subscribe(subscriptionFor(t, "a", v.URL+"/new"), ""); err != nil {
		t.Fatal(err)
	}
	v.answers["/new"] = []int{http.StatusGone}
	pusher.Deliver(context.Background())
	keys, _ := store.KeyState()
	replicated := Replica{Key: keys.Fingerprint, Records: []Subscription{{Endpoint: stale.Endpoint, Revision: stale.Revision + 1, Removed: true}}}
	if err := store.Merge(replicated, now); err != nil {
		t.Fatal(err)
	}
	if renewed, ok := store.SubscriptionOf("a"); !ok || renewed.Revision == stale.Revision {
		t.Fatalf("subscription %+v after a push queued before the browser renewed its keys failed here and on another node; want the renewal kept", renewed)
	}
	v.took()
	now = now.Add(sendingLease + time.Minute)
	pusher.Deliver(context.Background())
	if calls := v.took(); len(calls) != 0 {
		t.Fatalf("the push service saw %+v after a stale failure; want the failed push dropped, not sent again", calls)
	}
}

func TestAHandoffAsksOnceWhetherTheWorkIsDoneEvenAsItsSupervisorExits(t *testing.T) {
	ctx := context.Background()
	store := Open(t.TempDir())
	if _, err := store.VAPIDKey(""); err != nil {
		t.Fatal(err)
	}
	v := newVendor(t)
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	pusher := &Pusher{Store: store, Origin: "https://" + testHost, Client: v.Client(), Now: func() time.Time { return now }}
	if _, err := store.Subscribe(subscriptionFor(t, "a", v.URL+"/a"), ""); err != nil {
		t.Fatal(err)
	}
	handoff := state.Attention{ID: "handoff-csv-1", Kind: "handoff", Title: "Work done: mark it done or continue working?", StageKey: "handoff", CreatedAt: state.Time{Time: now}}
	finished := func(pane *tmux.Pane, attention ...state.Attention) []host.LocalFactory {
		st := &state.State{SchemaVersion: 2, ID: "csv", Stage: "handoff", Stages: map[string]state.Stage{"handoff": {Status: "running"}}, WaitingOn: "user", Attention: attention}
		r := host.New(host.Runtime{Config: &state.Config{StateDir: t.TempDir()}})
		r.SetSnapshot(host.Snapshot{At: now, Items: []host.Item{{Factory: factory.Factory{Repo: "/r/app", Slug: "csv", State: st}, Status: factory.NeedsYou, Pane: pane}}})
		return r.Local().Factories
	}
	offline := func(string) bool { return false }
	supervisor := &tmux.Pane{ID: "%1", Command: "claude"}
	store.Reconcile(testNode, finished(supervisor), offline, now)
	var bodies []string
	for _, pane := range []*tmux.Pane{supervisor, {ID: "%1", Command: "zsh"}, nil} {
		factories := finished(pane, handoff)
		if len(factories[0].Items) != 1 {
			t.Fatalf("a finished factory whose supervisor pane is %+v lists %+v; want its handoff until the developer answers it", pane, factories[0].Items)
		}
		store.Reconcile(testNode, factories, offline, now)
		var l ledger
		store.read(pushFile, &l)
		for _, j := range l.Jobs {
			var p Payload
			json.Unmarshal([]byte(j.Payload), &p)
			bodies = append(bodies, p.Body)
		}
		pusher.Deliver(ctx)
	}
	if calls := v.took(); len(calls) != 1 || len(bodies) != 1 || bodies[0] != workDone {
		t.Fatalf("the handoff reached the push service %d times saying %q; want once, asking whether the work is done, and never again for its supervisor exiting", len(calls), bodies)
	}
}

func TestAGateIsPushedAgainOnceTheDesignerHasWorkedOnTheComments(t *testing.T) {
	ctx := context.Background()
	store := Open(t.TempDir())
	if _, err := store.VAPIDKey(""); err != nil {
		t.Fatal(err)
	}
	v := newVendor(t)
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	pusher := &Pusher{Store: store, Origin: "https://" + testHost, Client: v.Client(), Now: func() time.Time { return now }}
	gate := state.Attention{ID: "gate-csv-design", Kind: "gate", StageKey: "design", Title: "Approve the design", CreatedAt: state.Time{Time: now}}
	gated := func(revising map[string]string) []host.LocalFactory {
		st := &state.State{ID: "csv", Stage: "design", WaitingOn: "user", Attention: []state.Attention{gate}}
		r := host.New(host.Runtime{Config: &state.Config{StateDir: t.TempDir()}})
		r.SetSnapshot(host.Snapshot{At: now, Items: []host.Item{{Factory: factory.Factory{Repo: "/r/app", Slug: "csv", State: st}, Pane: &tmux.Pane{ID: "%1", Command: "claude"}, Revising: revising}}})
		return r.Local().Factories
	}
	offline := func(string) bool { return false }
	store.Reconcile(testNode, nil, offline, now)
	if _, err := store.Subscribe(subscriptionFor(t, "a", v.URL+"/a"), ""); err != nil {
		t.Fatal(err)
	}
	var bodies []string
	for _, revising := range []map[string]string{nil, {gate.ID: "designer"}, nil} {
		store.Reconcile(testNode, gated(revising), offline, now)
		var l ledger
		store.read(pushFile, &l)
		for _, j := range l.Jobs {
			var p Payload
			json.Unmarshal([]byte(j.Payload), &p)
			bodies = append(bodies, p.Body)
		}
		pusher.Deliver(ctx)
	}
	if want := []string{needsYou, workingBody, revisedBody}; !slices.Equal(bodies, want) {
		t.Fatalf("the gate was pushed saying %q; want %q: cleared while the designer works, then pushed again to review", bodies, want)
	}
}

func TestAPairedPhonesSubscriptionKeepsDeliveringAfterTheAuthMigration(t *testing.T) {
	store := Open(t.TempDir())
	public, err := store.VAPIDKey("")
	if err != nil {
		t.Fatal(err)
	}
	v := newVendor(t)
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	a, b := subscriptionFor(t, "", v.URL+"/a"), subscriptionFor(t, "", v.URL+"/b")
	legacy := map[string]any{
		"devices.json":       map[string]any{"devices": []map[string]any{{"id": "dev-a", "name": "Pixel", "hash": "h1"}, {"id": "dev-b", "name": "iPad", "hash": "h2"}}},
		"codes.json":         map[string]any{"codes": []map[string]any{{"hash": "c1", "expires": now}}},
		"subscriptions.json": map[string]any{"subscriptions": []map[string]any{{"device": "dev-a", "endpoint": a.Endpoint, "p256dh": a.P256dh, "auth": a.Auth}, {"device": "dev-b", "endpoint": b.Endpoint, "p256dh": b.P256dh, "auth": b.Auth}}},
		"push.json":          map[string]any{"baseline": true, "items": map[string]any{"old": map[string]any{"route": "r", "item": "q-old", "incarnation": "csv-1", "open": true, "revision": 1}}, "jobs": []map[string]any{{"id": "queued", "device": "dev-a", "endpoint": a.Endpoint, "tag": "old", "payload": "{}", "due": now}}},
	}
	for name, value := range legacy {
		if err := store.write(name, value); err != nil {
			t.Fatal(err)
		}
	}

	for range 2 {
		if err := store.Adopt(); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range pairingFiles {
		if _, err := os.Stat(store.path(name)); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("%s survived the migration; pairing codes and device hashes must be gone", name)
		}
	}
	if again, _ := store.VAPIDKey(""); again != public {
		t.Fatal("the migration changed the VAPID key, so every subscribed phone would go silent")
	}
	pusher := &Pusher{Store: store, Origin: "https://" + testHost, Client: v.Client(), Now: func() time.Time { return now }}
	pusher.Deliver(context.Background())
	if calls := v.took(); len(calls) != 1 || calls[0].path != "/a" {
		t.Fatalf("after the migration the vendor saw %+v; want the job queued before the upgrade delivered to phone a", calls)
	}
	if _, err := store.Subscribe(subscriptionFor(t, "pixel", a.Endpoint), ""); err != nil {
		t.Fatal(err)
	}
	store.Reconcile(testNode, factoryWith(false, "old", "k1"), func(string) bool { return false }, now)
	pusher.Deliver(context.Background())
	if calls := v.took(); len(calls) != 2 {
		t.Fatalf("a new item reached %+v; want both paired phones, including the one whose app re-registered", calls)
	}
}

func subjectOf(authorization string) string {
	token, _, _ := strings.Cut(strings.TrimPrefix(authorization, "vapid t="), ",")
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return ""
	}
	claims, _ := base64.RawURLEncoding.DecodeString(parts[1])
	var body struct{ Sub string }
	json.Unmarshal(claims, &body)
	return body.Sub
}
