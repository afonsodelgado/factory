package hub

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/exp/golden"

	"github.com/afonsodelgado/factory/internal/factory"
	"github.com/afonsodelgado/factory/internal/host"
	"github.com/afonsodelgado/factory/internal/node"
	"github.com/afonsodelgado/factory/internal/node/nodetest"
	"github.com/afonsodelgado/factory/internal/state"
	"github.com/afonsodelgado/factory/internal/ui/selector"
	"github.com/afonsodelgado/factory/internal/ui/theme"
)

func hubOn(t *testing.T, tn *nodetest.Tailnet, local *nodetest.Node) *Model {
	t.Helper()
	m := NewModel(Model{
		Config: &state.Config{}, Now: tn.Now, Icons: theme.Nerd,
		Roster: func(ctx context.Context) (node.Roster, error) {
			var roster node.Roster
			err := local.Local.Do(ctx, http.MethodGet, "/api/nodes", nil, &roster)
			return roster, err
		},
		Peers: func(name string) *node.Client { return node.Remote(name, tn.Client(local.Machine)) },
	})
	t.Cleanup(func() {
		for _, l := range m.nodes {
			l.stop()
		}
	})
	return m
}

func readRoster(t *testing.T, m *Model) {
	t.Helper()
	msg, ok := m.pollRoster()().(rosterMsg)
	if !ok || msg.err != nil {
		t.Fatalf("the hub could not read the node list: %+v", msg)
	}
	m.rosterArrived(msg)
}

func until(t *testing.T, m *Model, done func() bool) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for !done() {
		select {
		case msg := <-m.nodeFeed:
			m.nodeArrived(msg)
		case <-deadline:
			t.Fatal("the hub never heard the other node")
		}
	}
}

func TestAnActionOnAnotherNodeGoesOnlyThereAndItsOutageIsCountedApart(t *testing.T) {
	tn := nodetest.New(t)
	macbook, homelab := tn.Machine("macbook").Start(), tn.Machine("homelab").Start()
	homelab.Runtime.SetFactories(host.LocalFactory{Key: "/srv/cowork:csv", Incarnation: "inc-csv", ID: "csv", Repo: "/srv/cowork", RepoName: "cowork", Status: "running", Stage: "implement", CanPause: true, Pane: "%3"})
	if _, err := macbook.Add("homelab"); err != nil {
		t.Fatal(err)
	}
	m := hubOn(t, tn, macbook)
	readRoster(t, m)
	key := "node:" + homelab.ID + "/inc-csv"
	until(t, m, func() bool { _, ok := m.snap.Find(key); return ok })
	if _, right := m.Header(); !strings.Contains(right, "homelab") {
		t.Fatalf("header right %q; want each other node listed", right)
	}
	golden.RequireEqual(t, selector.Render(m.data(), SelectorWidth, 8))

	if err := m.pause(key); err != nil {
		t.Fatal(err)
	}
	if got := m.flushLater()(); !strings.Contains(got.(noticeMsg).text, "csv was paused on homelab") || len(homelab.Runtime.Calls()) != 1 {
		t.Fatalf("pausing csv said %q and reached homelab %v; want it delivered there once", got.(noticeMsg).text, homelab.Runtime.Calls())
	}

	homelab.SetOnline(false)
	if err := m.pause(key); err != nil {
		t.Fatal(err)
	}
	if got := m.flushLater()(); !strings.Contains(got.(noticeMsg).text, "csv was not paused on homelab") {
		t.Errorf("pausing on a node that just went away said %q; want the failure named on that node", got.(noticeMsg).text)
	}
	if calls := macbook.Runtime.Calls(); len(calls) > 0 || len(tn.Strays()) > 0 {
		t.Errorf("a failed action reached this node %v or stray addresses %v; an action must never be redirected", calls, tn.Strays())
	}

	m.nodeArrived(nodeMsg{id: homelab.ID, err: context.DeadlineExceeded})
	tn.Advance(time.Minute)
	m.refresh()
	if it, _ := m.snap.Find(key); it.Status != factory.Unreachable {
		t.Fatalf("csv on a lost node is %s; want unreachable", it.Status)
	}
	if left, right := m.Header(); !strings.Contains(left, "1 unreachable") || strings.Contains(left, "failed") || !strings.Contains(right, "homelab lost 1m") {
		t.Errorf("header %q | %q; want the lost node's factory counted as unreachable, not failed, and the node marked lost", left, right)
	}
	if err := m.pause(key); err == nil || !strings.Contains(err.Error(), "homelab is unreachable") {
		t.Errorf("pausing on an unreachable node gave %v; want a refusal that names it", err)
	}
}

func TestTheHubAnnouncesEachJoinOnceAndDropsARemovedNode(t *testing.T) {
	tn := nodetest.New(t)
	macbook, homelab := tn.Machine("macbook").Start(), tn.Machine("homelab").Start()
	m := hubOn(t, tn, macbook)
	readRoster(t, m)
	if _, err := macbook.Add("homelab"); err != nil {
		t.Fatal(err)
	}
	readRoster(t, m)
	if !strings.Contains(m.notice.text, "homelab joined your nodes") {
		t.Fatalf("after homelab joined the hub says %q; want the join announced", m.notice.text)
	}
	m.notice = notice{}
	readRoster(t, m)
	if m.notice.text != "" {
		t.Errorf("a second read of the same list announced %q again", m.notice.text)
	}
	if err := macbook.Remove(homelab.ID); err != nil {
		t.Fatal(err)
	}
	readRoster(t, m)
	if len(m.nodes) > 0 {
		t.Error("the hub kept following a node the list removed")
	}
}
