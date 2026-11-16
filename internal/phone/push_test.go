package phone

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/afonsodelgado/factory/internal/host"
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

func subscriptionFor(t *testing.T, device, endpoint string) Subscription {
	key, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	auth := make([]byte, 16)
	rand.Read(auth)
	return Subscription{Device: device, Endpoint: endpoint, P256dh: base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes()), Auth: base64.RawURLEncoding.EncodeToString(auth)}
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
		queued, err := store.Reconcile(factories, connected, now)
		if err != nil {
			t.Fatal(err)
		}
		return queued
	}

	if reconcile(factoryWith(false, "old"), offline) {
		t.Fatal("enabling Phone notified about an item that was already open")
	}
	phones := map[string]Device{}
	for _, name := range []string{"a", "b"} {
		code, _, _ := store.NewCode(now)
		device, _, err := store.Exchange(code, name, now)
		if err != nil {
			t.Fatal(err)
		}
		phones[name] = device
		if err := store.Subscribe(subscriptionFor(t, device.ID, v.URL+"/"+name)); err != nil {
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
			if !strings.Contains("tag title body url revision resolved", name) {
				t.Errorf("payload field %q leaves the machine", name)
			}
		}
		if strings.Contains(j.Payload, "secret") {
			t.Errorf("payload %s carries the question, a path or the repo", j.Payload)
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

	if queued, _ := Open(store.Dir).Reconcile(factoryWith(false, "old", "k1"), offline, now); queued {
		t.Fatal("a restarted factory serve notified again about an item it already pushed")
	}

	v.answers["/a"] = []int{http.StatusServiceUnavailable}
	v.answers["/b"] = []int{http.StatusGone}
	reconcile(factoryWith(false, "old", "k1", "k2"), offline)
	pusher.Deliver(ctx)
	pusher.Deliver(ctx)
	if calls := v.took(); len(calls) != 2 {
		t.Fatalf("vendor saw %d pushes; want one each, with the failed one held for its Retry-After", len(calls))
	}
	if subs, _ := store.Subscriptions(); len(subs) != 1 || subs[0].Device != phones["a"].ID {
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
	if reconcile(factoryWith(false, "old"), func(string) bool { return true }) {
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
	if err := store.Revoke(phones["a"].ID); err != nil {
		t.Fatal(err)
	}
	pusher.Deliver(ctx)
	if calls := v.took(); len(calls) != 0 {
		t.Fatalf("a revoked phone was still pushed: %+v", calls)
	}

	if _, err := PublicClient().Get(v.URL); err == nil || !strings.Contains(err.Error(), "not public") {
		t.Fatalf("the production client reached a loopback push endpoint: %v", err)
	}
}

func TestAnOldEndpointsFailureLeavesTheRenewedSubscriptionOn(t *testing.T) {
	store := Open(t.TempDir())
	if _, err := store.VAPIDKey(""); err != nil {
		t.Fatal(err)
	}
	v := newVendor(t)
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	pusher := &Pusher{Store: store, Origin: "https://" + testHost, Client: v.Client(), Now: func() time.Time { return now }}
	offline := func(string) bool { return false }
	code, _, _ := store.NewCode(now)
	device, _, err := store.Exchange(code, "a", now)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Subscribe(subscriptionFor(t, device.ID, v.URL+"/old")); err != nil {
		t.Fatal(err)
	}
	store.Reconcile(factoryWith(false, "old"), offline, now)
	store.Reconcile(factoryWith(false, "old", "k1"), offline, now)
	if err := store.Subscribe(subscriptionFor(t, device.ID, v.URL+"/new")); err != nil {
		t.Fatal(err)
	}

	pusher.Deliver(context.Background())
	store.settle(job{ID: "in-flight", Device: device.ID, Endpoint: v.URL + "/old"}, http.StatusGone, 0, "", now)
	if sub, ok := store.SubscriptionOf(device.ID); !ok || sub.Endpoint != v.URL+"/new" {
		t.Fatalf("subscription %+v after the browser's old endpoint failed; want the renewed one kept, or the phone stays silent until the app is opened", sub)
	}
	if calls := v.took(); len(calls) != 0 {
		t.Fatalf("vendor saw %+v; want nothing sent to the replaced endpoint", calls)
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
