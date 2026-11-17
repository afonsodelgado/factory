package node_test

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/afonsodelgado/factory/internal/factory"
	"github.com/afonsodelgado/factory/internal/host"
	"github.com/afonsodelgado/factory/internal/node"
	"github.com/afonsodelgado/factory/internal/node/nodetest"
	"github.com/afonsodelgado/factory/internal/phone"
	"github.com/afonsodelgado/factory/internal/review"
	"github.com/afonsodelgado/factory/internal/state"
)

func names(n ...string) []string { return n }

func decodeJSON(r *http.Request, v any) error { return json.NewDecoder(r.Body).Decode(v) }

func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestHubAddAndInstallJoinConvergeOnOneListWithTheFirstHome(t *testing.T) {
	tn := nodetest.New(t)
	macbook := tn.Machine("macbook").Start()
	homelab := tn.Machine("homelab").Start()
	studio := tn.Machine("studio").Start()

	if _, err := macbook.Add("homelab"); err != nil {
		t.Fatal(err)
	}
	if _, err := studio.Join("macbook"); err != nil {
		t.Fatal(err)
	}
	tn.Settle(macbook, homelab, studio)

	for _, n := range []*nodetest.Node{macbook, homelab, studio} {
		roster := n.Roster()
		if got := n.Names(); !slices.Equal(got, names("homelab", "macbook", "studio")) || roster.Home != "https://"+macbook.Name {
			t.Errorf("%s lists %v with home %s; want all three nodes and macbook's app home", n.Short(), got, roster.Home)
		}
		if slices.ContainsFunc(roster.Members, func(m node.RosterMember) bool { return m.Pending > 0 }) {
			t.Errorf("%s still shows pending propagation after every node answered: %+v", n.Short(), roster.Members)
		}
	}
	if strays := tn.Strays(); len(strays) > 0 {
		t.Errorf("nodes dialed addresses outside the tailnet: %v", strays)
	}
}

func TestAnOfflineMemberShowsPendingThenCatchesUp(t *testing.T) {
	tn := nodetest.New(t)
	macbook := tn.Machine("macbook").Start()
	homelab := tn.Machine("homelab").Start()
	studio := tn.Machine("studio").Start()
	for _, name := range []string{"homelab", "studio"} {
		if _, err := macbook.Add(name); err != nil {
			t.Fatal(err)
		}
	}
	tn.Settle(macbook, homelab, studio)

	studio.SetOnline(false)
	if err := macbook.Remove(homelab.ID); err != nil {
		t.Fatal(err)
	}
	tn.Settle(macbook, homelab)
	pending := slices.IndexFunc(macbook.Roster().Members, func(m node.RosterMember) bool { return m.ID == studio.ID && m.Pending > 0 })
	if pending < 0 {
		t.Errorf("macbook claims offline studio applied the removal: %+v", macbook.Roster().Members)
	}

	studio.SetOnline(true)
	tn.Settle(macbook, studio)
	if got := studio.Names(); !slices.Equal(got, names("macbook", "studio")) {
		t.Errorf("studio lists %v after reconnecting; want the removal it missed", got)
	}
}

func TestARemovalBeatsAConcurrentStaleAddAndAReturningNodeCannotResurrectItself(t *testing.T) {
	tn := nodetest.New(t)
	macbook := tn.Machine("macbook").Start()
	homelab := tn.Machine("homelab").Start()
	studio := tn.Machine("studio").Start()
	for _, name := range []string{"homelab", "studio"} {
		if _, err := macbook.Add(name); err != nil {
			t.Fatal(err)
		}
	}
	tn.Settle(macbook, homelab, studio)

	homelab.SetOnline(false)
	studio.SetOnline(false)
	if err := macbook.Remove(homelab.ID); err != nil {
		t.Fatal(err)
	}
	if err := macbook.Remove(studio.ID); err != nil {
		t.Fatal(err)
	}
	stale := studio.Nodes.Dir.Draft(node.Member{ID: homelab.ID, Name: homelab.Name, Owner: nodetest.Owner}, uuid.NewString(), node.Ledger{})
	if err := studio.Nodes.Dir.Merge(stale); err != nil {
		t.Fatal(err)
	}
	homelab.SetOnline(true)
	studio.SetOnline(true)
	tn.Settle(homelab, studio, macbook)

	if got := macbook.Names(); !slices.Equal(got, names("macbook")) {
		t.Errorf("macbook lists %v after removed nodes came back with stale lists; want only itself", got)
	}
	for _, n := range []*nodetest.Node{homelab, studio} {
		if r := n.Roster(); !r.Removed {
			t.Errorf("%s never learned it was removed: %+v", n.Short(), r)
		}
	}
	if leaving := macbook.Roster().Leaving; len(leaving) > 0 {
		t.Errorf("macbook still waits for removed nodes to learn their removal: %+v", leaving)
	}
}

func TestARejoinWhoseHistoryRemovesTheAddingNodeIsRefused(t *testing.T) {
	tn := nodetest.New(t)
	macbook := tn.Machine("macbook").Start()
	homelab := tn.Machine("homelab").Start()
	if _, err := macbook.Add("homelab"); err != nil {
		t.Fatal(err)
	}
	tn.Settle(macbook, homelab)
	homelab.SetOnline(false)
	if err := macbook.Remove(homelab.ID); err != nil {
		t.Fatal(err)
	}
	if err := homelab.Remove(macbook.ID); err != nil {
		t.Fatal(err)
	}
	homelab.SetOnline(true)
	if _, err := macbook.Add("homelab"); err == nil || !strings.Contains(err.Error(), "removes this node") {
		t.Errorf("re-adding homelab, whose history removes macbook, answered %v", err)
	}
	if got := macbook.Names(); !slices.Equal(got, names("macbook")) {
		t.Errorf("macbook lists %v after the refused rejoin; want itself", got)
	}
}

func TestRemovingANodeLeavesItsFactoriesRefusesThisMachineAndStopsTheRemovedNodeChangingLists(t *testing.T) {
	tn := nodetest.New(t)
	macbook := tn.Machine("macbook").Start()
	homelab := tn.Machine("homelab").Start()
	homelab.Runtime.SetFactories(host.LocalFactory{Key: "/r/app:csv", ID: "csv", Status: "working"})
	if _, err := macbook.Add("homelab"); err != nil {
		t.Fatal(err)
	}
	tn.Settle(macbook, homelab)

	err := macbook.Remove(macbook.ID)
	if err == nil || !strings.Contains(err.Error(), "factory node uninstall") {
		t.Errorf("removing this machine answered %v; want a refusal that points to factory node uninstall", err)
	}
	if err := macbook.Remove(homelab.ID); err != nil {
		t.Fatal(err)
	}
	tn.Settle(macbook, homelab)
	eventually(t, "homelab's snapshot to show its removal", func() bool {
		snap, _ := homelab.API.Latest()
		return snap.Roster != nil && snap.Roster.Removed
	})
	snap, _ := homelab.API.Latest()
	if calls := homelab.Runtime.Calls(); len(calls) > 0 || len(snap.Factories) != 1 || !snap.Roster.Removed {
		t.Errorf("homelab after removal: runtime calls %v, factories %v, roster %+v; want its factory untouched and the removal known", calls, snap.Factories, snap.Roster)
	}
	tn.Machine("studio").Start()
	if _, err := homelab.Add("studio"); err == nil || !strings.Contains(err.Error(), "removed from its node list") {
		t.Errorf("a removed node added studio: %v", err)
	}
}

func TestOnlyTheOwnersOwnUntaggedCompatibleNodesCanBeAdded(t *testing.T) {
	tn := nodetest.New(t)
	macbook := tn.Machine("macbook").Start()
	shared := tn.Machine("sams-mini")
	shared.Login = "sam@example.com"
	shared.Start()
	foreign := tn.Machine("guest")
	foreign.Name = "guest.tail9999.ts.net"
	foreign.Start()
	tagged := tn.Machine("ci-runner")
	tagged.Tags = []string{"tag:ci"}
	tagged.Start()
	older := tn.Machine("old-mini").Start()
	older.Nodes.Protocol, older.Nodes.MinProtocol = node.Protocol+5, node.Protocol+5
	funnel := tn.Machine("public")
	funnel.Start()
	funnel.SetFunnel(true)

	cases := map[string]string{
		"sams-mini":             "belongs to sam@example.com",
		"guest.tail9999.ts.net": "not on " + nodetest.Suffix,
		"ci-runner":             "tagged tag:ci",
		"old-mini":              "factory update",
		"public":                "Funnel is on",
	}
	for name, want := range cases {
		report, err := macbook.Nodes.Check(context.Background(), name)
		var notFound node.NotFoundError
		if errors.As(err, &notFound) && name == "guest.tail9999.ts.net" {
			continue
		}
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		text := report.Refusal + strings.Join(fixes(report), " ")
		if report.Refusal == "" || !strings.Contains(text, want) {
			t.Errorf("checking %s refused %q with fixes %v; want a refusal naming %q", name, report.Refusal, fixes(report), want)
		}
		if _, err := macbook.Add(name); err == nil {
			t.Errorf("adding %s succeeded", name)
		}
	}
	if got := macbook.Names(); !slices.Equal(got, names("macbook")) {
		t.Errorf("macbook lists %v after refused adds", got)
	}
}

func fixes(r node.CheckReport) []string {
	var all []string
	for _, c := range r.Checks {
		all = append(all, c.Fix)
	}
	return all
}

func TestTypedNamesResolveOnlyThroughTailscaleStatus(t *testing.T) {
	tn := nodetest.New(t)
	macbook := tn.Machine("macbook").Start()
	homelab := tn.Machine("homelab")
	homelab.Start()
	for _, typed := range []string{"homelab", "HOMELAB", homelab.Name, homelab.Name + ".", homelab.IP} {
		report, err := macbook.Nodes.Check(context.Background(), typed)
		if err != nil || report.Target.Name != homelab.Name || report.Refusal != "" {
			t.Errorf("checking %q gave %+v, %v; want homelab's canonical name", typed, report.Target, err)
		}
	}
	for _, typed := range []string{"https://evil.example", "evil.example", "homelab.tail9999.ts.net", "100.64.9.9", "homelb"} {
		var notFound node.NotFoundError
		if _, err := macbook.Nodes.Check(context.Background(), typed); !errors.As(err, &notFound) {
			t.Errorf("checking %q answered %v; want a name tailscale status does not list", typed, err)
		}
	}
	if strays := tn.Strays(); len(strays) > 0 {
		t.Errorf("typed names reached addresses outside tailscale status: %v", strays)
	}
}

func TestDiscoveryAsksEachMachineForAtMostItsCapAndListsTheRestAsDiagnostics(t *testing.T) {
	tn := nodetest.New(t)
	macbook := tn.Machine("macbook").Start()
	tn.Machine("homelab").Start()
	sleeper := tn.Machine("sleeper")
	sleeper.Hang()
	tn.Machine("nas")
	offline := tn.Machine("old-mini")
	offline.SetOnline(false)
	phone := tn.Machine("iphone")
	phone.OS = "iOS"
	other := tn.Machine("sams-mini")
	other.Login = "sam@example.com"
	tagged := tn.Machine("ci-runner")
	tagged.Tags = []string{"tag:ci"}

	started := time.Now()
	rows, err := macbook.Nodes.Discover(context.Background())
	took := time.Since(started)
	if err != nil {
		t.Fatal(err)
	}
	states := map[string]string{}
	for _, r := range rows {
		states[r.Short] = r.State
	}
	want := map[string]string{"homelab": node.StateNode, "sleeper": node.StateNoNode, "nas": node.StateNoNode, "old-mini": node.StateOffline, "ci-runner": node.StateTagged}
	if !mapsEqual(states, want) {
		t.Errorf("discovery listed %v; want %v", states, want)
	}
	if took > tn.ProbeWait+time.Second {
		t.Errorf("discovery took %s with one machine that never answers; want each probe capped at %s", took, tn.ProbeWait)
	}
}

func mapsEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func TestChecksWriteAndCloneNothingStopWhenCancelledAndOnlyAnExplicitCloneRunsOnTheTarget(t *testing.T) {
	tn := nodetest.New(t)
	macbook := tn.Machine("macbook").Start()
	homelab := tn.Machine("homelab").Start()
	cowork := "git@github.com:alice/cowork.git"
	macbook.SetRepos(map[string]string{cowork: "/r/cowork"})
	before := rosterFiles(t, macbook, homelab)
	if _, err := macbook.Nodes.Check(context.Background(), "homelab"); err != nil {
		t.Fatal(err)
	}
	if clones := homelab.Clones(); len(clones) > 0 {
		t.Errorf("checking homelab cloned %v", clones)
	}
	stopped := make(chan struct{})
	homelab.SetProbes(node.Probe{Name: "git access", Run: func(ctx context.Context) node.Check {
		<-ctx.Done()
		close(stopped)
		return node.Check{}
	}})
	homelab.Nodes.Ready.Wait = time.Hour
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(200 * time.Millisecond)
		cancel()
	}()
	if _, err := macbook.Nodes.Check(ctx, "homelab"); !errors.Is(err, context.Canceled) {
		t.Errorf("a cancelled check answered %v", err)
	}
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Error("homelab kept running its check after the caller stopped asking")
	}
	if after := rosterFiles(t, macbook, homelab); !slices.Equal(before, after) {
		t.Error("checking a node changed a node list")
	}

	laptop := tn.Machine("laptop")
	clone := node.CloneRequest{Operation: uuid.NewString(), URL: cowork}
	if status, _, _ := tn.Request(laptop, homelab.Machine, "POST", "/api/clone", "https://evil.example", clone); status != http.StatusForbidden {
		t.Errorf("another origin's clone answered %d", status)
	}
	if status, _, body := tn.Request(laptop, homelab.Machine, "POST", "/api/clone", "", clone); status != http.StatusOK {
		t.Fatalf("the hub's clone answered %d %s", status, body)
	}
	if got := homelab.Clones(); !slices.Equal(got, []string{cowork}) || len(macbook.Clones()) > 0 {
		t.Errorf("homelab cloned %v and macbook %v; want only the explicit clone, on homelab with its own git login", got, macbook.Clones())
	}
}

func rosterFiles(t *testing.T, nodes ...*nodetest.Node) []string {
	var files []string
	for _, n := range nodes {
		data, err := os.ReadFile(node.RosterPath(n.Dir))
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, string(data))
	}
	return files
}

func TestAddRechecksAtCommitAndAStalledTargetAddsNothing(t *testing.T) {
	tn := nodetest.New(t)
	macbook := tn.Machine("macbook").Start()
	homelab := tn.Machine("homelab").Start()
	if report, err := macbook.Nodes.Check(context.Background(), "homelab"); err != nil || report.Refusal != "" {
		t.Fatalf("%+v, %v", report, err)
	}
	homelab.SetFunnel(true)
	if _, err := macbook.Add("homelab"); err == nil {
		t.Error("adding homelab after Funnel turned on succeeded")
	}
	homelab.SetFunnel(false)
	homelab.SetProbes(node.Probe{Name: "tools", Run: func(ctx context.Context) node.Check {
		<-ctx.Done()
		return node.Check{}
	}})
	homelab.Nodes.Ready.Wait = time.Hour
	if _, err := macbook.Add("homelab"); err == nil {
		t.Error("adding homelab while its check never answered succeeded")
	}
	for _, n := range []*nodetest.Node{macbook, homelab} {
		if got := n.Names(); len(got) != 1 {
			t.Errorf("%s lists %v after refused adds; want nothing written", n.Short(), got)
		}
	}
}

func TestANodeMissingAToolJoinsButRefusesToCreateFactories(t *testing.T) {
	tn := nodetest.New(t)
	macbook := tn.Machine("macbook").Start()
	homelab := tn.Machine("homelab").Start()
	homelab.SetProbes(node.Probe{Name: "tools", Run: func(context.Context) node.Check {
		return node.Check{Name: "tools", Mark: node.MarkFail, Detail: "gh-stack not installed", Fix: "gh extension install github/gh-stack"}
	}})
	result, err := macbook.Add("homelab")
	if err != nil || result.Report.Problems != 1 || result.Member.ID != homelab.ID {
		t.Fatalf("adding homelab with a missing tool answered %+v, %v; want it added with one named problem", result, err)
	}
	err = homelab.Local.Request(context.Background(), "create", uuid.NewString(), host.CreateRequest{Repo: "/r/app", Slug: "csv"}, nil)
	if err == nil || !strings.Contains(err.Error(), "gh extension install github/gh-stack") {
		t.Errorf("creating a factory on homelab answered %v; want a refusal naming the fix", err)
	}
	if calls := homelab.Runtime.Calls(); slices.Contains(calls, "create") || slices.Contains(calls, "open") {
		t.Errorf("the runtime received a new factory before homelab was runnable: %v", calls)
	}
	homelab.Runtime.SetFactories(host.LocalFactory{Key: "/r/app:csv", ID: "csv", Repo: "/r/app"})
	eventually(t, "homelab to publish its factory", func() bool { return homelab.API.Holds("/r/app", "csv") })
	if err := homelab.Local.Request(context.Background(), "open", uuid.NewString(), host.OpenRequest{Name: "csv", Repo: "/r/app"}, nil); err != nil {
		t.Errorf("opening an existing factory on homelab was refused: %v", err)
	}
}

func TestBrowsersCannotEnrollReadOrReachNodeRoutesFromOtherOrigins(t *testing.T) {
	tn := nodetest.New(t)
	macbook := tn.Machine("macbook").Start()
	homelab := tn.Machine("homelab").Start()
	if _, err := macbook.Add("homelab"); err != nil {
		t.Fatal(err)
	}
	tn.Settle(macbook, homelab)
	phone := tn.Machine("iphone")
	phone.OS = "iOS"
	home := "https://" + macbook.Name
	add := node.NodeRequest{Operation: uuid.NewString(), Name: "studio"}
	cases := []struct {
		name, method, path, origin string
		body                       any
		headers                    []string
		status                     int
		corsFor                    string
	}{
		{"an unknown origin reading the snapshot", "GET", "/api/snapshot", "https://evil.example", nil, nil, 403, ""},
		{"an unknown origin's preflight", "OPTIONS", "/api/nodes", "https://evil.example", nil, []string{"Access-Control-Request-Method", "POST"}, 403, ""},
		{"an opaque origin enrolling", "POST", "/api/nodes", "null", add, nil, 403, ""},
		{"a native caller on the browser enrollment route", "POST", "/api/nodes", "", add, nil, 403, ""},
		{"a page embedding the API from another site", "GET", "/api/nodes", "", nil, []string{"Sec-Fetch-Site", "cross-site"}, 403, ""},
		{"the home app on a node route", "POST", "/api/node/pull", home, node.Pull{From: macbook.ID}, nil, 403, home},
		{"a browser without Origin on a node route", "GET", "/api/node/hello", "", nil, []string{"Sec-Fetch-Mode", "cors"}, 403, ""},
		{"the home app reading another node", "GET", "/api/nodes", home, nil, nil, 200, home},
		{"the home app's preflight", "OPTIONS", "/api/snapshot", home, nil, []string{"Access-Control-Request-Method", "GET"}, 204, home},
		{"the home app checking a machine through another node", "POST", "/api/nodes/check", home, node.NodeRequest{Name: "macbook"}, nil, 200, home},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			status, header, body := tn.Request(phone, homelab.Machine, c.method, c.path, c.origin, c.body, c.headers...)
			if status != c.status || header.Get("Access-Control-Allow-Origin") != c.corsFor {
				t.Fatalf("got %d with CORS %q: %s; want %d with CORS %q", status, header.Get("Access-Control-Allow-Origin"), body, c.status, c.corsFor)
			}
			if c.origin != "" && !slices.Contains(header.Values("Vary"), "Origin") {
				t.Errorf("an Origin-dependent answer has no Vary: Origin")
			}
			if c.status == 403 && (strings.Contains(body, "factories") || strings.Contains(body, "members")) {
				t.Errorf("a refused request still carried data: %s", body)
			}
		})
	}
}

type fakeCoordinator struct {
	challenges chan string
}

func (f fakeCoordinator) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var v node.Verify
	if r.URL.Path == "/api/node/join/verify" && decodeJSON(r, &v) == nil {
		f.challenges <- v.Challenge
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"node":"x"}`))
}

func TestAJoinChallengeWorksOnceAndExpires(t *testing.T) {
	tn := nodetest.New(t)
	homelab := tn.Machine("homelab").Start()
	coordinator := tn.Machine("macbook")
	fake := fakeCoordinator{challenges: make(chan string, 4)}
	coordinator.Serve(fake)
	listed := node.Op{ID: uuid.NewString(), Author: "x", Clock: node.Clock{"x": 1}, Kind: "add", Member: node.Member{ID: homelab.ID, Name: homelab.Name}}
	ledger := node.Ledger{Roster: uuid.NewString(), Home: "https://" + coordinator.Name, Ops: []node.Op{listed}}
	prepare := func(join string) string {
		status, _, body := tn.Request(coordinator, homelab.Machine, "POST", "/api/node/join/prepare", "", node.Prepare{Join: join, Coordinator: coordinator.Name})
		if status != 200 {
			t.Fatalf("prepare answered %d %s", status, body)
		}
		return <-fake.challenges
	}
	sender := phone.Open(t.TempDir())
	sender.VAPIDKey("")
	shared, err := sender.Export()
	if err != nil {
		t.Fatal(err)
	}
	commit := func(join, challenge string, ledger node.Ledger) int {
		before := keyOf(t, homelab).Fingerprint
		status, _, _ := tn.Request(coordinator, homelab.Machine, "POST", "/api/node/join/commit", "", node.Commit{Join: join, Challenge: challenge, Ledger: ledger, Push: &shared})
		if status != 200 && keyOf(t, homelab).Fingerprint != before {
			t.Fatalf("a refused commit (%d) still installed the push key it carried", status)
		}
		return status
	}

	first := uuid.NewString()
	challenge := prepare(first)
	if status := commit(first, "g"+challenge[1:], ledger); status == 200 {
		t.Error("a commit with the wrong challenge was accepted")
	}
	if status := commit(first, challenge, ledger); status == 200 {
		t.Error("a challenge was accepted after a failed attempt used it up")
	}
	second := uuid.NewString()
	challenge = prepare(second)
	tn.Advance(node.ChallengeLife + time.Second)
	if status := commit(second, challenge, ledger); status == 200 {
		t.Error("an expired challenge was accepted")
	}
	tn.Advance(-node.ChallengeLife - time.Second)
	third := uuid.NewString()
	challenge = prepare(third)
	if status := commit(third, challenge, node.Ledger{Roster: uuid.NewString(), Home: "https://" + coordinator.Name}); status == 200 {
		t.Error("a commit whose list leaves this node out was accepted")
	}
	fourth := uuid.NewString()
	challenge = prepare(fourth)
	if status := commit(fourth, challenge, ledger); status != 200 {
		t.Fatalf("a fresh challenge answered %d", status)
	}
	if status := commit(fourth, challenge, ledger); status == 200 {
		t.Error("a used challenge was replayed")
	}
	if homelab.Roster().ID != ledger.Roster || keyOf(t, homelab).Fingerprint != shared.Fingerprint() {
		t.Error("the one accepted commit did not take the coordinator's list and push key")
	}
}

func TestAttachingToAnotherNodesPaneRunsSSHOnlyForThePreparedView(t *testing.T) {
	tn := nodetest.New(t)
	tn.Machine("macbook").Start()
	homelab := tn.Machine("homelab").Start()
	remote := node.Remote(homelab.Name, tn.Client(tn.Machine("laptop")))
	if _, err := node.AttachCommand(context.Background(), remote, homelab.Name, host.AttachRequest{}); err == nil {
		t.Error("an attach the node refused still produced an ssh command")
	}
	args, err := node.AttachCommand(context.Background(), remote, homelab.Name, host.AttachRequest{Key: "/r/app:csv", Role: "supervisor", Token: "%12"})
	if err != nil {
		t.Fatal(err)
	}
	if args[0] != "ssh" || !slices.Contains(args, homelab.Name) || args[len(args)-1] != "view-/r/app:csv-%12" {
		t.Errorf("attach ran %v; want ssh to homelab opening the view the node prepared", args)
	}
}

type pushVendor struct {
	*httptest.Server
	mu      sync.Mutex
	calls   []string
	answers []int
}

func newPushVendor(t *testing.T) *pushVendor {
	v := &pushVendor{}
	v.Server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		v.mu.Lock()
		defer v.mu.Unlock()
		_, key, _ := strings.Cut(r.Header.Get("Authorization"), ", k=")
		v.calls = append(v.calls, r.URL.Path+" "+key)
		status := http.StatusCreated
		if len(v.answers) > 0 {
			status, v.answers = v.answers[0], v.answers[1:]
			w.Header().Set("Retry-After", "60")
		}
		w.WriteHeader(status)
	}))
	t.Cleanup(v.Close)
	return v
}

func (v *pushVendor) took() []string {
	v.mu.Lock()
	defer v.mu.Unlock()
	calls := v.calls
	v.calls = nil
	return calls
}

func (v *pushVendor) answer(statuses ...int) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.answers = statuses
}

func browserSubscription(t *testing.T, endpoint, installation string) map[string]any {
	key, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	auth := make([]byte, 16)
	rand.Read(auth)
	keys := map[string]string{"p256dh": base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes()), "auth": base64.RawURLEncoding.EncodeToString(auth)}
	return map[string]any{"endpoint": endpoint, "expirationTime": nil, "keys": keys, "installation": installation}
}

func subscribeAt(t *testing.T, tn *nodetest.Tailnet, n *nodetest.Node, body map[string]any) {
	t.Helper()
	if status, _, answer := tn.Request(tn.Machine("iphone"), n.Machine, "POST", "/api/push/subscription", "https://"+n.Name, body); status != 200 {
		t.Fatalf("subscribing at %s answered %d %s", n.Short(), status, answer)
	}
}

func endpoints(t *testing.T, n *nodetest.Node) []string {
	t.Helper()
	subs, err := n.Phone.Subscriptions()
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, sub := range subs {
		paths = append(paths, sub.Endpoint[strings.LastIndex(sub.Endpoint, "/"):])
	}
	slices.Sort(paths)
	return paths
}

func keyOf(t *testing.T, n *nodetest.Node) phone.KeyState {
	t.Helper()
	state, err := n.Phone.KeyState()
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func needingYou(key string) host.LocalFactory {
	return host.LocalFactory{Key: "/r/app:" + key, ID: key, Incarnation: "inc-" + key, Items: []host.LocalItem{{Key: key, AttentionItem: host.AttentionItem{ID: "q-" + key, Kind: "question"}}}}
}

func publish(t *testing.T, n *nodetest.Node, f host.LocalFactory) {
	t.Helper()
	n.Runtime.SetFactories(f)
	eventually(t, n.Short()+" to publish its item", func() bool { _, published, _ := n.API.Factories(); return len(published) == 1 })
}

func firstEvent(t *testing.T, tn *nodetest.Tailnet, n *nodetest.Node, origin string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+n.Name+"/api/events", nil)
	req.Header.Set("Origin", origin)
	resp, err := tn.Client(tn.Machine("iphone")).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	lines := bufio.NewScanner(resp.Body)
	lines.Buffer(nil, 1<<20)
	for lines.Scan() {
		if data, ok := strings.CutPrefix(lines.Text(), "data: "); ok {
			return data
		}
	}
	t.Fatalf("%s's event stream sent no event: %v", n.Short(), lines.Err())
	return ""
}

func TestJoiningCopiesThePushKeyOnlyToTheVerifiedNodeAndBothSendToTheSamePhone(t *testing.T) {
	ctx := context.Background()
	vendor := newPushVendor(t)
	tn := nodetest.New(t)
	tn.PushClient = vendor.Client()
	macbook := tn.Machine("macbook").Start()
	homelab := tn.Machine("homelab").Start()
	subscribeAt(t, tn, macbook, browserSubscription(t, vendor.URL+"/iphone", "iphone"))

	result, err := macbook.Add("homelab")
	if err != nil {
		t.Fatal(err)
	}
	mine, theirs := keyOf(t, macbook), keyOf(t, homelab)
	if !result.Push.Shared || theirs.Fingerprint != mine.Fingerprint || !slices.Equal(endpoints(t, homelab), names("/iphone")) {
		t.Fatalf("after the join homelab sends with %+v and holds %v, the add reported %+v; want macbook's key %s and the phone's subscription", theirs, endpoints(t, homelab), result.Push, mine.Fingerprint)
	}
	secret, err := macbook.Phone.Export()
	if err != nil {
		t.Fatal(err)
	}
	added, _ := json.Marshal(result)
	seen := []string{string(added)}
	home := "https://" + macbook.Name
	for _, n := range []*nodetest.Node{macbook, homelab} {
		for _, path := range []string{"/api/nodes", "/api/snapshot", "/api/nodes/candidates", "/api/push/subscription?installation=iphone"} {
			_, _, body := tn.Request(tn.Machine("iphone"), n.Machine, "GET", path, home, nil)
			seen = append(seen, body)
		}
		_, _, report := tn.Request(tn.Machine("iphone"), n.Machine, "POST", "/api/nodes/check", home, node.NodeRequest{Name: "studio"})
		seen = append(seen, report)
	}
	seen = append(seen, firstEvent(t, tn, homelab, home))
	for _, body := range seen {
		if strings.Contains(body, secret.Private) || strings.Contains(body, secret.Subscriptions[0].Auth) {
			t.Fatalf("a browser-visible answer carries the private push key or a subscription secret: %s", body)
		}
	}
	if info, err := os.Stat(filepath.Join(homelab.Dir, "phone", "vapid.json")); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("homelab keeps the copied key in %v (%v); want a file only its owner reads", info, err)
	}

	for _, n := range []*nodetest.Node{macbook, homelab} {
		n.Notify(ctx)
	}
	publish(t, macbook, needingYou("k-mac"))
	publish(t, homelab, needingYou("k-home"))
	macbook.Notify(ctx)
	homelab.Notify(ctx)
	calls := vendor.took()
	if len(calls) != 2 || calls[0] != calls[1] || calls[0] != "/iphone "+mine.Public {
		t.Fatalf("the push service saw %v; want one push from each node to the same subscription, both signed with macbook's key", calls)
	}
}

func TestAddWithoutPKeepsTheTargetsOwnKeyAndPSwitchesItAndDropsItsOldPhone(t *testing.T) {
	vendor := newPushVendor(t)
	for _, switching := range []bool{false, true} {
		tn := nodetest.New(t)
		tn.PushClient = vendor.Client()
		macbook := tn.Machine("macbook").Start()
		homelab := tn.Machine("homelab").Start()
		subscribeAt(t, tn, macbook, browserSubscription(t, vendor.URL+"/iphone", "iphone"))
		subscribeAt(t, tn, homelab, browserSubscription(t, vendor.URL+"/ipad", "ipad"))
		before := keyOf(t, homelab)
		secret, err := macbook.Phone.Export()
		if err != nil {
			t.Fatal(err)
		}
		tn.Watch("/api/node/join/commit")

		var report node.CheckReport
		if err := macbook.Local.Do(context.Background(), http.MethodPost, "/api/nodes/check", node.NodeRequest{Name: "homelab"}, &report); err != nil || !report.PushConflict {
			t.Fatalf("checking homelab answered %+v, %v; want the push key conflict named before anything is written", report, err)
		}
		result, err := macbook.AddWith(node.NodeRequest{Operation: uuid.NewString(), Name: "homelab", ReplaceKey: switching})
		if err != nil {
			t.Fatal(err)
		}
		tn.Settle(macbook, homelab)
		after, shared := keyOf(t, homelab), keyOf(t, macbook)
		switch {
		case !switching && (after.Fingerprint != before.Fingerprint || after.Conflict != shared.Fingerprint || result.Push.Conflict == "" || !slices.Equal(endpoints(t, homelab), names("/ipad"))):
			t.Errorf("without p homelab sends with %+v and holds %v, the add reported %+v; want its own key %s and phone kept, the conflict named, and no subscription for another key", after, endpoints(t, homelab), result.Push, before.Fingerprint)
		case switching && (after.Fingerprint != shared.Fingerprint || after.Conflict != "" || !result.Push.Shared || !slices.Equal(endpoints(t, homelab), names("/iphone"))):
			t.Errorf("with p homelab sends with %+v and holds %v; want macbook's key %s and only the shared phone, its old phone dropped", after, endpoints(t, homelab), shared.Fingerprint)
		}
		sentKey := slices.ContainsFunc(tn.Bodies("/api/node/join/commit"), func(body string) bool { return strings.Contains(body, secret.Private) })
		if sentKey != switching {
			t.Errorf("the private key went to homelab: %v; want it sent only when p switches homelab's key", sentKey)
		}
	}
}

type impostorTarget struct {
	tn      *nodetest.Tailnet
	self    *nodetest.Machine
	id      string
	commits chan string
}

func (f impostorTarget) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	body, _ := io.ReadAll(r.Body)
	switch r.URL.Path {
	case "/api/node/hello":
		json.NewEncoder(w).Encode(node.Hello{ID: f.id, Name: f.self.Name, Version: nodetest.Version, Protocol: node.Protocol, MinProtocol: node.MinProtocol, OS: "linux", Owner: nodetest.Owner, Roster: uuid.NewString(), Members: 1})
	case "/api/node/readiness":
		w.Write([]byte(`{"checks":[],"problems":0}`))
	case "/api/node/pull":
		w.Write([]byte(`{"roster":"elsewhere","ops":[]}`))
	case "/api/node/join/prepare":
		var p node.Prepare
		json.Unmarshal(body, &p)
		node.Remote(p.Coordinator, f.tn.Client(f.self)).Do(r.Context(), http.MethodPost, "/api/node/join/verify", node.Verify{Join: p.Join, Node: f.id, Name: "impostor." + nodetest.Suffix, Challenge: "guessed"}, nil)
		json.NewEncoder(w).Encode(node.Joined{Node: f.id})
	default:
		f.commits <- string(body)
		w.Write([]byte(`{}`))
	}
}

func TestACoordinatorSendsNoPushKeyToANodeThatFailsItsCallback(t *testing.T) {
	tn := nodetest.New(t)
	macbook := tn.Machine("macbook").Start()
	homelab := tn.Machine("homelab")
	fake := impostorTarget{tn: tn, self: homelab, id: uuid.NewString(), commits: make(chan string, 4)}
	homelab.Serve(fake)
	if _, err := macbook.Add("homelab"); err == nil || !strings.Contains(err.Error(), "did not prove") {
		t.Fatalf("adding a node whose callback names another machine answered %v", err)
	}
	select {
	case body := <-fake.commits:
		t.Fatalf("the coordinator still sent %s", body)
	default:
	}
}

func TestSubscriptionChangesReachEveryNodeAndARemovedNodeCancelsItsQueuedSends(t *testing.T) {
	ctx := context.Background()
	vendor := newPushVendor(t)
	tn := nodetest.New(t)
	tn.PushClient = vendor.Client()
	macbook := tn.Machine("macbook").Start()
	homelab := tn.Machine("homelab").Start()
	if _, err := macbook.Add("homelab"); err != nil {
		t.Fatal(err)
	}
	iphone := browserSubscription(t, vendor.URL+"/iphone", "iphone")
	subscribeAt(t, tn, macbook, iphone)
	tn.Settle(macbook, homelab)
	if got := endpoints(t, homelab); !slices.Equal(got, names("/iphone")) {
		t.Fatalf("homelab holds %v after the phone subscribed at home; want the subscription replicated", got)
	}
	tn.Request(tn.Machine("iphone"), macbook.Machine, "DELETE", "/api/push/subscription", "https://"+macbook.Name, map[string]string{"endpoint": vendor.URL + "/iphone"})
	tn.Settle(macbook, homelab)
	if got := endpoints(t, homelab); len(got) != 0 {
		t.Fatalf("homelab holds %v after the phone turned notifications off; want the removal replicated", got)
	}
	subscribeAt(t, tn, macbook, iphone)
	tn.Settle(macbook, homelab)

	homelab.Notify(ctx)
	publish(t, homelab, needingYou("k-home"))
	vendor.answer(http.StatusServiceUnavailable)
	homelab.Notify(ctx)
	if calls := vendor.took(); len(calls) != 1 {
		t.Fatalf("the push service saw %v; want one push, held for its Retry-After", calls)
	}
	homelab.SetOnline(false)
	if err := macbook.Remove(homelab.ID); err != nil {
		t.Fatal(err)
	}
	homelab.SetOnline(true)
	tn.Settle(macbook, homelab)
	tn.Advance(2 * time.Minute)
	homelab.Pusher.Deliver(ctx)
	if calls := vendor.took(); len(calls) > 0 || len(endpoints(t, homelab)) > 0 {
		t.Fatalf("after learning its removal homelab pushed %v and still holds %v; want its queued send cancelled and the shared phone forgotten", calls, endpoints(t, homelab))
	}
	if got := endpoints(t, macbook); !slices.Equal(got, names("/iphone")) {
		t.Errorf("macbook holds %v after removing homelab; want its own subscription untouched", got)
	}
}

type qaEngine struct{}

func (qaEngine) NewFactoryPane(host.NewPane) (string, error) { return "%1", nil }
func (qaEngine) Pause(string, string) error                  { return nil }
func (qaEngine) NewStagePane(host.StagePane) (string, error) { return "%2", nil }
func (qaEngine) KillStages(string, string, string) error     { return nil }
func (qaEngine) Type(string, string) error                   { return nil }

type qaNode struct {
	*nodetest.Node
	f        factory.Factory
	result   string
	browsers *node.Browsers
	stop     func()
}

func (q *qaNode) boot(t *testing.T) {
	q.browsers = &node.Browsers{NoSandbox: goruntime.GOOS == "linux", StateDir: q.Dir}
	queue := node.NewQueue()
	ctx, cancel := context.WithCancel(context.Background())
	r := host.New(host.Runtime{Config: &state.Config{StateDir: q.Dir}, Engine: qaEngine{}, Jobs: &node.Jobs{Queue: queue, Ctx: ctx}, Browsers: q.browsers})
	r.SetSnapshot(host.Snapshot{At: time.Now(), Items: []host.Item{{Factory: q.f}}})
	stopped := make(chan struct{})
	go func() { r.Run(ctx, queue.Events()); close(stopped) }()
	q.stop = func() { cancel(); <-stopped }
	q.Runtime.Behind(queue)
	browsers := q.browsers
	t.Cleanup(func() { cancel(); <-stopped; browsers.Close() })
}

func (q *qaNode) job(t *testing.T) (dir, browserURL string, stack int) {
	t.Helper()
	launch := state.LaunchScriptPath(q.Dir, q.f.Repo, q.f.Slug, "qa")
	until(t, "the QA pane on "+q.Short()+"'s browser", time.Minute, func() bool {
		if failed, err := os.ReadFile(q.result); err == nil {
			if os.Getenv("CI") == "" {
				t.Skip("no Chrome here: ", string(failed))
			}
			t.Fatalf("QA on %s failed: %s", q.Short(), failed)
		}
		_, err := os.Stat(launch)
		return err == nil
	})
	script, _ := os.ReadFile(launch)
	_, config, _ := strings.Cut(string(script), "--mcp-config '")
	config, _, _ = strings.Cut(config, "'")
	data, _ := os.ReadFile(config)
	_, browserURL, _ = strings.Cut(string(data), "--browser-url=")
	browserURL, _, _ = strings.Cut(browserURL, `"`)
	pid, _ := os.ReadFile(filepath.Join(q.f.Worktree, "stack.pid"))
	stack, _ = strconv.Atoi(strings.TrimSpace(string(pid)))
	return filepath.Dir(config), browserURL, stack
}

func until(t *testing.T, what string, wait time.Duration, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(wait)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func browserAnswers(url string) bool {
	resp, err := http.Get(url + "/json/version")
	if err == nil {
		resp.Body.Close()
	}
	return err == nil
}

func running(pid int) bool {
	out, err := exec.Command("ps", "-o", "stat=", "-p", strconv.Itoa(pid)).Output()
	stat := strings.TrimSpace(string(out))
	return err == nil && stat != "" && !strings.HasPrefix(stat, "Z")
}

func cleanedUp(t *testing.T, who, dir, browserURL string, stack int) {
	t.Helper()
	until(t, who+"'s browser, stack and profile to go", 10*time.Second, func() bool {
		_, err := os.Stat(dir)
		return errors.Is(err, os.ErrNotExist) && !browserAnswers(browserURL) && !running(stack)
	})
}

func TestTwoNodesWithoutADisplayEachRunOnlyTheirOwnQAAndRemoveItOnPauseAndAfterACrash(t *testing.T) {
	if _, found := review.ChromeBinary(""); !found && os.Getenv("CI") == "" {
		t.Skip("no Chrome here")
	}
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "npx"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("DISPLAY", "")
	t.Setenv("WAYLAND_DISPLAY", "")
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.Write([]byte(`<!doctype html><html><body><h1>Contacts</h1><script>console.error("export failed to load")</script></body></html>`))
	}))
	t.Cleanup(app.Close)
	_, port, _ := net.SplitHostPort(app.Listener.Addr().String())
	tn := nodetest.New(t)
	start := func(machine, slug string) *qaNode {
		repo := t.TempDir()
		worktree := filepath.Join(repo, "wt")
		config := "qa:\n  mode: browser\n  web_port: " + port + "\n  health: " + app.URL + "/health\n  health_status: 204\n  seed: none\n  sign_in: none\n  start: 'echo $$ > stack.pid; exec sleep 600'\n"
		for _, err := range []error{os.MkdirAll(worktree, 0o755), os.MkdirAll(filepath.Join(repo, ".factory"), 0o755), os.WriteFile(state.RepoConfigPath(repo), []byte(config), 0o644)} {
			if err != nil {
				t.Fatal(err)
			}
		}
		q := &qaNode{Node: tn.Machine(machine).Start(), f: factory.Factory{Repo: repo, Slug: slug, Dir: filepath.Join(worktree, ".claude", "factory", slug), Worktree: worktree, State: &state.State{ID: slug}}}
		q.boot(t)
		var reply host.StageReply
		if err := q.Local.Request(context.Background(), "stage", uuid.NewString(), host.StageRequest{Role: "qa", Factory: slug, Text: "Test the export."}, &reply); err != nil {
			t.Fatal(err)
		}
		q.result = reply.Result
		return q
	}
	alpha, beta := start("alpha", "csv"), start("beta", "pdf")
	alphaDir, alphaBrowser, alphaStack := alpha.job(t)
	betaDir, betaBrowser, betaStack := beta.job(t)
	switch {
	case alphaBrowser == betaBrowser || alphaDir == betaDir:
		t.Fatalf("both nodes' QA share %s; want a browser and profile each", alphaBrowser)
	case !browserAnswers(alphaBrowser) || !browserAnswers(betaBrowser):
		t.Fatal("a node's QA browser does not answer after the stage command that started it returned")
	}
	if _, err := os.Stat(state.LaunchScriptPath(alpha.Dir, beta.f.Repo, beta.f.Slug, "qa")); err == nil {
		t.Fatal("alpha opened a QA pane for beta's factory")
	}

	if err := alpha.Local.Request(context.Background(), "pause", uuid.NewString(), struct{ Key string }{alpha.f.Key()}, nil); err != nil {
		t.Fatal(err)
	}
	cleanedUp(t, "paused alpha", alphaDir, alphaBrowser, alphaStack)
	runs, _ := state.LoadStageRuns(state.StageRunsPath(alpha.Dir, alpha.f.Repo, alpha.f.Slug))
	run, _ := runs.Latest("qa")
	evidence := host.EvidenceDir(alpha.f, run)
	console, _ := os.ReadFile(filepath.Join(evidence, review.ConsoleLog))
	shot, _ := os.ReadFile(filepath.Join(evidence, review.FinalShot))
	if !strings.Contains(string(console), "export failed to load") || !bytes.HasPrefix(shot, []byte("\x89PNG")) {
		t.Fatalf("the paused QA kept console %q and a %d-byte screenshot in %s; want the page's error and a PNG outside the removed profile", console, len(shot), evidence)
	}
	if !browserAnswers(betaBrowser) {
		t.Fatal("pausing alpha's factory closed beta's browser")
	}

	beta.stop()
	beta.boot(t)
	cleanedUp(t, "beta after its node restarted", betaDir, betaBrowser, betaStack)
	until(t, "beta's QA result", 10*time.Second, func() bool {
		result, _ := os.ReadFile(beta.result)
		return strings.Contains(string(result), host.FailRestart)
	})
}
