package node_test

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
	cdp "github.com/chromedp/chromedp/remote"
	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/google/uuid"

	"github.com/afonsodelgado/factory/internal/factory"
	"github.com/afonsodelgado/factory/internal/gitclone"
	"github.com/afonsodelgado/factory/internal/host"
	"github.com/afonsodelgado/factory/internal/node"
	"github.com/afonsodelgado/factory/internal/node/nodetest"
	"github.com/afonsodelgado/factory/internal/phone"
	"github.com/afonsodelgado/factory/internal/review"
	"github.com/afonsodelgado/factory/internal/state"
	"github.com/afonsodelgado/factory/internal/tmux"
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

func TestMovingThePhoneHomeOnOneNodeReachesEveryNodesList(t *testing.T) {
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
	var moved node.Roster
	if err := studio.Local.Do(context.Background(), "POST", "/api/nodes/home", node.HomeRequest{Operation: uuid.NewString(), Node: homelab.ID}, &moved); err != nil {
		t.Fatal(err)
	}
	tn.Settle(macbook, homelab, studio)
	for _, n := range []*nodetest.Node{macbook, homelab, studio} {
		roster := n.Roster()
		if n.Nodes.Dir.Home() != "https://"+homelab.Name || roster.Home != "https://"+homelab.Name || !slices.Equal(roster.PreviousHomes, []string{"https://" + macbook.Name}) {
			t.Errorf("%s has the home %s (roster %s, previous %v); want homelab, with macbook's address sending phones there", n.Short(), n.Nodes.Dir.Home(), roster.Home, roster.PreviousHomes)
		}
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

func TestExplicitReenrollmentAfterMutualRemovalPreservesTheAppHome(t *testing.T) {
	for _, door := range []string{"add", "join", "third node"} {
		t.Run(door, func(t *testing.T) {
			tn := nodetest.New(t)
			macbook := tn.Machine("macbook").Start()
			homelab := tn.Machine("homelab").Start()
			if _, err := macbook.Add("homelab"); err != nil {
				t.Fatal(err)
			}
			members := []*nodetest.Node{macbook, homelab}
			coordinator := macbook
			want := names("homelab", "macbook")
			if door == "third node" {
				coordinator = tn.Machine("studio").Start()
				if _, err := macbook.Add("studio"); err != nil {
					t.Fatal(err)
				}
				members = append(members, coordinator)
				want = names("homelab", "macbook", "studio")
			}
			tn.Settle(members...)
			home := macbook.Roster().Home
			homelab.SetOnline(false)
			if err := macbook.Remove(homelab.ID); err != nil {
				t.Fatal(err)
			}
			if err := homelab.Remove(macbook.ID); err != nil {
				t.Fatal(err)
			}
			tn.Settle(macbook, coordinator)
			homelab.SetOnline(true)
			var err error
			if door == "join" {
				_, err = homelab.Join("macbook")
			} else {
				_, err = coordinator.Add("homelab")
			}
			if err != nil {
				t.Fatal(err)
			}
			tn.Settle(members...)
			for _, n := range members {
				if got := n.Names(); !slices.Equal(got, want) || n.Roster().Removed || n.Roster().Home != home {
					t.Errorf("explicit re-enrollment left %s with %v and roster %+v", n.Short(), got, n.Roster())
				}
			}
		})
	}
}

func TestReenrollmentNamesEveryMemberRestoredFromTheReturningHistory(t *testing.T) {
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
	if err := macbook.Remove(homelab.ID); err != nil {
		t.Fatal(err)
	}
	if err := homelab.Remove(studio.ID); err != nil {
		t.Fatal(err)
	}
	tn.Settle(macbook, studio)
	homelab.SetOnline(true)
	var preview node.CheckReport
	if err := macbook.Local.Do(context.Background(), http.MethodPost, "/api/nodes/check", map[string]string{"name": "homelab"}, &preview); err != nil {
		t.Fatal(err)
	}
	result, err := macbook.Add("homelab")
	if err != nil {
		t.Fatal(err)
	}
	for _, report := range []node.CheckReport{preview, result.Report} {
		if !slices.ContainsFunc(report.Checks, func(c node.Check) bool {
			return c.Name == "re-enroll" && strings.Contains(c.Detail, "studio") && strings.Contains(c.Detail, "homelab") && strings.Contains(c.Detail, "remove it again")
		}) {
			t.Errorf("re-enrollment did not explain studio's restoration: %+v", report.Checks)
		}
	}
	tn.Settle(macbook, homelab, studio)
	for _, n := range []*nodetest.Node{macbook, homelab, studio} {
		if !slices.Equal(n.Names(), names("homelab", "macbook", "studio")) || n.Roster().Removed {
			t.Errorf("%s lost a member after re-enrollment: %+v", n.Short(), n.Roster())
		}
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
	sleeper.SetHang(true)
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
		{"the home app asking a node to update itself", "POST", "/api/node/update", home, node.UpdateRequest{From: macbook.ID, Version: "v9.9.9"}, nil, 403, home},
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

func TestFactoryUpdateAsksEveryListedNodeAndNoOtherMachineCanTriggerOne(t *testing.T) {
	tn := nodetest.New(t)
	macbook := tn.Machine("macbook").Start()
	homelab := tn.Machine("homelab").Start()
	studio := tn.Machine("studio").Start()
	if _, err := macbook.Add("homelab"); err != nil {
		t.Fatal(err)
	}
	tn.Settle(macbook, homelab)
	var mu sync.Mutex
	var asked []string
	homelab.Nodes.Update = func(version string) error {
		mu.Lock()
		defer mu.Unlock()
		asked = append(asked, version)
		return nil
	}

	status, _, body := tn.Request(studio.Machine, homelab.Machine, "POST", "/api/node/update", "", node.UpdateRequest{From: studio.ID, Version: "v9.9.9"})
	if status != http.StatusForbidden {
		t.Errorf("a machine outside the list asked homelab to update and got %d: %s", status, body)
	}
	var updates node.Updates
	if err := macbook.Local.Do(context.Background(), http.MethodPost, "/api/nodes/update", node.UpdateAll{Version: "v9.9.9"}, &updates); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if !slices.Equal(asked, []string{"v9.9.9"}) || len(updates.Nodes) != 1 || updates.Nodes[0].Node != "homelab" {
		t.Errorf("factory update on macbook asked homelab for %v and reported %+v; want exactly one request for v9.9.9, from the listed node only", asked, updates.Nodes)
	}
}

func TestSharedSettingsReachEveryNodeAndAJoinerTakesTheListsOverItsOwn(t *testing.T) {
	const repo = "repo:git@github.com:acme/app.git"
	tn := nodetest.New(t)
	macbook := tn.Machine("macbook").Start()
	homelab := tn.Machine("homelab").Start()
	macbook.SetSetting("rules", "be terse")
	macbook.SetSetting(repo, "qa: {mode: browser}")
	homelab.SetCloneTime(3 * tn.ProbeWait)
	for _, draft := range []string{"homelab's first draft", "homelab's own draft"} {
		homelab.SetSetting("rules", draft)
		tn.Settle(homelab)
	}
	if _, err := macbook.Add("homelab"); err != nil {
		t.Fatal(err)
	}
	tn.Settle(macbook, homelab)

	if got := homelab.Setting("rules"); got != "be terse" {
		t.Errorf("homelab joined with rules %q; want the list's rules, or a joiner's own file overwrites every node", got)
	}
	if got := homelab.Setting(repo); got != "qa: {mode: browser}" || !slices.Equal(homelab.Clones(), []string{"git@github.com:acme/app.git"}) {
		t.Errorf("homelab has repo config %q and cloned %v; want the repo cloned with its config", got, homelab.Clones())
	}

	homelab.SetSetting("rules", "be terser")
	tn.Settle(macbook, homelab)
	if got := macbook.Setting("rules"); got != "be terser" {
		t.Errorf("an edit on homelab left macbook with %q", got)
	}
	revisions := func() map[string]int64 {
		r := map[string]int64{}
		for _, s := range macbook.Nodes.Settings.Records() {
			r[s.Key] = s.Revision
		}
		return r
	}
	before := revisions()
	tn.Settle(macbook, homelab)
	if after := revisions(); !maps.Equal(before, after) {
		t.Errorf("settling again changed the revisions from %v to %v, so the nodes keep sending the same settings back and forth", before, after)
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

func TestAttachingToAnotherNodesPaneStreamsOnlyThePreparedView(t *testing.T) {
	tn := nodetest.New(t)
	tn.Machine("macbook").Start()
	homelab := tn.Machine("homelab").Start()
	remote := node.Remote(homelab.Name, tn.Client(tn.Machine("laptop")))
	to := node.StreamTarget{Machine: homelab.Name, User: "factory"}
	if _, err := node.AttachCommand(context.Background(), remote, to, host.AttachRequest{}); err == nil {
		t.Error("an attach the node refused still produced a stream command")
	}
	args, err := node.AttachCommand(context.Background(), remote, to, host.AttachRequest{Key: "/r/app:csv", Role: "supervisor", Token: "%12"})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(args[1:3], []string{"host", "stream"}) || !slices.Contains(args, homelab.Name) || args[len(args)-1] != "view-/r/app:csv-%12" {
		t.Errorf("attach ran %v; want a stream from homelab of the view the node prepared", args)
	}
}

func TestNodesStreamEachOthersSessionsBothWaysAndNeverToABrowser(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		if os.Getenv("CI") == "" {
			t.Skip("tmux not installed")
		}
		t.Fatal(err)
	}
	t.Setenv("TMUX", "")
	tm := tmux.Client{Socket: "node-stream-" + uuid.NewString(), ConfigFile: "/dev/null"}
	t.Cleanup(func() { tm.Run("kill-server") })
	tn := nodetest.New(t)
	macbook, homelab := tn.Machine("macbook"), tn.Machine("homelab")
	macbook.Tmux, homelab.Tmux = &tm, &tm
	macbook.Start()
	homelab.Start()
	for _, pair := range [][2]*nodetest.Machine{{macbook, homelab}, {homelab, macbook}} {
		from, to := pair[0], pair[1]
		view := uuid.NewString()
		session := host.ViewSession(view)
		if _, err := tm.NewSession(session, "agent", 80, 24, `exec sh -c 'while read line; do echo "got $line"; done'`); err != nil {
			t.Fatal(err)
		}
		if _, err := tm.Run("set-option", "-t", session, "status", "off"); err != nil {
			t.Fatal(err)
		}
		address := "https://" + to.Name + "/api/node/stream/" + view
		if _, resp, err := websocket.Dial(context.Background(), address, &websocket.DialOptions{HTTPClient: tn.Client(from), HTTPHeader: http.Header{"Origin": {"https://" + to.Name}}}); err == nil || resp == nil || resp.StatusCode != http.StatusForbidden {
			t.Fatalf("%s opened %s's session for a page from its own origin", from.Short(), to.Short())
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		conn, err := node.DialStream(ctx, tn.Client(from), to.Name, view, node.Resize{Cols: 80, Rows: 24}, "xterm-256color")
		if err != nil {
			t.Fatalf("%s could not stream %s's session: %v", from.Short(), to.Short(), err)
		}
		var mu sync.Mutex
		var screen strings.Builder
		go func() {
			for {
				_, data, err := conn.Read(ctx)
				if err != nil {
					return
				}
				mu.Lock()
				screen.Write(data)
				mu.Unlock()
			}
		}()
		if err := conn.Write(ctx, websocket.MessageBinary, []byte("ping from "+from.Short()+"\r")); err != nil {
			t.Fatal(err)
		}
		eventually(t, "the answer to "+from.Short()+"'s typing", func() bool {
			mu.Lock()
			defer mu.Unlock()
			return strings.Contains(screen.String(), "got ping from "+from.Short())
		})
		if err := wsjson.Write(ctx, conn, node.Resize{Cols: 100, Rows: 30}); err != nil {
			t.Fatal(err)
		}
		eventually(t, from.Short()+"'s resize reaching the pane", func() bool {
			size, _ := tm.Display("="+session+":", "#{pane_width}x#{pane_height}")
			return size == "100x30"
		})
		conn.Close(websocket.StatusNormalClosure, "")
		eventually(t, "the attach client leaving "+to.Short()+"'s session", func() bool {
			clients, err := tm.Clients("=" + session)
			return err == nil && len(clients) == 0
		})
		if !tm.HasSession(session) {
			t.Errorf("closing %s's stream ended the session on %s", from.Short(), to.Short())
		}
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

func browserAnswersSoon(url string) bool {
	deadline := time.Now().Add(3 * time.Second)
	for !browserAnswers(url) {
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(50 * time.Millisecond)
	}
	return true
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
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	t.Setenv("CODEX_HOME", t.TempDir())
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
		q := &qaNode{Node: tn.Machine(machine).Start(), f: factory.Factory{Repo: repo, Slug: slug, Dir: filepath.Join(worktree, ".claude", "factory", slug), Worktree: worktree, State: &state.State{SchemaVersion: 2, ID: slug}}}
		q.boot(t)
		var reply host.StageReply
		q.f.State.Workflow = state.Workflow{ActiveRun: "qa-1", Pending: &state.Effect{ID: "qa-1", Kind: "stage", RunID: "qa-1", Role: "qa", Stage: "qa"}, Runs: []state.WorkflowRun{{ID: "qa-1", Role: "qa", Stage: "qa", Round: 1, Prompt: "stages/qa-1.prompt.md", Result: "stages/qa-1.result.json"}}}
		if err := q.Local.Request(context.Background(), "stage", uuid.NewString(), host.StageRequest{EffectID: "qa-1", Role: "qa", Factory: slug, Text: "Test the export."}, &reply); err != nil {
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
	case !browserAnswersSoon(alphaBrowser) || !browserAnswersSoon(betaBrowser):
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
	if !browserAnswersSoon(betaBrowser) {
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

type walkthroughs struct {
	t         *testing.T
	machine   *nodetest.Machine
	stacks    *node.Stacks
	exposure  *node.Exposure
	pages     *review.Server
	dir       string
	factories []review.Factory
}

func newWalkthroughs(t *testing.T) *walkthroughs {
	m := nodetest.New(t).Machine("alpha")
	m.ServeOn("443", "http://127.0.0.1:7418")
	m.ServeOn("8443", "http://127.0.0.1:3000")
	overlay, err := os.ReadFile("../../bundle/review/overlay.js")
	if err != nil {
		t.Fatal(err)
	}
	pages, err := review.New("tok", overlay)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pages.Close() })
	w := &walkthroughs{t: t, machine: m, pages: pages, dir: t.TempDir()}
	w.stacks = w.boot()
	return w
}

func (w *walkthroughs) boot() *node.Stacks {
	ts := node.Tailscale{Run: w.machine.Run}
	w.exposure = &node.Exposure{Tailscale: ts}
	w.exposure.Check(context.Background())
	stacks := &node.Stacks{Tailscale: ts, Gate: node.Gate{Owner: nodetest.Owner, Exposure: w.exposure}, Host: w.machine.Name, Origin: "https://" + w.machine.Name, Review: w.pages, StateDir: w.dir}
	w.t.Cleanup(func() { stacks.Close() })
	return stacks
}

const stackApp = "qa:\n  mode: browser\n  url: http://127.0.0.1:${FACTORY_WEB_PORT}/contacts?tab=all\n  health: http://127.0.0.1:${FACTORY_WEB_PORT}/health\n  health_status: 204\n  seed: none\n  sign_in: cat cookies.json\n  start: 'env > stack.env.tmp && mv stack.env.tmp stack.env; exec sleep 600'\n"

func (w *walkthroughs) factory(slug, config string) factory.Factory {
	repo := w.t.TempDir()
	worktree := filepath.Join(repo, "wt")
	f := factory.Factory{Repo: repo, Slug: slug, Dir: filepath.Join(worktree, ".claude", "factory", slug), Worktree: worktree, State: &state.State{SchemaVersion: 2, ID: slug}}
	cookies := `[{"name":"session","value":"s3cret-` + slug + `","domain":"localhost"}]`
	for _, err := range []error{os.MkdirAll(f.Dir, 0o755), os.MkdirAll(state.RepoDir(repo), 0o755), os.WriteFile(state.RepoConfigPath(repo), []byte(config), 0o644), os.WriteFile(filepath.Join(worktree, "cookies.json"), []byte(cookies), 0o644)} {
		if err != nil {
			w.t.Fatal(err)
		}
	}
	w.factories = append(w.factories, host.ReviewFactory(host.Item{Factory: f}))
	w.pages.SetFactories(w.factories)
	return f
}

func leaseOf(f factory.Factory) host.StackLease {
	return host.StackLease{Factory: f.Key(), Route: host.ReviewKey(f), Label: f.Label(), Repo: f.Repo, Slug: f.Slug, Worktree: f.Worktree, Page: review.App}
}

func (w *walkthroughs) launch(f factory.Factory) (host.StackStatus, map[string]string) {
	w.t.Helper()
	profile, err := state.ReadProfile(f.Repo)
	if err != nil {
		w.t.Fatal(err)
	}
	os.Remove(filepath.Join(f.Worktree, "stack.env"))
	done := make(chan host.StackStatus, 1)
	go func() { done <- w.stacks.Start(context.Background(), leaseOf(f), profile) }()
	env := serveApp(w.t, f.Worktree)
	return <-done, env
}

func (w *walkthroughs) start(f factory.Factory) (host.StackStatus, map[string]string) {
	w.t.Helper()
	status, env := w.launch(f)
	if status.State != host.StackReady {
		w.t.Fatalf("%s's stack is %s at %s: %s", f.Slug, status.State, status.Failure, status.Reason)
	}
	return status, env
}

func serveApp(t *testing.T, worktree string) map[string]string {
	t.Helper()
	var data []byte
	until(t, "the stack's start command", 10*time.Second, func() bool {
		var err error
		data, err = os.ReadFile(filepath.Join(worktree, "stack.env"))
		return err == nil
	})
	env := map[string]string{}
	for line := range strings.SplitSeq(string(data), "\n") {
		if name, value, ok := strings.Cut(line, "="); ok {
			env[name] = value
		}
	}
	ln, err := net.Listen("tcp", "127.0.0.1:"+env[node.WebPortEnv])
	if err != nil {
		t.Fatalf("the app cannot listen on the port the node gave it: %v", err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health":
			w.WriteHeader(http.StatusNoContent)
		case "/headers":
			json.NewEncoder(w).Encode(r.Header)
		case "/login":
			http.Redirect(w, r, "http://localhost:"+env[node.WebPortEnv]+"/home", http.StatusFound)
		case "/ws":
			conn, buf, _ := http.NewResponseController(w).Hijack()
			defer conn.Close()
			buf.WriteString("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n\r\n")
			buf.Flush()
			line, _ := buf.ReadString('\n')
			conn.Write([]byte("echo " + line))
		case "/contacts":
			w.Header().Set("Content-Type", "text/html")
			w.Write([]byte(`<!doctype html><html><body><h1>Contacts</h1><button id="export">Export</button></body></html>`))
		default:
			w.Write([]byte("app:" + r.URL.Path))
		}
	})}
	go server.Serve(ln)
	t.Cleanup(func() { server.Close() })
	return env
}

func (w *walkthroughs) proxyOf(s host.StackStatus) string {
	_, port, _ := strings.Cut(strings.TrimPrefix(s.Origin, "https://"), ":")
	return w.machine.Served()[port]
}

func (w *walkthroughs) ask(s host.StackStatus, method, path, login string, header ...string) *http.Response {
	w.t.Helper()
	req, _ := http.NewRequest(method, w.proxyOf(s)+path, nil)
	req.Host = strings.TrimPrefix(s.Origin, "https://")
	if login != "" {
		req.Header.Set(node.LoginHeader, login)
	}
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	client := http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		w.t.Fatal(err)
	}
	w.t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func text(resp *http.Response) string {
	data, _ := io.ReadAll(resp.Body)
	return string(data)
}

func TestTwoWalkthroughsGetTheirOwnTailnetPortsAndTheProxyPassesOnlyTheOwnerWithoutIdentityHeaders(t *testing.T) {
	w := newWalkthroughs(t)
	csv, pdf := w.factory("csv", stackApp), w.factory("pdf", stackApp)
	type started struct {
		status host.StackStatus
		env    map[string]string
	}
	both := make(chan started, 2)
	for _, f := range []factory.Factory{csv, pdf} {
		go func() {
			var result started
			defer func() { both <- result }()
			result.status, result.env = w.start(f)
		}()
	}
	a, b := <-both, <-both
	if t.Failed() {
		return
	}
	switch {
	case a.status.Origin == b.status.Origin || a.env[node.WebPortEnv] == b.env[node.WebPortEnv]:
		t.Fatalf("both walkthroughs share %s on app port %s; want a tailnet port and an app port each", a.status.Origin, a.env[node.WebPortEnv])
	case a.env[node.PublicOriginEnv] != a.status.Origin || !strings.HasSuffix(a.status.URL, "/contacts?tab=all"):
		t.Fatalf("the stack was told its public origin is %q and links %q; want %s and qa.url's page", a.env[node.PublicOriginEnv], a.status.URL, a.status.Origin)
	}
	for _, s := range []host.StackStatus{a.status, b.status} {
		port := strings.TrimPrefix(s.Origin, "https://"+w.machine.Name+":")
		if !slices.Contains(w.machine.ServeLog(), "serve --bg --https="+port+" "+w.proxyOf(s)) || port == "8443" {
			t.Fatalf("Serve log %v; want --https=%s mapped to the walkthrough's own loopback proxy, never the 8443 another tool holds", w.machine.ServeLog(), port)
		}
	}

	resp := w.ask(a.status, "GET", "/headers", nodetest.Owner, "Tailscale-User-Name", "Alice", "X-Factory-Pane-Token", "secret")
	var seen http.Header
	json.NewDecoder(resp.Body).Decode(&seen)
	for name := range seen {
		if strings.HasPrefix(name, "Tailscale-") || strings.HasPrefix(name, "X-Factory-") {
			t.Errorf("the app saw %s: %v; the proxy must strip identity and internal headers", name, seen[name])
		}
	}
	if got := seen.Get("X-Forwarded-Host"); got != strings.TrimPrefix(a.status.Origin, "https://") {
		t.Errorf("the app saw X-Forwarded-Host %q; want the walkthrough's tailnet host", got)
	}
	if resp := w.ask(a.status, "GET", "/headers", "bob@example.com"); resp.StatusCode != http.StatusForbidden {
		t.Errorf("another tailnet user got %d; want 403", resp.StatusCode)
	}
	if resp := w.ask(a.status, "GET", "/headers", ""); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("a request without a Tailscale login got %d; want 401", resp.StatusCode)
	}
	if resp := w.ask(a.status, "GET", "/api/snapshot", nodetest.Owner); text(resp) != "app:/api/snapshot" {
		t.Error("the walkthrough's origin reached something other than its own app for /api/snapshot; the control API must not live there")
	}
	if got := w.ask(a.status, "GET", "/login", nodetest.Owner).Header.Get("Location"); got != a.status.Origin+"/home" {
		t.Errorf("the app's redirect reached the browser as %q; want it on the walkthrough's origin", got)
	}
	if page := text(w.ask(a.status, "GET", "/contacts", nodetest.Owner)); !strings.Contains(page, review.AppPrefix+"/overlay.js") {
		t.Errorf("the app page came through without the review overlay:\n%s", page)
	}

	conn, err := net.Dial("tcp", strings.TrimPrefix(w.proxyOf(a.status), "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	fmt.Fprintf(conn, "GET /ws HTTP/1.1\r\nHost: %s\r\n%s: %s\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n\r\n", strings.TrimPrefix(a.status.Origin, "https://"), node.LoginHeader, nodetest.Owner)
	upgraded, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil || upgraded.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("a WebSocket through the proxy got %v, %v; want the app's upgrade", upgraded, err)
	}
	conn.Write([]byte("ping\n"))
	if echo, _ := io.ReadAll(conn); string(echo) != "echo ping\n" {
		t.Errorf("the upgraded connection carried back %q; want the app's echo", echo)
	}

	w.machine.SetFunnel(true)
	w.exposure.Check(context.Background())
	if resp := w.ask(a.status, "GET", "/headers", nodetest.Owner); resp.StatusCode != http.StatusForbidden || !strings.Contains(text(resp), "Funnel") {
		t.Errorf("with Funnel on the walkthrough answered %d; want the node's Funnel refusal", resp.StatusCode)
	}
}

func TestThePhoneSignInLinkWorksOnceAndRefusesCookiesItCannotHold(t *testing.T) {
	w := newWalkthroughs(t)
	csv := w.factory("csv", stackApp)
	status, _ := w.start(csv)
	opener := http.NewServeMux()
	opener.HandleFunc("GET /w/{route}", w.stacks.Open)
	open := func() string {
		rec := httptest.NewRecorder()
		opener.ServeHTTP(rec, httptest.NewRequest("GET", status.Href, nil))
		next := rec.Header().Get("Location")
		if rec.Code != http.StatusSeeOther || !strings.HasPrefix(next, status.Origin+review.AppPrefix+"/sign-in?ticket=") {
			t.Fatalf("the walkthrough link answered %d to %q; want a one-use sign-in on %s", rec.Code, next, status.Origin)
		}
		return strings.TrimPrefix(next, status.Origin)
	}

	signIn := open()
	resp := w.ask(status, "GET", signIn, nodetest.Owner)
	cookies := resp.Cookies()
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/contacts?tab=all" || len(cookies) != 1 || cookies[0].Value != "s3cret-csv" || !cookies[0].Secure {
		t.Fatalf("the sign-in answered %d to %q with %v; want a secure session cookie from qa.sign_in and the app page", resp.StatusCode, resp.Header.Get("Location"), cookies)
	}
	if again := w.ask(status, "GET", signIn, nodetest.Owner); again.StatusCode != http.StatusGone || len(again.Cookies()) > 0 {
		t.Errorf("the same sign-in link answered %d with %v the second time; want it refused", again.StatusCode, again.Cookies())
	}

	sso := `[{"name":"sso","value":"x","domain":".example.com"}]`
	os.WriteFile(filepath.Join(csv.Worktree, "cookies.json"), []byte(sso), 0o644)
	refused := w.ask(status, "GET", open(), nodetest.Owner)
	if body := text(refused); refused.StatusCode != http.StatusConflict || !strings.Contains(body, host.FailPhoneSignIn) || len(refused.Cookies()) > 0 {
		t.Errorf("a sign-in for another site answered %d with %v: %s; want the named refusal and no cookie", refused.StatusCode, refused.Cookies(), body)
	}

	pdf := w.factory("pdf", stackApp)
	os.WriteFile(filepath.Join(pdf.Worktree, "cookies.json"), []byte(sso), 0o644)
	if failed, _ := w.launch(pdf); failed.Failure != host.FailPhoneSignIn || w.proxyOf(failed) != "" {
		t.Errorf("a stack whose sign-in no phone can hold became %s at %q; want it refused by name before Serve publishes it", failed.State, failed.Failure)
	}
}

func TestACommentFromAnotherFactorysWalkthroughIsRefused(t *testing.T) {
	w := newWalkthroughs(t)
	csv, pdf := w.factory("csv", stackApp), w.factory("pdf", stackApp)
	a, _ := w.start(csv)
	b, _ := w.start(pdf)
	comment := func(origin string) int {
		req, _ := http.NewRequest("POST", w.proxyOf(a)+review.AppPrefix+"/api/threads", strings.NewReader(`{"page":"app","intent":"fix","text":"Make Export primary.","anchor":{"url":"`+a.URL+`"}}`))
		req.Host = strings.TrimPrefix(a.Origin, "https://")
		req.Header.Set(node.LoginHeader, nodetest.Owner)
		req.Header.Set("Origin", origin)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if status := comment(b.Origin); status != http.StatusForbidden {
		t.Fatalf("pdf's walkthrough page commented on csv with %d; want 403", status)
	}
	if status := comment(a.Origin); status != http.StatusOK {
		t.Fatalf("csv's own walkthrough page could not comment: %d", status)
	}
	pdfStore, _ := w.pages.Store(w.factories[1])
	pdfStore.Create(review.NewThread{Page: review.App, Intent: "fix", Text: "pdf only"})
	if resp := w.ask(a, "GET", review.AppPrefix+"/api/%2e%2e/%2e%2e/r/"+host.ReviewKey(pdf)+"/_api/threads?page=app", nodetest.Owner); strings.Contains(text(resp), "pdf only") {
		t.Fatal("csv's walkthrough origin read pdf's threads through a dot-segment path")
	}
	pdfStore.Delete(pdfStore.Threads(review.App)[0].ID)
	csvStore, _ := w.pages.Store(w.factories[0])
	if got := csvStore.Threads(review.App); len(got) != 1 || len(pdfStore.Threads(review.App)) != 0 {
		t.Errorf("csv holds %d app threads and pdf %d; want the one comment on csv only", len(got), len(pdfStore.Threads(review.App)))
	}
}

type stackRuntime struct{ queue node.Queue }

func (w *walkthroughs) runtime(fs ...factory.Factory) *stackRuntime {
	queue := node.NewQueue()
	ctx, cancel := context.WithCancel(context.Background())
	r := host.New(host.Runtime{Config: &state.Config{StateDir: w.t.TempDir()}, Engine: qaEngine{}, Jobs: &node.Jobs{Queue: queue, Ctx: ctx}, Stacks: w.stacks, Pages: w.pages})
	var items []host.Item
	for _, f := range fs {
		items = append(items, host.Item{Factory: f})
	}
	r.SetSnapshot(host.Snapshot{At: time.Now(), Items: items})
	stopped := make(chan struct{})
	go func() { r.Run(ctx, queue.Events()); close(stopped) }()
	w.t.Cleanup(func() { cancel(); <-stopped })
	return &stackRuntime{queue: queue}
}

func (s *stackRuntime) call(t *testing.T, kind string, request, into any) {
	t.Helper()
	data, _ := json.Marshal(request)
	answer, err := s.queue.Call(context.Background(), host.Message{T: kind, Data: data})
	if err == nil && answer.T == "error" {
		err = errors.New(answer.Error)
	}
	if err != nil {
		t.Fatal(err)
	}
	if into != nil {
		json.Unmarshal(answer.Data, into)
	}
}

func (s *stackRuntime) walkthrough(t *testing.T, key string) (host.LocalFactory, bool) {
	var local host.Local
	s.call(t, "local", nil, &local)
	for _, f := range local.Factories {
		if f.Key == key && f.ReviewStack != nil && f.ReviewStack.State != host.StackStarting {
			return f, true
		}
	}
	return host.LocalFactory{}, false
}

func TestAStackThatExitsBeforeItAnswersIsNamedOnTheFactory(t *testing.T) {
	w := newWalkthroughs(t)
	csv := w.factory("csv", strings.Replace(stackApp, "exec sleep 600", "exit 3", 1))
	rt := w.runtime(csv)
	rt.call(t, "review", host.ReviewRequest{Action: "stack", Factory: host.ReviewKey(csv), Page: review.App}, nil)
	var f host.LocalFactory
	until(t, "the stack's outcome in the snapshot", 10*time.Second, func() (ok bool) {
		f, ok = rt.walkthrough(t, csv.Key())
		return ok
	})
	item := slices.IndexFunc(f.Items, func(i host.LocalItem) bool { return i.Kind == "stack" })
	if f.ReviewStack.Failure != host.FailStack || item < 0 || !strings.Contains(f.Items[item].Title, host.FailStack) {
		t.Fatalf("the snapshot shows walkthrough %+v and items %+v; want the stack_start failure on the factory", f.ReviewStack, f.Items)
	}
	if served := w.machine.Served(); len(served) != 2 {
		t.Errorf("a failed stack left Serve mappings %v; want only the two that were there before", served)
	}
}

func TestPauseDoneAndARestartRemoveOnlyTheWalkthroughsOwnServeMappings(t *testing.T) {
	w := newWalkthroughs(t)
	csv, pdf, xls := w.factory("csv", stackApp), w.factory("pdf", stackApp), w.factory("xls", stackApp)
	rt := w.runtime(csv, pdf, xls)
	port := func(s host.StackStatus) string { return strings.TrimPrefix(s.Origin, "https://"+w.machine.Name+":") }
	paused, _ := w.start(csv)
	rt.call(t, "pause", struct{ Key string }{csv.Key()}, nil)
	if _, still := w.machine.Served()[port(paused)]; still {
		t.Fatalf("pausing csv left its mapping: %v", w.machine.Served())
	}

	finished, _ := w.start(pdf)
	done := host.New(host.Runtime{Config: &state.Config{StateDir: t.TempDir()}, Jobs: &node.Jobs{Queue: node.NewQueue(), Ctx: context.Background()}, Stacks: w.stacks})
	done.SetSnapshot(host.Snapshot{Items: []host.Item{{Factory: pdf, Status: factory.Done}, {Factory: xls}}})
	done.ReleaseFinishedStacks()
	if _, still := w.machine.Served()[port(finished)]; still {
		t.Fatalf("a done factory kept its walkthrough mapping: %v", w.machine.Served())
	}

	ours, _ := w.start(xls)
	taken, _ := w.start(pdf)
	w.machine.ServeOn(port(taken), "http://127.0.0.1:9999")
	recovered := w.boot().Recover()
	served := w.machine.Served()
	if _, still := served[port(ours)]; still || served[port(taken)] != "http://127.0.0.1:9999" || served["443"] == "" || served["8443"] == "" {
		t.Fatalf("after the restart Serve holds %v; want xls's mapping gone and everything the node does not own untouched", served)
	}
	if len(recovered) != 2 {
		t.Errorf("the restart reported %d stopped walkthroughs; want 2", len(recovered))
	}
}

func TestABrowserCommentsOnAWalkthroughPageThroughItsTailnetPortWithoutATunnel(t *testing.T) {
	w := newWalkthroughs(t)
	csv := w.factory("csv", stackApp)
	status, _ := w.start(csv)
	proxy, _ := url.Parse(w.proxyOf(status))
	var serve *httptest.Server
	serve = httptest.NewServer(&httputil.ReverseProxy{Rewrite: func(pr *httputil.ProxyRequest) {
		pr.SetURL(proxy)
		pr.Out.Host = strings.TrimPrefix(status.Origin, "https://")
		pr.Out.Header.Set(node.LoginHeader, nodetest.Owner)
		if pr.In.Header.Get("Origin") == serve.URL {
			pr.Out.Header.Set("Origin", status.Origin)
		}
	}})
	t.Cleanup(serve.Close)
	chrome, err := review.LaunchHeadless(filepath.Join(t.TempDir(), "profile"), "", goruntime.GOOS == "linux")
	if err != nil {
		if os.Getenv("CI") == "" {
			t.Skip("no Chrome here: ", err)
		}
		t.Fatal(err)
	}
	t.Cleanup(func() { chrome.Close() })
	alloc, cancelAlloc := cdp.NewAllocator(context.Background(), chrome.URL)
	defer cancelAlloc()
	tab, cancelTab := chromedp.NewContext(alloc)
	defer cancelTab()
	ctx, cancel := context.WithTimeout(tab, 30*time.Second)
	defer cancel()
	if err := chromedp.Do(ctx, chromedp.Navigate(serve.URL+"/contacts")); err != nil {
		t.Fatal(err)
	}
	overlay := `document.querySelector("factory-review")?.shadowRoot`
	run := func(what, js string) {
		t.Helper()
		until(t, what, 10*time.Second, func() bool {
			done, err := chromedp.Run(ctx, chromedp.Evaluate[bool](js))
			return err == nil && done
		})
	}
	run("the overlay's comment panel", `(() => { const b = `+overlay+`?.querySelector('[data-action="panel"]'); b?.click(); return !!b })()`)
	run("a page comment", `(() => { const b = `+overlay+`.querySelector('[data-action="page-comment"]'); b?.click(); return !!b })()`)
	run("the saved comment", `(() => { const f = `+overlay+`.querySelector(".composer"); if (!f || f.hidden) return false; f.querySelector("textarea").value = "Make Export primary."; f.requestSubmit(); return true })()`)

	store, _ := w.pages.Store(w.factories[0])
	until(t, "the comment in csv's review threads", 10*time.Second, func() bool {
		threads := store.Threads(review.App)
		return len(threads) == 1 && threads[0].Messages[0].Text == "Make Export primary."
	})
}

const (
	appRemote  = "https://git.example/acme/app.git"
	goneRemote = "https://git.example/acme/gone.git"
)

type gitFixture struct {
	t        *testing.T
	repo     string
	worktree string
	dir      string
}

func (g gitFixture) run(dir string, args ...string) string {
	g.t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		g.t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func (g gitFixture) commit(dir, file string) {
	g.t.Helper()
	if err := os.WriteFile(filepath.Join(dir, file), []byte(file+"\n"), 0o644); err != nil {
		g.t.Fatal(err)
	}
	g.run(dir, "add", file)
	g.run(dir, "commit", "-qm", file)
}

func newGitFixture(t *testing.T) gitFixture {
	t.Helper()
	root := t.TempDir()
	bare := filepath.Join(root, "remote.git")
	gitconfig := filepath.Join(root, "gitconfig")
	config := fmt.Sprintf("[user]\n\tname = factory\n\temail = factory@example.com\n[init]\n\tdefaultBranch = main\n[url %q]\n\tinsteadOf = %s\n[url %q]\n\tinsteadOf = %s\n", bare, appRemote, filepath.Join(root, "missing.git"), goneRemote)
	if err := os.WriteFile(gitconfig, []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", root)
	t.Setenv("GIT_CONFIG_GLOBAL", gitconfig)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(root, "claude"))
	t.Setenv("CODEX_HOME", filepath.Join(root, "codex"))
	g := gitFixture{t: t, repo: filepath.Join(root, "app")}
	g.run(root, "init", "-q", "--bare", bare)
	g.run(root, "init", "-q", g.repo)
	g.commit(g.repo, "README.md")
	g.run(g.repo, "remote", "add", "origin", appRemote)
	g.run(g.repo, "push", "-q", "origin", "main")
	g.worktree = filepath.Join(factory.WorktreesDir(g.repo), "csv")
	g.run(g.repo, "worktree", "add", "-q", "-b", "worktree-csv", g.worktree)
	g.dir = filepath.Join(g.worktree, ".claude", "factory", "csv")
	return g
}

func writeFiles(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

type movableRuntime struct {
	movable    host.Movable
	supervisor atomic.Value
}

func (r *movableRuntime) Call(_ context.Context, m host.Message) (host.Message, error) {
	var value any = host.Local{Factories: []host.LocalFactory{}}
	if m.T == "movable" || m.T == "checkpoint" {
		answer := r.movable
		if m.T == "checkpoint" {
			answer.Waiting, _ = r.supervisor.Load().(string)
		}
		value = answer
	}
	data, _ := json.Marshal(value)
	return host.Message{T: "done", Data: data}, nil
}

func cloneWithOwnCredentials(stateDir string) func(context.Context, string) (string, error) {
	return func(ctx context.Context, url string) (string, error) { return gitclone.Ensure(ctx, stateDir, url) }
}

func TestACheckpointWithAStackEvidenceAndThreadsStagesOnAnotherNodeWithoutAConversation(t *testing.T) {
	g := newGitFixture(t)
	g.run(g.worktree, "checkout", "-q", "-b", "csv-1")
	g.commit(g.worktree, "export.go")
	g.run(g.worktree, "checkout", "-q", "-b", "csv-2")
	g.commit(g.worktree, "export_test.go")
	op := uuid.NewString()
	stateJSON := fmt.Sprintf(`{"schema_version":2,"id":"csv","branch":"worktree-csv","session_id":"old-session","prs":[{"number":11,"branch":"csv-1"},{"number":12,"branch":"csv-2"}],"stages":{"design":{"artifacts":[{"url":"design/variant-a.html"}]}},"checkpoint":{"operation":%q,"status":"ready"}}`, op)
	factoryFiles := map[string]string{
		"state.json":            stateJSON,
		"brief.md":              "Export contacts as CSV.",
		"design/variant-a.html": "<h1>Export</h1>",
		"review/threads.json":   `[{"id":"t1","page":"design/variant-a.html","messages":[{"text":"Make Export primary."}]}]`,
		"review/shots/t1.png":   "png",
		"review/stack.json":     `{"cookies":[{"name":"session","value":"secret"}]}`,
		".heartbeat":            "",
	}
	writeFiles(t, g.dir, factoryFiles)
	writeFiles(t, filepath.Join(g.worktree, ".claude", "qa", "csv"), map[string]string{"qa-1/console.txt": "no errors", "qa-1/export.png": "png"})
	writeFiles(t, g.repo, map[string]string{".factory/config.yml": "qa:\n  mode: terminal\n"})
	writeFiles(t, os.Getenv("CLAUDE_CONFIG_DIR"), map[string]string{"projects/csv/old-session.jsonl": `{"role":"user"}`})

	tn := nodetest.New(t)
	macbook := tn.Machine("macbook").Start()
	homelab := tn.Machine("homelab").Start()
	if _, err := macbook.Add("homelab"); err != nil {
		t.Fatal(err)
	}
	homelab.Nodes.Clone = cloneWithOwnCredentials(homelab.Dir)
	incarnation := uuid.NewString()
	runtime := &movableRuntime{movable: host.Movable{
		Key: g.repo + ":csv", Incarnation: incarnation, ID: "csv", Slug: "csv", Repo: g.repo, Worktree: g.worktree, Dir: g.dir,
		Evidence: filepath.Join(g.worktree, ".claude", "qa", "csv"), Branch: "worktree-csv",
		PRs: []state.PR{{Number: 11, Branch: "csv-1"}, {Number: 12, Branch: "csv-2"}},
	}}
	runtime.supervisor.Store("waiting for the supervisor of csv to write checkpoint " + op)
	macbook.Runtime.Behind(runtime)

	waiting, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	if _, err := macbook.Transfers.Stage(waiting, op, g.repo+":csv", "homelab"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("staging while the supervisor had not checkpointed answered %v; want it to wait", err)
	}
	runtime.supervisor.Store("")
	transfer, err := macbook.Transfers.Stage(context.Background(), op, g.repo+":csv", "homelab")
	if err != nil || transfer.Phase != node.PhaseStaged {
		t.Fatalf("staging csv on homelab answered %+v, %v", transfer, err)
	}

	staged := node.StagedDir(homelab.Dir, incarnation)
	var manifest node.Manifest
	data, err := os.ReadFile(filepath.Join(staged, "manifest.json"))
	if err != nil || json.Unmarshal(data, &manifest) != nil {
		t.Fatalf("homelab has no staged manifest: %v", err)
	}
	var got []string
	for _, f := range manifest.Files {
		got = append(got, f.Path)
		if content, err := os.ReadFile(filepath.Join(staged, "files", filepath.FromSlash(f.Path))); err != nil || int64(len(content)) != f.Size {
			t.Errorf("%s was not staged as listed: %v", f.Path, err)
		}
	}
	slices.Sort(got)
	want := []string{"config/config.yml", "evidence/qa-1/console.txt", "evidence/qa-1/export.png", "factory/brief.md", "factory/design/variant-a.html", "factory/review/shots/t1.png", "factory/review/threads.json", "factory/state.json"}
	if !slices.Equal(got, want) {
		t.Errorf("homelab staged %v; want only the factory's files, its QA evidence and the repo config, without generated cookies or a conversation", got)
	}
	if threads, _ := os.ReadFile(filepath.Join(staged, "files", "factory", "review", "threads.json")); string(threads) != factoryFiles["review/threads.json"] {
		t.Errorf("the review threads arrived as %q", threads)
	}
	clone := gitclone.Dir(homelab.Dir, appRemote)
	g.run(clone, "fetch", "-q", "origin")
	wantStack := []node.StackBranch{
		{Branch: "csv-1", PR: 11, SHA: g.run(g.worktree, "rev-parse", "csv-1")},
		{Branch: "csv-2", PR: 12, Base: "csv-1", SHA: g.run(g.worktree, "rev-parse", "csv-2")},
		{Branch: "worktree-csv", SHA: g.run(g.worktree, "rev-parse", "worktree-csv")},
	}
	if !slices.Equal(manifest.Stack, wantStack) {
		t.Errorf("the manifest records the stack as %+v; want %+v", manifest.Stack, wantStack)
	}
	for _, b := range wantStack {
		if sha := g.run(clone, "rev-parse", "origin/"+b.Branch); sha != b.SHA {
			t.Errorf("homelab's own clone sees %s at %s, not %s", b.Branch, sha, b.SHA)
		}
	}
	retained, err := os.Stat(node.CheckpointPath(macbook.Dir, incarnation))
	if err != nil || retained.Mode().Perm() != 0o600 {
		t.Errorf("macbook did not keep its last checkpoint privately: %v", err)
	}
}

func TestACheckpointRefusesSecretBearingFilesAndLeavesGeneratedCookiesBehind(t *testing.T) {
	op := uuid.NewString()
	stateJSON := fmt.Sprintf(`{"schema_version":2,"id":"csv","checkpoint":{"operation":%q,"status":"ready"}}`, op)
	for _, secret := range []string{".env", "review/cookies.json", "evidence:qa-1/profile/Default/Preferences", "notes/sessions/old.jsonl"} {
		t.Run(secret, func(t *testing.T) {
			root := t.TempDir()
			movable := host.Movable{Dir: filepath.Join(root, "factory"), Evidence: filepath.Join(root, "qa"), Repo: root}
			writeFiles(t, movable.Dir, map[string]string{"state.json": stateJSON})
			place, name := movable.Dir, secret
			if rest, inEvidence := strings.CutPrefix(secret, "evidence:"); inEvidence {
				place, name = movable.Evidence, rest
			}
			writeFiles(t, place, map[string]string{name: "x"})
			_, err := node.Capture(movable, node.Manifest{}, node.DefaultLimits)
			if err == nil || !strings.Contains(err.Error(), filepath.FromSlash(name)) {
				t.Errorf("a checkpoint holding %s answered %v; want a refusal naming it", secret, err)
			}
		})
	}
	linked := t.TempDir()
	writeFiles(t, linked, map[string]string{"state.json": stateJSON})
	if err := os.Symlink(filepath.Join(os.Getenv("HOME"), ".ssh", "id_ed25519"), filepath.Join(linked, "plan.md")); err != nil {
		t.Fatal(err)
	}
	if _, err := node.Capture(host.Movable{Dir: linked, Repo: t.TempDir()}, node.Manifest{}, node.DefaultLimits); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Errorf("a checkpoint holding a symlink answered %v; want a refusal", err)
	}
	root := t.TempDir()
	writeFiles(t, root, map[string]string{"state.json": stateJSON, "review/stack.json": `{"cookies":[]}`})
	cp, err := node.Capture(host.Movable{Dir: root, Repo: t.TempDir()}, node.Manifest{}, node.DefaultLimits)
	if err != nil || len(cp.Files) != 1 {
		t.Errorf("a factory with a generated stack.json captured %d files, %v; want only state.json", len(cp.Files), err)
	}
}

func forgedCheckpoint(source string, files map[string]string) node.Checkpoint {
	op := uuid.NewString()
	cp := node.Checkpoint{Manifest: node.Manifest{Schema: node.CheckpointSchema, Operation: op, Incarnation: uuid.NewString(), Factory: "csv", Slug: "csv", Source: source, GitURL: appRemote}}
	cp = withFile(cp, "factory/state.json", fmt.Sprintf(`{"schema_version":2,"id":"csv","checkpoint":{"operation":%q,"status":"ready"}}`, op))
	for _, name := range slices.Sorted(maps.Keys(files)) {
		cp = withFile(cp, name, files[name])
	}
	return cp
}

func withFile(cp node.Checkpoint, name, content string) node.Checkpoint {
	sum := sha256.Sum256([]byte(content))
	cp.Manifest.Files = append(slices.Clone(cp.Manifest.Files), node.ManifestFile{Path: name, Kind: "file", Size: int64(len(content)), SHA256: hex.EncodeToString(sum[:])})
	cp.Files = append(slices.Clone(cp.Files), node.Payload{Path: name, Data: []byte(content)})
	return cp
}

func TestANodeStagesNoMaliciousOrOversizedCheckpoint(t *testing.T) {
	tn := nodetest.New(t)
	macbook := tn.Machine("macbook").Start()
	homelab := tn.Machine("homelab").Start()
	if _, err := macbook.Add("homelab"); err != nil {
		t.Fatal(err)
	}
	homelab.Transfers.Limits = node.Limits{File: 1 << 10, Total: 1 << 20, Files: 64}
	symlink := forgedCheckpoint(macbook.ID, map[string]string{"factory/plan.md": "/etc/passwd"})
	symlink.Manifest.Files[1].Kind = "symlink"
	duplicate := withFile(forgedCheckpoint(macbook.ID, map[string]string{"factory/plan.md": "a"}), "factory/plan.md", "b")
	otherFactory := forgedCheckpoint(macbook.ID, nil)
	otherFactory.Manifest.Factory = "billing"
	unknownSchema := forgedCheckpoint(macbook.ID, nil)
	unknownSchema.Manifest.Schema = node.CheckpointSchema + 1
	cases := []struct {
		name   string
		cp     node.Checkpoint
		reason string
	}{
		{"symlink", symlink, "only regular files"},
		{"traversal", forgedCheckpoint(macbook.ID, map[string]string{"factory/../../../.ssh/authorized_keys": "key"}), "outside the factory folder"},
		{"oversize", forgedCheckpoint(macbook.ID, map[string]string{"evidence/qa-1/trace.bin": strings.Repeat("x", 2<<10)}), "larger than"},
		{"duplicate", duplicate, "twice"},
		{"conflicting identity", otherFactory, "its state.json is csv"},
		{"unknown schema", unknownSchema, "schema"},
	}
	unsafeSlugs := []string{"", ".", "..", "../../escaped", "a/b", `a\b`, "/tmp/escaped", strings.Repeat("a", 256), strings.Repeat("é", 128), ".git", "-rf", "csv\n", "csv\x00x", "csv\t", "csv\x7f", "csv\u0085"}
	for _, slug := range unsafeSlugs {
		cp := forgedCheckpoint(macbook.ID, nil)
		cp.Manifest.Slug = slug
		cases = append(cases, struct {
			name   string
			cp     node.Checkpoint
			reason string
		}{"slug " + slug, cp, "factory folder"})
	}
	for _, c := range cases {
		status, _, body := tn.Request(macbook.Machine, homelab.Machine, "POST", "/api/node/transfer/stage", "", c.cp)
		if status == http.StatusOK || !strings.Contains(body, c.reason) {
			t.Errorf("a %s checkpoint answered %d %s; want a refusal saying %q", c.name, status, body, c.reason)
		}
		if _, err := os.Stat(node.StagedDir(homelab.Dir, c.cp.Manifest.Incarnation)); err == nil {
			t.Errorf("homelab staged the %s checkpoint", c.name)
		}
	}
	honest := forgedCheckpoint(macbook.ID, map[string]string{"factory/plan.md": "plan"})
	if status, _, body := tn.Request(macbook.Machine, homelab.Machine, "POST", "/api/node/transfer/stage", "", honest); status != http.StatusOK {
		t.Fatalf("an honest checkpoint answered %d %s", status, body)
	}
	stranger := tn.Machine("stranger").Start()
	if status, _, _ := tn.Request(stranger.Machine, homelab.Machine, "POST", "/api/node/transfer/stage", "", forgedCheckpoint(stranger.ID, nil)); status == http.StatusOK {
		t.Error("homelab staged a checkpoint from a node outside its node list")
	}
	safeSlugs := []string{"Fix_Bug", "CSV", "a--b", "csv-", "café", "with space", strings.Repeat("é", 127)}
	for _, length := range []int{200, 250, 254, 255} {
		safeSlugs = append(safeSlugs, strings.Repeat("a", length))
	}
	for _, slug := range append(slices.Clone(safeSlugs), unsafeSlugs...) {
		t.Run(fmt.Sprintf("slug %q preserves the source until safe", slug), func(t *testing.T) {
			f := newMoveFixture(t)
			f.csv.Slug = slug
			f.mac = newMoveHost("macbook", f.log, f.csv)
			f.macbook.Runtime.Behind(f.mac)
			moved, err := f.macbook.Transfers.Move(context.Background(), uuid.NewString(), f.csv.Key, "homelab")
			if slices.Contains(safeSlugs, slug) {
				if err != nil || moved.Phase != node.PhaseActive {
					t.Fatalf("safe folder failed to move: %+v, %v", moved, err)
				}
			} else if err == nil || moved.Phase != node.PhasePreflight || !f.mac.runs(f.csv.Key) || len(f.log.all()) != 0 || len(f.lab.adopted) != 0 {
				t.Fatalf("unsafe slug changed execution: %+v, %v, log=%v", moved, err, f.log.all())
			}
		})
	}

}

func TestPreflightRefusesANodeThatCannotRunTheFactoryAndNamesWhy(t *testing.T) {
	newGitFixture(t)
	tn := nodetest.New(t)
	homelab := tn.Machine("homelab").Start()
	homelab.Nodes.Clone = cloneWithOwnCredentials(homelab.Dir)
	browserConfig := "qa:\n  mode: browser\n  url: http://localhost:${FACTORY_WEB_PORT}/\n  health: http://localhost:${FACTORY_WEB_PORT}/health\n  start: exit 3\n  seed: none\n  sign_in: none\n"
	failing := func(name, detail string) node.Probe {
		return node.Probe{Name: name, Run: func(context.Context) node.Check { return node.Check{Name: name, Mark: node.MarkFail, Detail: detail} }}
	}
	cases := []struct {
		name   string
		probe  node.Probe
		remote string
		config string
		reason string
	}{
		{"missing tool", failing("tools", "gh-stack is not installed"), appRemote, "", "gh-stack is not installed"},
		{"missing agent login", failing("claude", "claude is not logged in"), appRemote, "", "claude is not logged in"},
		{"unclonable repo", nodetest.Passing("tools", "ok"), goneRemote, "", "cannot clone " + goneRemote},
		{"failing stack recipe", nodetest.Passing("tools", "ok"), appRemote, browserConfig, "stack_start"},
	}
	for _, c := range cases {
		homelab.SetProbes(c.probe)
		request := node.PreflightRequest{Operation: uuid.NewString(), Incarnation: uuid.NewString(), Factory: "csv", Slug: "csv", GitURL: c.remote, Branch: "worktree-csv", Config: c.config, Protocol: node.Protocol}
		if report := homelab.Transfers.Preflight(context.Background(), request); !strings.Contains(report.Refusal, c.reason) {
			t.Errorf("preflight with a %s answered %q; want a refusal naming %q", c.name, report.Refusal, c.reason)
		}
	}
}

type hostLog struct {
	mu      sync.Mutex
	entries []string
}

func (l *hostLog) add(entry string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.entries = append(l.entries, entry)
}

func (l *hostLog) all() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.entries)
}

func (l *hostLog) count(entry string) int {
	n := 0
	for _, e := range l.all() {
		if e == entry {
			n++
		}
	}
	return n
}

type moveHost struct {
	name string
	log  *hostLog

	mu           sync.Mutex
	factories    map[string]host.Movable
	adopted      map[string]host.AdoptRequest
	refusals     map[string]string
	unmovable    map[string]string
	holdings     map[string]host.Holding
	waiting      string
	onCheckpoint func()
	onRelease    func()
	onAdopt      func()
}

func newMoveHost(name string, log *hostLog, factories ...host.Movable) *moveHost {
	h := &moveHost{name: name, log: log, factories: map[string]host.Movable{}, adopted: map[string]host.AdoptRequest{}, refusals: map[string]string{}, unmovable: map[string]string{}, holdings: map[string]host.Holding{}}
	for _, f := range factories {
		h.factories[f.Key] = f
	}
	return h
}

func (h *moveHost) set(change func(*moveHost)) {
	h.mu.Lock()
	defer h.mu.Unlock()
	change(h)
}

func (h *moveHost) runs(key string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	_, ok := h.factories[key]
	return ok
}

func (h *moveHost) holding(key string) host.Holding {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.holdings[key]
}

func (h *moveHost) Call(_ context.Context, m host.Message) (host.Message, error) {
	h.mu.Lock()
	value, then, err := h.handle(m)
	h.mu.Unlock()
	if then != nil {
		then()
	}
	if err != nil {
		return host.Message{T: "error", Error: err.Error()}, nil
	}
	data, _ := json.Marshal(value)
	return host.Message{T: "done", Data: data}, nil
}

func (h *moveHost) handle(m host.Message) (any, func(), error) {
	var r struct {
		Key, Operation, Repo, Slug string
	}
	json.Unmarshal(m.Data, &r)
	switch m.T {
	case "local":
		local := host.Local{Factories: []host.LocalFactory{}}
		for _, key := range slices.Sorted(maps.Keys(h.factories)) {
			f := h.factories[key]
			local.Factories = append(local.Factories, host.LocalFactory{Key: key, ID: f.ID, Incarnation: f.Incarnation, Repo: f.Repo, Generation: h.holdings[key].Generation, Unverified: h.holdings[key].Unverified})
		}
		return local, nil, nil
	case "movable", "checkpoint":
		f, ok := h.factories[r.Key]
		switch {
		case !ok:
			return nil, nil, fmt.Errorf("no factory %s", r.Key)
		case h.unmovable[r.Key] != "":
			return nil, nil, errors.New(h.unmovable[r.Key])
		case m.T == "movable":
			return f, nil, nil
		case h.refusals[r.Key] != "":
			return nil, nil, errors.New(h.refusals[r.Key])
		case h.waiting != "":
			f.Waiting = h.waiting
			return f, nil, nil
		}
		return f, h.onCheckpoint, supervisorCheckpoints(f.Dir, r.Operation)
	case "release":
		key := state.Key(r.Repo, r.Slug)
		delete(h.factories, key)
		h.log.add(h.name + " release " + r.Slug)
		return nil, h.onRelease, nil
	case "can-adopt":
		return nil, h.onAdopt, nil
	case "adopt":
		var adopt host.AdoptRequest
		json.Unmarshal(m.Data, &adopt)
		key := state.Key(adopt.Repo, adopt.Slug)
		h.adopted[key] = adopt
		return key, nil, nil
	case "launch-moved":
		a := h.adopted[r.Key]
		worktree := filepath.Join(factory.WorktreesDir(a.Repo), a.Slug)
		h.factories[r.Key] = host.Movable{Key: r.Key, Incarnation: a.Incarnation, ID: a.Slug, Slug: a.Slug, Repo: a.Repo, Worktree: worktree, Dir: filepath.Join(worktree, ".claude", "factory", a.Slug), Branch: "worktree-" + a.Slug}
		h.log.add(h.name + " launch " + a.Slug)
		return "%1", nil, nil
	case "holding":
		var holding host.HoldingRequest
		json.Unmarshal(m.Data, &holding)
		h.holdings[holding.Key] = holding.Holding
	}
	return nil, nil, nil
}

func supervisorCheckpoints(dir, operation string) error {
	path := filepath.Join(dir, "state.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var s map[string]any
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}
	s["checkpoint"] = map[string]string{"operation": operation, "status": "ready"}
	data, _ = json.Marshal(s)
	return os.WriteFile(path, data, 0o644)
}

type moveFixture struct {
	g                gitFixture
	tn               *nodetest.Tailnet
	macbook, homelab *nodetest.Node
	mac, lab         *moveHost
	log              *hostLog
	csv              host.Movable
}

func newMoveFixture(t *testing.T) moveFixture {
	t.Helper()
	g := newGitFixture(t)
	writeFiles(t, g.dir, map[string]string{"state.json": `{"schema_version":2,"id":"csv","branch":"worktree-csv","session_id":"old-session"}`, "brief.md": "Export contacts as CSV."})
	writeFiles(t, g.repo, map[string]string{".factory/config.yml": "qa:\n  mode: terminal\n"})
	tn := nodetest.New(t)
	f := moveFixture{g: g, tn: tn, macbook: tn.Machine("macbook").Start(), homelab: tn.Machine("homelab").Start(), log: &hostLog{}}
	if _, err := f.macbook.Add("homelab"); err != nil {
		t.Fatal(err)
	}
	f.homelab.Nodes.Clone = cloneWithOwnCredentials(f.homelab.Dir)
	f.macbook.SetRepos(map[string]string{appRemote: g.repo})
	f.csv = host.Movable{Key: state.Key(g.repo, "csv"), Incarnation: uuid.NewString(), ID: "csv", Slug: "csv", Repo: g.repo, Worktree: g.worktree, Dir: g.dir, Evidence: filepath.Join(g.worktree, ".claude", "qa", "csv"), Branch: "worktree-csv"}
	f.mac, f.lab = newMoveHost("macbook", f.log, f.csv), newMoveHost("homelab", f.log)
	f.macbook.Runtime.Behind(f.mac)
	f.homelab.Runtime.Behind(f.lab)
	return f
}

func restarted(n *nodetest.Node) *node.Transfers {
	return &node.Transfers{Commands: n.Transfers.Commands, Nodes: n.Nodes, StateDir: n.Dir, Holds: n.API.HoldsIncarnation, Poll: 20 * time.Millisecond, Holders: &node.Holders{Dir: filepath.Join(n.Dir, "holders.git")}}
}

func holderOf(t *testing.T, incarnation string) node.Holder {
	t.Helper()
	h, _, err := (&node.Holders{Dir: t.TempDir()}).Read(context.Background(), appRemote, incarnation)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func progressOf(t *testing.T, ts *node.Transfers, op string) node.Progress {
	t.Helper()
	p, err := ts.Progress(op)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestAMoveInterruptedAtEachStepResumesAndNeverRunsTheFactoryInTwoPlaces(t *testing.T) {
	f := newMoveFixture(t)
	op, key, background := uuid.NewString(), f.csv.Key, context.Background()

	f.mac.set(func(h *moveHost) { h.waiting = "waiting for the supervisor of csv to write checkpoint " + op })
	crashed, crash := context.WithCancel(background)
	first := restarted(f.macbook)
	first.Ctx = crashed
	if _, err := first.Start(background, node.MoveRequest{Operation: op, Factory: "csv", Target: "homelab"}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the move to wait for the supervisor's checkpoint", func() bool { return progressOf(t, first, op).State == node.StateCheckpointWaiting })
	crash()
	eventually(t, "the crashed move to stop", func() bool { return !first.Running(op) })
	if p := progressOf(t, restarted(f.macbook), op); p.State != node.StateInterrupted || p.Phase != node.PhasePreflight {
		t.Fatalf("a crash while the supervisor checkpointed left %s at %s", p.State, p.Phase)
	}

	f.mac.set(func(h *moveHost) { h.waiting, h.onCheckpoint = "", func() { f.homelab.SetHang(true) } })
	hung, stop := context.WithTimeout(background, 2*time.Second)
	restarted(f.macbook).Move(hung, op, key, "homelab")
	stop()
	f.homelab.SetHang(false)
	f.mac.set(func(h *moveHost) { h.onCheckpoint = nil })
	if p := progressOf(t, restarted(f.macbook), op); p.State != node.StateInterrupted || p.Phase != node.PhaseCheckpoint {
		t.Fatalf("a crash after the checkpoint left %s at %s", p.State, p.Phase)
	}
	if staged, err := restarted(f.macbook).Stage(background, op, key, "homelab"); err != nil || staged.Phase != node.PhaseStaged {
		t.Fatalf("resuming after the checkpoint answered %+v, %v", staged, err)
	}

	f.mac.set(func(h *moveHost) { h.onRelease = func() { f.homelab.SetOnline(false) } })
	if _, err := restarted(f.macbook).Move(background, op, key, "homelab"); err == nil {
		t.Fatal("a move whose target went away after the release reported success")
	}
	stranded := progressOf(t, restarted(f.macbook), op)
	if stranded.State != node.StateRecoveryNeeded || !strings.Contains(strings.Join(stranded.Recovery, "\n"), "--force") {
		t.Errorf("a source released to an unavailable target reports %s with %q; want recovery steps", stranded.State, stranded.Recovery)
	}
	if f.mac.runs(key) || f.log.count("homelab launch csv") > 0 {
		t.Fatalf("after an unconfirmed release macbook runs csv: %v, homelab launched it: %v; want it stopped everywhere", f.mac.runs(key), f.log.all())
	}

	f.mac.set(func(h *moveHost) { h.onRelease = nil })
	f.homelab.SetOnline(true)
	for range 2 {
		if moved, err := restarted(f.macbook).Move(background, op, key, "homelab"); err != nil || moved.Phase != node.PhaseActive {
			t.Fatalf("retrying the move answered %+v, %v", moved, err)
		}
	}
	entries := f.log.all()
	if released, launched := slices.Index(entries, "macbook release csv"), slices.Index(entries, "homelab launch csv"); released < 0 || launched < released || f.log.count("homelab launch csv") != 1 {
		t.Errorf("host calls were %v; want macbook stopped before homelab started csv, and homelab to start it once", entries)
	}
	if h := holderOf(t, f.csv.Incarnation); h.Node != f.homelab.ID || h.Generation != 1 || h.Operation != op {
		t.Errorf("the holder record is %+v; want homelab at generation 1 by this move", h)
	}
	moved := filepath.Join(factory.WorktreesDir(gitclone.Dir(f.homelab.Dir, appRemote)), "csv", ".claude", "factory", "csv", "brief.md")
	if brief, err := os.ReadFile(moved); err != nil || string(brief) != "Export contacts as CSV." {
		t.Errorf("homelab's worktree holds %q, %v; want the checkpointed factory folder", brief, err)
	}
}

func TestATargetThatLosesGitMidTakeoverStaysRetryableAndNeverRefusesTheMove(t *testing.T) {
	f := newMoveFixture(t)
	bare := f.g.run(f.g.repo, "ls-remote", "--get-url", "origin")
	away := bare + ".away"
	f.mac.set(func(h *moveHost) {
		h.onRelease = func() {
			f.lab.set(func(target *moveHost) { target.onAdopt = func() { os.Rename(bare, away) } })
		}
	})
	op, key, background := uuid.NewString(), f.csv.Key, context.Background()
	if _, err := f.macbook.Transfers.Move(background, op, key, "homelab"); err == nil {
		t.Fatal("a move whose target lost its git remote mid-takeover reported success")
	}
	if err := os.Rename(away, bare); err != nil {
		t.Fatal(err)
	}
	f.mac.set(func(h *moveHost) { h.onRelease = nil })
	f.lab.set(func(h *moveHost) { h.onAdopt = nil })
	if moved, err := f.macbook.Transfers.Move(background, op, key, "homelab"); err != nil || moved.Phase != node.PhaseActive {
		t.Fatalf("retrying once git was back answered %+v, %v; want the move finished", moved, err)
	}
}

func TestATakeoverCancelledMidwayIsNotStoredAsARefusal(t *testing.T) {
	f := newMoveFixture(t)
	git, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	shims, marker := t.TempDir(), filepath.Join(t.TempDir(), "stalled")
	shim := fmt.Sprintf("#!/bin/sh\ncase \"$*\" in *cat-file*) [ -e %[1]s ] || { touch %[1]s; exec sleep 30; };; esac\nexec %[2]s \"$@\"\n", marker, git)
	if err := os.WriteFile(filepath.Join(shims, "git"), []byte(shim), 0o755); err != nil {
		t.Fatal(err)
	}
	f.mac.set(func(h *moveHost) { h.onRelease = func() { f.homelab.SetOnline(false) } })
	op, background := uuid.NewString(), context.Background()
	if _, err := f.macbook.Transfers.Move(background, op, f.csv.Key, "homelab"); err == nil {
		t.Fatal("a move whose target went away after the release reported success")
	}
	f.homelab.SetOnline(true)
	source, _, err := node.LoadTransfer(f.macbook.Dir, op)
	if err != nil {
		t.Fatal(err)
	}
	activation := node.HandOver{Operation: op, Incarnation: source.Incarnation, Checkpoint: source.Checkpoint, Lease: source.Lease}
	t.Setenv("PATH", shims+string(os.PathListSeparator)+os.Getenv("PATH"))
	cancelled, cancel := context.WithCancel(background)
	go func() {
		for {
			if _, err := os.Stat(marker); err == nil {
				cancel()
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()
	if _, err := f.homelab.Transfers.Activate(cancelled, activation); err == nil {
		t.Fatal("an activation cancelled while the target checked its branches reported success")
	}
	if taken, err := f.homelab.Transfers.Activate(background, activation); err != nil || taken.Phase != node.PhaseActive {
		t.Fatalf("activating again after the cancellation answered %+v, %v; want the takeover finished", taken, err)
	}
}

func (f moveFixture) moveToHomelab(t *testing.T) {
	t.Helper()
	if moved, err := f.macbook.Transfers.Move(context.Background(), uuid.NewString(), f.csv.Key, "homelab"); err != nil || moved.Phase != node.PhaseActive {
		t.Fatalf("moving csv to homelab answered %+v, %v", moved, err)
	}
}

func TestAFactoryMovesBackWithItsNewCommitsAndTakingBackAStrandedMoveEndsIt(t *testing.T) {
	f := newMoveFixture(t)
	f.moveToHomelab(t)
	background := context.Background()
	clone := gitclone.Dir(f.homelab.Dir, appRemote)
	away := filepath.Join(factory.WorktreesDir(clone), "csv")
	if err := os.Remove(filepath.Join(away, ".claude", "factory", "csv", "brief.md")); err != nil {
		t.Fatal(err)
	}
	f.g.commit(away, "made-on-homelab.go")
	back := uuid.NewString()
	request := node.MoveRequest{Operation: back, Factory: state.Key(clone, "csv"), Target: "macbook"}
	if status, _, body := f.tn.Request(f.macbook.Machine, f.homelab.Machine, "POST", "/api/transfers", "", request); status != http.StatusAccepted {
		t.Fatalf("macbook's hub asking homelab to move csv back answered %d %s", status, body)
	}
	for deadline := time.Now().Add(time.Minute); progressOf(t, f.homelab.Transfers, back).Running; time.Sleep(50 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the move back never finished")
		}
	}
	if p := progressOf(t, f.homelab.Transfers, back); p.State != node.StateActive {
		t.Fatalf("the move back ended %s: %s%s", p.State, p.Refusal, p.Failure)
	}
	if head, want := f.g.run(f.g.worktree, "rev-parse", "HEAD"), f.g.run(away, "rev-parse", "HEAD"); head != want {
		t.Errorf("macbook's worktree is at %s after the move back; want homelab's commit %s", head, want)
	}
	if _, err := os.Stat(filepath.Join(f.g.dir, "brief.md")); !os.IsNotExist(err) {
		t.Errorf("macbook kept the brief homelab deleted: %v; want the folder to match the checkpoint", err)
	}

	stranded := uuid.NewString()
	f.mac.set(func(h *moveHost) { h.onRelease = func() { f.homelab.SetOnline(false) } })
	f.macbook.Transfers.Move(background, stranded, f.csv.Key, "homelab")
	if p := progressOf(t, f.macbook.Transfers, stranded); p.State != node.StateRecoveryNeeded {
		t.Fatalf("the stranded move reports %s: %s%s", p.State, p.Refusal, p.Failure)
	}
	if taken, err := f.macbook.Transfers.Takeover(background, node.TakeoverRequest{Operation: uuid.NewString(), Factory: "csv", Confirm: true}); err != nil || taken.Phase != node.PhaseActive {
		t.Fatalf("taking csv back on macbook answered %+v, %v", taken, err)
	}
	if p := progressOf(t, f.macbook.Transfers, stranded); p.State == node.StateRecoveryNeeded {
		t.Errorf("after taking csv back the stranded move still asks for recovery: %q", p.Recovery)
	}
	f.homelab.SetOnline(true)
	f.mac.set(func(h *moveHost) { h.onRelease = nil })
	next, err := f.macbook.Transfers.Start(background, node.MoveRequest{Operation: uuid.NewString(), Factory: "csv", Target: "homelab"})
	if err != nil || next.Operation == stranded {
		t.Errorf("moving csv again answered %+v, %v; want a new move, never the stranded one resumed", next, err)
	}
	f.macbook.Transfers.Cancel(next.Operation)
	eventually(t, "the new move to stop", func() bool { return !f.macbook.Transfers.Running(next.Operation) })
}

func TestAForcedTakeoverWarnsNeedsACheckpointAndTheReturningHolderLearnsItLost(t *testing.T) {
	t.Run("a normal move operation cannot authorize a takeover", func(t *testing.T) {
		f := newMoveFixture(t)
		op := uuid.NewString()
		if _, err := restarted(f.macbook).Stage(context.Background(), op, f.csv.Key, "homelab"); err != nil {
			t.Fatal(err)
		}
		for _, confirm := range []bool{false, true} {
			status, _, body := f.tn.Request(f.macbook.Machine, f.homelab.Machine, "POST", "/api/node/transfer/takeover", "", node.TakeoverRequest{Operation: op, Factory: "csv", Confirm: confirm})
			if status != http.StatusConflict || !strings.Contains(body, "new operation") {
				t.Errorf("reused move operation: %d %s", status, body)
			}
			if _, err := f.homelab.Transfers.Takeover(context.Background(), node.TakeoverRequest{Operation: op, Factory: "csv", Confirm: confirm}); err == nil {
				t.Error("direct takeover reused a normal move")
			}
		}
	})
	f := newMoveFixture(t)
	f.moveToHomelab(t)
	background := context.Background()
	studio := f.tn.Machine("studio").Start()
	if _, err := f.macbook.Add("studio"); err != nil {
		t.Fatal(err)
	}
	studio.Runtime.Behind(newMoveHost("studio", f.log))
	f.homelab.SetOnline(false)

	if _, err := studio.Transfers.Takeover(background, node.TakeoverRequest{Operation: uuid.NewString(), Factory: "csv", Confirm: true}); err == nil || !strings.Contains(err.Error(), "no checkpoint") {
		t.Errorf("a takeover on a node without a checkpoint answered %v; want a refusal", err)
	}
	op := uuid.NewString()
	var unconfirmed struct{ Warning node.Unconfirmed }
	err := f.macbook.Local.Do(background, http.MethodPost, "/api/transfers/takeover", node.TakeoverRequest{Operation: op, Factory: "csv"}, &unconfirmed)
	var status node.StatusError
	if !errors.As(err, &status) || status.Status != http.StatusPreconditionRequired || !strings.Contains(unconfirmed.Warning.Warning, "may only be out of reach") || f.log.count("macbook launch csv") > 0 {
		t.Fatalf("an unconfirmed takeover answered %v with %+v; want the warning and nothing started", err, unconfirmed)
	}
	if taken, err := f.macbook.Transfers.Takeover(background, node.TakeoverRequest{Operation: op, Factory: "csv", Confirm: true}); err != nil || taken.Phase != node.PhaseActive {
		t.Fatalf("a confirmed takeover answered %+v, %v", taken, err)
	}
	if h := holderOf(t, f.csv.Incarnation); h.Node != f.macbook.ID || h.Generation != 2 || f.mac.holding(f.csv.Key).Generation != 2 {
		t.Errorf("after the takeover the holder is %+v and macbook answers at generation %d; want macbook at generation 2", h, f.mac.holding(f.csv.Key).Generation)
	}

	f.homelab.SetOnline(true)
	bare := filepath.Join(filepath.Dir(f.g.repo), "remote.git")
	if err := os.Rename(bare, bare+".away"); err != nil {
		t.Fatal(err)
	}
	returning := restarted(f.homelab)
	returning.HoldUnverified(background)
	returning.Reconcile(background)
	homelabKey := state.Key(gitclone.Dir(f.homelab.Dir, appRemote), "csv")
	if f.lab.holding(homelabKey).Unverified == "" || f.log.count("homelab release csv") > 0 {
		t.Errorf("homelab restarted without reaching the holder record and holds csv as %+v; want it kept from resuming", f.lab.holding(homelabKey))
	}
	if err := os.Rename(bare+".away", bare); err != nil {
		t.Fatal(err)
	}
	returning.Reconcile(background)
	if f.lab.runs(homelabKey) || f.log.count("homelab release csv") != 1 {
		t.Errorf("homelab still runs csv after the record says macbook holds it: %v", f.log.all())
	}
}

func TestTwoNodesTakingOverAtOnceLeaveExactlyOneHolder(t *testing.T) {
	g := newGitFixture(t)
	g.run(g.worktree, "push", "-q", "origin", "worktree-csv")
	tn := nodetest.New(t)
	macbook := tn.Machine("macbook").Start()
	claimants := []*nodetest.Node{tn.Machine("homelab").Start(), tn.Machine("studio").Start()}
	cp := forgedCheckpoint(macbook.ID, nil)
	cp.Manifest.Slug, cp.Manifest.Branch = "csv", "worktree-csv"
	cp.Manifest.Stack = []node.StackBranch{{Branch: "worktree-csv", SHA: g.run(g.worktree, "rev-parse", "worktree-csv")}}
	log := &hostLog{}
	for _, n := range claimants {
		if _, err := macbook.Add(n.Short()); err != nil {
			t.Fatal(err)
		}
		n.Nodes.Clone = cloneWithOwnCredentials(n.Dir)
		n.Runtime.Behind(newMoveHost(n.Short(), log))
		if status, _, body := tn.Request(macbook.Machine, n.Machine, "POST", "/api/node/transfer/stage", "", cp); status != http.StatusOK {
			t.Fatalf("staging on %s answered %d %s", n.Short(), status, body)
		}
	}
	outcomes := make([]error, len(claimants))
	var wg sync.WaitGroup
	for i, n := range claimants {
		wg.Go(func() {
			_, outcomes[i] = n.Transfers.Takeover(context.Background(), node.TakeoverRequest{Operation: uuid.NewString(), Factory: "csv", Confirm: true})
		})
	}
	wg.Wait()
	winner := slices.IndexFunc(outcomes, func(err error) bool { return err == nil })
	loser := 1 - winner
	if winner < 0 || outcomes[loser] == nil || !strings.Contains(outcomes[loser].Error(), "claimed this factory first") {
		t.Fatalf("two simultaneous takeovers answered %v; want exactly one to win and the other told who did", outcomes)
	}
	won := holderOf(t, cp.Manifest.Incarnation)
	if won.Node != claimants[winner].ID || won.Generation != 1 || len(slices.DeleteFunc(log.all(), func(e string) bool { return !strings.HasSuffix(e, " launch csv") })) != 1 {
		t.Errorf("the holder is %+v and the hosts saw %v; want only %s holding and running csv", won, log.all(), claimants[winner].Short())
	}
	late := won
	late.Node, late.Operation = claimants[loser].ID, uuid.NewString()
	if _, err := (&node.Holders{Dir: t.TempDir()}).Claim(context.Background(), appRemote, "", late); !errors.As(err, new(node.HolderChanged)) || holderOf(t, cp.Manifest.Incarnation).Node != won.Node {
		t.Errorf("a claimant that read no holder before the winner wrote answered %v; want its claim refused", err)
	}
}

func TestMoveAllReportsEachFactorysOutcomeWithoutUndoingTheOthers(t *testing.T) {
	f := newMoveFixture(t)
	billing := host.Movable{Key: state.Key(f.g.repo, "billing"), Incarnation: uuid.NewString(), ID: "billing", Slug: "billing", Repo: f.g.repo, Worktree: f.g.worktree, Dir: f.g.dir, Branch: "worktree-csv"}
	docs := host.Movable{Key: state.Key(f.g.repo, "docs"), Incarnation: uuid.NewString(), ID: "docs", Slug: "docs", Repo: f.g.repo}
	f.mac.set(func(h *moveHost) {
		h.factories[billing.Key], h.factories[docs.Key] = billing, docs
		h.refusals[billing.Key] = "the supervisor of billing cannot checkpoint: its QA stage is running"
		h.unmovable[docs.Key] = "docs has no state.json in a worktree yet, so it has nothing to checkpoint"
	})
	op := uuid.NewString()
	if _, err := f.macbook.Transfers.StartAll(context.Background(), node.MoveAllRequest{Operation: op, Target: "homelab"}); err != nil {
		t.Fatal(err)
	}
	var report node.BatchProgress
	for deadline := time.Now().Add(time.Minute); report.Operation == "" || report.Running; time.Sleep(50 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("move all never finished: %+v", report)
		}
		report, _ = f.macbook.Transfers.BatchProgress(op)
	}
	got := map[string]string{}
	for _, item := range report.Items {
		got[item.Factory] = item.State + ": " + item.Reason
	}
	want := map[string]string{
		"billing": node.StateRefused + ": " + f.mac.refusals[billing.Key],
		"csv":     node.StateActive + ": ",
		"docs":    node.StateRefused + ": " + f.mac.unmovable[docs.Key],
	}
	if !maps.Equal(got, want) {
		t.Errorf("move all reported %v; want %v", got, want)
	}
	if h := holderOf(t, f.csv.Incarnation); h.Node != f.homelab.ID {
		t.Errorf("csv's holder is %+v after move all; want homelab despite the other refusals", h)
	}
}

type registeredMoveHost struct {
	inner   *moveHost
	runtime *host.Runtime
}

func (h registeredMoveHost) Call(ctx context.Context, m host.Message) (host.Message, error) {
	if m.T == "can-adopt" || m.T == "adopt" {
		var r host.AdoptRequest
		if err := json.Unmarshal(m.Data, &r); err != nil {
			return host.Message{}, err
		}
		err := h.runtime.CanAdopt(r)
		if err == nil && m.T == "adopt" {
			_, err = h.runtime.Adopt(r)
		}
		if err != nil {
			return host.Message{T: "error", Error: err.Error()}, nil
		}
	}
	return h.inner.Call(ctx, m)
}

func assertOccupiedMovePreserved(t *testing.T, existing, incoming string, alias bool) {
	t.Helper()
	f := newMoveFixture(t)
	ctx := context.Background()
	clone, err := gitclone.Ensure(ctx, t.TempDir(), appRemote)
	if err != nil {
		t.Fatal(err)
	}
	f.homelab.SetRepos(map[string]string{appRemote: clone})
	worktree := filepath.Join(factory.WorktreesDir(clone), existing)
	f.g.run(clone, "worktree", "add", "-q", "-b", "occupied", worktree)
	files := map[string]string{
		filepath.Join(".claude", "factory", "csv", "state.json"):  `{"schema_version":2,"id":"csv","waiting_on":"keep this state"}`,
		filepath.Join(".claude", "factory", "csv", "progress.md"): "keep this progress",
		filepath.Join(".claude", "qa", existing, "evidence.txt"):  "keep this evidence",
	}
	writeFiles(t, worktree, files)
	writeFiles(t, filepath.Join(clone, ".git", "info"), map[string]string{"exclude": strings.Join(state.FactoryPaths, "\n") + "\n"})
	cfg := &state.Config{StateDir: t.TempDir(), Repos: []string{clone}}
	reg, err := state.LoadRegistry(cfg.RegistryPath())
	if err != nil {
		t.Fatal(err)
	}
	r := host.New(host.Runtime{Config: cfg, Loader: &host.Loader{Config: cfg, Registry: reg, Now: time.Now}, Now: time.Now})
	if _, err := r.Adopt(host.AdoptRequest{Repo: clone, Slug: existing, Incarnation: uuid.NewString()}); err != nil {
		t.Fatal(err)
	}
	f.homelab.Runtime.Behind(registeredMoveHost{inner: f.lab, runtime: r})
	destination := filepath.Join(factory.WorktreesDir(clone), incoming)
	if alias {
		if err := os.Symlink(worktree, destination); err != nil {
			t.Fatal(err)
		}
	}
	a, err := os.Stat(worktree)
	if err != nil {
		t.Fatal(err)
	}
	b, statErr := os.Stat(destination)
	same := statErr == nil && os.SameFile(a, b)
	if statErr != nil && !os.IsNotExist(statErr) {
		t.Fatal(statErr)
	}
	f.csv.Slug, f.csv.Key = incoming, state.Key(f.g.repo, incoming)
	f.mac = newMoveHost("macbook", f.log, f.csv)
	f.macbook.Runtime.Behind(f.mac)
	if err := r.CanAdopt(host.AdoptRequest{Repo: clone, Slug: incoming, Incarnation: f.csv.Incarnation}); same && err == nil {
		t.Error("the runtime accepted another factory's filesystem folder")
	} else if !same && err != nil {
		t.Errorf("distinct filesystem folders were refused: %v", err)
	}
	moved, err := f.macbook.Transfers.Move(ctx, uuid.NewString(), f.csv.Key, "homelab")
	if same {
		if err == nil || moved.Phase != node.PhasePreflight || !f.mac.runs(f.csv.Key) || len(f.log.all()) != 0 || len(f.lab.adopted) != 0 {
			t.Errorf("occupied folder was not refused before release: phase=%s err=%v log=%v", moved.Phase, err, f.log.all())
		}
	} else if err != nil || moved.Phase != node.PhaseActive {
		t.Errorf("distinct folder could not move: %+v, %v", moved, err)
	}
	for path, want := range files {
		data, err := os.ReadFile(filepath.Join(worktree, path))
		if err != nil || string(data) != want {
			t.Errorf("occupied factory's %s changed: %q, %v", path, data, err)
		}
	}
	if branch := f.g.run(worktree, "branch", "--show-current"); branch != "occupied" {
		t.Errorf("occupied worktree changed branch to %s", branch)
	}
}

func TestAMoveRefusesFilesystemAliasesOfAnotherFactoryBeforeRelease(t *testing.T) {
	for _, c := range []struct {
		existing, incoming string
		alias              bool
	}{
		{"csv", "CSV", false}, {"café", "cafe\u0301", false}, {"csv", "linked", true},
	} {
		t.Run(c.incoming, func(t *testing.T) { assertOccupiedMovePreserved(t, c.existing, c.incoming, c.alias) })
	}
}

func TestAMoveRefusesAnOccupiedSlugBeforeReleasingTheSource(t *testing.T) {
	assertOccupiedMovePreserved(t, "csv", "csv", false)
}

func TestAReturnedFactoryUsesItsLatestStateWhenTheFolderDiffersFromItsID(t *testing.T) {
	f := newMoveFixture(t)
	worktree := filepath.Join(factory.WorktreesDir(f.g.repo), "zeta")
	f.g.run(f.g.repo, "worktree", "move", f.g.worktree, worktree)
	f.csv.Key, f.csv.Slug, f.csv.Worktree = state.Key(f.g.repo, "zeta"), "zeta", worktree
	f.csv.Dir = filepath.Join(worktree, ".claude", "factory", "csv")
	writeFiles(t, f.csv.Dir, map[string]string{"state.json": `{"schema_version":2,"id":"csv","branch":"worktree-csv","waiting_on":"before the move"}`})
	writeFiles(t, filepath.Join(f.g.repo, ".git", "info"), map[string]string{"exclude": strings.Join(state.FactoryPaths, "\n") + "\n"})
	f.mac = newMoveHost("macbook", f.log, f.csv)
	f.macbook.Runtime.Behind(f.mac)
	f.moveToHomelab(t)
	clone := gitclone.Dir(f.homelab.Dir, appRemote)
	writeFiles(t, filepath.Join(clone, ".git", "info"), map[string]string{"exclude": strings.Join(state.FactoryPaths, "\n") + "\n"})
	found := factory.Discover([]string{clone}, &state.Registry{})
	if len(found) != 1 || found[0].State == nil {
		t.Fatalf("target discovery: %+v", found)
	}
	current := found[0]
	if current.ID != "csv" {
		t.Errorf("the factory id became its folder name: %s", current.ID)
	}
	writeFiles(t, current.Dir, map[string]string{"state.json": `{"schema_version":2,"id":"csv","branch":"worktree-csv","waiting_on":"after work on homelab"}`})
	f.lab.set(func(h *moveHost) {
		m := h.factories[current.Key()]
		m.ID, m.Dir, m.Branch = "csv", current.Dir, "worktree-csv"
		h.factories[current.Key()] = m
	})
	f.homelab.SetRepos(map[string]string{appRemote: clone})
	moved, err := f.homelab.Transfers.Move(context.Background(), uuid.NewString(), current.Key(), "macbook")
	if err != nil || moved.Phase != node.PhaseActive {
		t.Fatalf("return move: %+v, %v", moved, err)
	}
	found = factory.Discover([]string{f.g.repo}, &state.Registry{})
	if len(found) != 1 || found[0].State == nil || found[0].State.WaitingOn != "after work on homelab" || found[0].Dir != f.csv.Dir {
		t.Fatalf("returned checkpoint was shadowed by stale state: %+v", found)
	}
}

func TestAMovedLegacyFactoryUsesTheTargetsWorktree(t *testing.T) {
	f := newMoveFixture(t)
	writeFiles(t, f.csv.Dir, map[string]string{"state.json": fmt.Sprintf(`{"schema_version":2,"id":"csv","branch":"worktree-csv","worktree":%q,"unknown_future_field":{"keep":true}}`, f.csv.Worktree)})
	f.moveToHomelab(t)
	clone := gitclone.Dir(f.homelab.Dir, appRemote)
	found := factory.Discover([]string{clone}, &state.Registry{})
	want := filepath.Join(factory.WorktreesDir(clone), "csv")
	if len(found) != 1 || found[0].Worktree != want || found[0].State.Worktree != want {
		t.Fatalf("legacy state points outside target worktree: %+v; want %s", found, want)
	}
	data, err := os.ReadFile(filepath.Join(found[0].Dir, "state.json"))
	if err != nil || !strings.Contains(string(data), "unknown_future_field") {
		t.Fatalf("rewriting the path lost state: %s, %v", data, err)
	}
}
