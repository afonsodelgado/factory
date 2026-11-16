package node_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/afonsodelgado/factory/internal/host"
	"github.com/afonsodelgado/factory/internal/node"
	"github.com/afonsodelgado/factory/internal/node/nodetest"
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
	macbook.Nodes.Repos = func() []string { return []string{cowork} }
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
	commit := func(join, challenge string, ledger node.Ledger) int {
		status, _, _ := tn.Request(coordinator, homelab.Machine, "POST", "/api/node/join/commit", "", node.Commit{Join: join, Challenge: challenge, Ledger: ledger})
		return status
	}

	first := uuid.NewString()
	challenge := prepare(first)
	if status := commit(first, "0"+challenge[1:], ledger); status == 200 {
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
	if homelab.Roster().ID != ledger.Roster {
		t.Error("the one accepted commit did not stage the coordinator's list")
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
