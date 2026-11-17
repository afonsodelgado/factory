package hub

import (
	"context"
	"net/http"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/exp/golden"
	"github.com/google/uuid"

	"github.com/afonsodelgado/factory/internal/factory"
	"github.com/afonsodelgado/factory/internal/host"
	"github.com/afonsodelgado/factory/internal/node"
	"github.com/afonsodelgado/factory/internal/node/nodetest"
	"github.com/afonsodelgado/factory/internal/state"
	"github.com/afonsodelgado/factory/internal/tmux"
	"github.com/afonsodelgado/factory/internal/ui/dialog"
	"github.com/afonsodelgado/factory/internal/ui/panel"
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

func TestAnActionOnAnotherNodeGoesOnlyThereWithItsGenerationAndItsOutageIsCountedApart(t *testing.T) {
	tn := nodetest.New(t)
	macbook, homelab := tn.Machine("macbook").Start(), tn.Machine("homelab").Start()
	homelab.Runtime.SetFactories(host.LocalFactory{Key: "/srv/cowork:csv", Incarnation: "inc-csv", Generation: 3, ID: "csv", Repo: "/srv/cowork", RepoName: "cowork", Status: "running", Stage: "implement", CanPause: true, Pane: "%3"})
	tn.Watch("/api/factories//srv/cowork:csv/actions")
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
	csv, _ := m.snap.Find(key)
	m.answerOnNode(csv, node.ActionBody{Action: host.ActAnswer, Item: "Q1", Text: "yes"})
	m.flushLater()()
	if bodies := tn.Bodies("/api/factories//srv/cowork:csv/actions"); len(bodies) != 2 || !strings.Contains(bodies[0], `"generation":3`) || !strings.Contains(bodies[1], `"generation":3`) {
		t.Fatalf("the pause and the answer sent %q; want both to carry the generation they were loaded at, so a moved factory refuses them as stale", bodies)
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

func askingOn(t *testing.T, n *nodetest.Node, typed host.Typist) {
	t.Helper()
	now := time.Date(2026, 11, 16, 9, 0, 0, 0, time.UTC)
	ask := func(id, question string, age time.Duration) state.Attention {
		return state.Attention{ID: id, Kind: "question", Questions: []string{question}, Options: state.OptionLists{{"eu-west", "us-east"}}, Recommended: state.Strings{"eu-west"}, CreatedAt: state.Time{Time: now.Add(-age)}}
	}
	f := factory.Factory{Repo: "/srv/cowork", Slug: "csv", Entry: &state.Entry{Incarnation: "inc-csv"}, State: &state.State{SchemaVersion: 2, ID: "csv", Stage: "plan", Attention: []state.Attention{ask("q1", "Which region?", 2*time.Minute), ask("q2", "Which bucket region?", time.Minute)}}}
	pane := tmux.Pane{ID: "%1", Command: "claude", Token: "sup"}
	idle := state.HookEvent{Event: "Stop", At: now.Add(-time.Minute)}
	supervisorNeverAcknowledges := now.Add(host.ReceiptWait + time.Second)
	r := host.New(host.Runtime{Config: &state.Config{StateDir: t.TempDir()}, TM: tmux.Client{Socket: "factory-test-" + uuid.NewString(), ConfigFile: "/dev/null"}, Typist: typed, Now: func() time.Time { return now }})
	r.SetSnapshot(host.Snapshot{At: supervisorNeverAcknowledges, Panes: []tmux.Pane{pane}, Items: []host.Item{{Factory: f, Status: factory.NeedsYou, Hook: &state.HookFile{Main: &idle, Last: idle}, Pane: &pane}}})
	queue := node.NewQueue()
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan error, 1)
	go func() { stopped <- r.Run(ctx, queue.Events()) }()
	t.Cleanup(func() {
		cancel()
		<-stopped
	})
	n.Runtime.Behind(queue)
}

func TestAnAnswerToAnotherNodeIsTypedThereOnceComesBackWhenUnreceivedAndIsUncertainNotRedirectedWhenTheNodeIsLost(t *testing.T) {
	tn := nodetest.New(t)
	macbook, homelab := tn.Machine("macbook").Start(), tn.Machine("homelab").Start()
	typed := &fakeTypist{}
	askingOn(t, homelab, typed)
	if _, err := macbook.Add("homelab"); err != nil {
		t.Fatal(err)
	}
	m := hubOn(t, tn, macbook)
	readRoster(t, m)
	key := "node:" + homelab.ID + "/inc-csv"
	until(t, m, func() bool { it, ok := m.snap.Find(key); return ok && len(m.attentionItems(it, nil)) == 2 })
	m.choose(key)
	m.panel.tab = panel.Attention
	if view := escapes.ReplaceAllString(m.PanelView(109, 28), ""); !strings.Contains(view, "Which region?") {
		t.Fatalf("homelab's question is missing from this hub's panel:\n%s", view)
	}

	m.deliver("")
	sent := m.flushLater()().(nodeAnsweredMsg)
	m.nodeAnswered(sent)
	if view := escapes.ReplaceAllString(m.PanelView(109, 28), ""); len(typed.typed) != 1 || !strings.Contains(typed.typed[0], "q1 answers") || !strings.Contains(view, "Sending q1") {
		t.Fatalf("answering q1 typed %q on homelab and the panel shows:\n%s\nwant it typed there once and shown as sending", typed.typed, view)
	}
	it, _ := m.snap.Find(key)
	q1 := m.attentionItems(it, nil)[0]
	retry := m.answerBody(q1, m.draft(it.Factory, q1), "")
	retry.Operation, retry.Incarnation = sent.operation, "inc-csv"
	var again host.Receipt
	if err := m.peer(homelab.Name).Do(context.Background(), http.MethodPost, actionsOf(m.nodeFactories[key]), retry, &again); err != nil || again != sent.receipt || len(typed.typed) != 1 {
		t.Fatalf("a retry of the same operation gave %+v, %v and typed %q; want the first receipt %+v and nothing typed again", again, err, typed.typed, sent.receipt)
	}
	for deadline := time.Now().Add(5 * time.Second); !strings.Contains(m.panel.refused[key+"|q1"], host.NotReceived); time.Sleep(50 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("q1, which homelab's supervisor never received, still shows %q; want homelab's refusal and the item open to answer again", m.panel.refused[key+"|q1"])
		}
		m.nodeArrived(nodeMsg{id: homelab.ID})
		runLater(m)
	}

	homelab.SetOnline(false)
	m.deliver("")
	m.nodeAnswered(m.flushLater()().(nodeAnsweredMsg))
	m.nodeAnswered(m.flushLater()().(nodeAnsweredMsg))
	view := escapes.ReplaceAllString(m.PanelView(109, 28), "")
	if !strings.Contains(view, "Outcome unknown") || strings.Contains(view, "Not sent") || len(typed.typed) != 1 {
		t.Errorf("an answer to homelab while it is down shows:\n%s\ntyped %q; want the outcome unknown, never \"not sent\"", view, typed.typed)
	}
	if calls := macbook.Runtime.Calls(); len(calls) > 0 || len(tn.Strays()) > 0 {
		t.Errorf("the answer reached this node %v or stray addresses %v; an answer must never be redirected", calls, tn.Strays())
	}

	homelab.SetOnline(true)
	m.recheckSends(homelab.ID)
	m.nodeAnswered(m.flushLater()().(nodeAnsweredMsg))
	if view := escapes.ReplaceAllString(m.PanelView(109, 28), ""); !strings.Contains(view, "homelab has no record of it") {
		t.Errorf("once homelab answers again the panel shows:\n%s\nwant homelab's own receipt lookup to settle the answer", view)
	}
}

func runLater(m *Model) {
	later := m.later
	m.later = nil
	for _, cmd := range later {
		if msg, ok := cmd().(nodeAnsweredMsg); ok {
			m.nodeAnswered(msg)
		}
	}
}

func TestCreatingOnANodeThatHasTheRepoUsesItsCheckoutAndClonesNothing(t *testing.T) {
	tn := nodetest.New(t)
	macbook, homelab := tn.Machine("macbook").Start(), tn.Machine("homelab").Start()
	origin := "git@github.com:alice/cowork.git"
	repo := t.TempDir()
	gitIn(t, repo, "init", "-q")
	gitIn(t, repo, "remote", "add", "origin", origin)
	homelab.SetRepos(map[string]string{origin: "/srv/cowork"})
	if _, err := macbook.Add("homelab"); err != nil {
		t.Fatal(err)
	}
	m := hubOn(t, tn, macbook)
	readRoster(t, m)
	until(t, m, func() bool { l, ok := m.nodes[homelab.ID]; return ok && l.answered })

	if err := m.createOnNode(dialog.NewResult{Node: homelab.Name, Repo: repo, Slug: "csv", Prompt: "Export contacts."}); err != nil {
		t.Fatal(err)
	}
	got := m.flushLater()().(noticeMsg)
	if !strings.Contains(got.text, "csv started on homelab") || len(homelab.Clones()) > 0 || !slices.Contains(homelab.Runtime.Calls(), "create") {
		t.Fatalf("creating on homelab said %q, cloned %v and called %v; want it started in homelab's own checkout with no clone", got.text, homelab.Clones(), homelab.Runtime.Calls())
	}
}

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}
