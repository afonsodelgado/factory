package hub

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/exp/golden"

	"github.com/afonsodelgado/factory/internal/factory"
	"github.com/afonsodelgado/factory/internal/host"
	"github.com/afonsodelgado/factory/internal/remote"
	"github.com/afonsodelgado/factory/internal/state"
	"github.com/afonsodelgado/factory/internal/ui/panel"
	"github.com/afonsodelgado/factory/internal/ui/selector"
	"github.com/afonsodelgado/factory/internal/ui/theme"
)

var remoteNow = time.Date(2026, 11, 12, 14, 10, 0, 0, time.UTC)

func wired(id, stage string, status factory.Status, attention ...state.Attention) remote.Factory {
	st := &state.State{ID: id, Stage: stage, Attention: attention, History: []state.HistoryEntry{{At: state.Time{Time: remoteNow.Add(-6 * time.Minute)}, Stage: stage, Event: "started"}}}
	return remote.Factory{ID: id, Slug: id, Incarnation: "inc-" + id, Repo: "/home/afonso/factory/repos/cowork", Worktree: "/home/afonso/factory/repos/cowork/.claude/worktrees/" + id, State: st, Entry: &state.Entry{CreatedAt: remoteNow.Add(-time.Hour)}, Status: status, Unblocked: factory.Running}
}

func mixedModel(t *testing.T) *Model {
	t.Helper()
	cfg := &state.Config{StateDir: t.TempDir(), Repos: []string{"/r/cowork", "/r/factory"}}
	cfg.Remotes = map[string]state.Remote{"homelab": {Host: "homelab", User: "afonso"}, "spare": {Host: "spare"}}
	m := NewModel(Model{Config: cfg, Now: func() time.Time { return remoteNow }, Icons: theme.Nerd})
	question := state.Attention{ID: "q-csv-1", Kind: "question", Title: "Scope of the export", CreatedAt: state.Time{Time: remoteNow.Add(-6 * time.Minute)}}
	m.links = map[string]*link{
		"homelab": {ok: true, server: "s-1", contact: remoteNow, view: remote.View{Home: "/home/afonso", Factories: []remote.Factory{
			wired("csv-export", "requirements", factory.NeedsYou, question),
			wired("rate-limits", "qa", factory.Running),
		}}},
		"spare": {server: "s-2", since: remoteNow.Add(-time.Hour), contact: remoteNow.Add(-4 * time.Minute), view: remote.View{Factories: []remote.Factory{
			wired("doctor-json", "plan", factory.Stalled),
		}}},
	}
	local := Item{Factory: factory.Factory{Repo: "/r/cowork", Slug: "eng-7449", State: &state.State{ID: "ENG-7449", Stage: "implement"}, Entry: &state.Entry{CreatedAt: remoteNow.Add(-time.Hour)}}, Status: factory.Running}
	m.apply(Snapshot{At: remoteNow, Items: []Item{local}})
	return m
}

func TestRemoteFactoriesGroupBesideLocalOnesAndGoGreyWhenUnreachable(t *testing.T) {
	m := mixedModel(t)
	t.Run("wide", func(t *testing.T) {
		golden.RequireEqual(t, selector.Render(m.data(), SelectorWidth, 18))
	})
	t.Run("narrow", func(t *testing.T) {
		golden.RequireEqual(t, selector.Render(m.data(), NarrowSelectorWidth, 18))
	})
	if _, right := m.Header(); !strings.Contains(right, "homelab") || !strings.Contains(right, "spare unreachable 4m") {
		t.Errorf("header right %q; want each remote's link, so a lost server is visible from any row", right)
	}
}

func TestAServerLinkOpensOnceAcrossPollsAndHubRestarts(t *testing.T) {
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "ssh"), []byte("#!/bin/sh\nexit 255\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	m := mixedModel(t)
	var opened []string
	withLink := func(m *Model) *Model {
		m.Tunnels = &remote.Tunnels{StateDir: m.Config.StateDir}
		m.Open = func(url string) { opened = append(opened, url) }
		m.links["homelab"].view.Opens = []remote.Open{{ID: "o-1", Factory: "csv-export", URL: "https://github.com/acme/app/pull/7"}, {ID: "o-2", Factory: "csv-export", URL: "http://localhost:5173/"}}
		return m
	}
	withLink(m)

	first, second := m.settleOpens("homelab"), m.settleOpens("homelab")
	if first == nil || second != nil {
		t.Fatal("a second poll before the first open finished started another open")
	}
	drain(first)
	restarted := withLink(mixedModel(t))
	restarted.Config.StateDir = m.Config.StateDir
	drain(restarted.settleOpens("homelab"))
	if len(opened) != 1 || opened[0] != "https://github.com/acme/app/pull/7" {
		t.Fatalf("opened %v; want the PR link once, as it is, even though the server never heard the acknowledgement", opened)
	}
	if !restarted.opening["o-2"] {
		t.Fatal("a link whose tunnel failed was dropped; want it kept on the server and tried again after a restart")
	}
}

func TestTheHubFollowsRemoteAndPushChangesMadeOutsideIt(t *testing.T) {
	root := t.TempDir()
	requests := filepath.Join(root, "requests.log")
	writeTestFile(t, filepath.Join(root, "bin", "ssh"), "#!/bin/sh\ncat >>"+requests+"\necho >>"+requests+"\necho '{\"protocol\":1,\"status\":\"completed\",\"data\":{}}'\n")
	os.Chmod(filepath.Join(root, "bin", "ssh"), 0o755)
	t.Setenv("PATH", filepath.Join(root, "bin")+string(os.PathListSeparator)+os.Getenv("PATH"))
	configDir := filepath.Join(root, "config")
	startup := state.Machine{Remotes: map[string]state.Remote{"homelab": {Host: "homelab"}}, Push: state.Push{Topic: "atStartup"}}
	m := NewModel(Model{Config: &state.Config{ConfigDir: configDir, StateDir: filepath.Join(root, "state"), Machine: startup}, Now: func() time.Time { return remoteNow }, Icons: theme.Nerd})
	poll := func() string {
		t.Helper()
		os.Remove(requests)
		drain(m.pollRemotes())
		for _, l := range m.links {
			l.busy = false
		}
		data, _ := os.ReadFile(requests)
		return string(data)
	}

	writeTestFile(t, filepath.Join(configDir, "config.yml"), "remotes:\n  homelab:\n    host: homelab\npush:\n  topic: fromTheCLI\n")
	if sent := poll(); !strings.Contains(sent, "fromTheCLI") || strings.Contains(sent, "atStartup") {
		t.Fatalf("the hub sent:\n%s\nwant the push settings factory remote push wrote, not the copy it loaded at start", sent)
	}
	writeTestFile(t, filepath.Join(configDir, "config.yml"), "push: {}\n")
	if sent := poll(); sent != "" {
		t.Fatalf("after factory remote remove the hub still sent:\n%s", sent)
	}
}

func drain(cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	if batch, ok := cmd().(tea.BatchMsg); ok {
		for _, c := range batch {
			drain(c)
		}
	}
}

func TestARemoteGateNamesTheServerAndTheTunnelItsPageComesThrough(t *testing.T) {
	local := time.Local
	time.Local = time.UTC
	t.Cleanup(func() { time.Local = local })
	m := mixedModel(t)
	gate := state.Attention{ID: "gate-plan", Kind: "gate", Title: "The plan is ready", CreatedAt: state.Time{Time: remoteNow.Add(-2 * time.Minute)}}
	w := wired("doctor-json", "plan", factory.NeedsYou, gate)
	w.State.Stages = map[string]state.Stage{"plan": {Artifacts: []state.Artifact{{Title: "Plan", URL: "file://" + w.Worktree + "/.claude/factory/doctor-json/plan.html"}}}}
	homelab := m.links["homelab"]
	homelab.view.Review = remote.ReviewHome{Socket: "/home/me/.local/state/factory/review.sock", Token: "tok"}
	homelab.view.Factories = append(homelab.view.Factories, w)
	data, _ := json.Marshal([]remote.Tunnel{{Server: "s-1", Socket: "/home/me/.local/state/factory/review.sock", Local: 52817}})
	host.WriteFile(remote.TunnelsPath(m.Config.StateDir, "homelab"), string(data))
	m.refresh()
	it, ok := m.snap.Named("doctor-json")
	if !ok {
		t.Fatal("doctor-json is not listed")
	}
	m.choose(it.Factory.Key())
	m.panel.tab = panel.Attention
	golden.RequireEqual(t, m.PanelView(109, 20))
}

func TestReconnectDoesNotRepeatAttentionEvents(t *testing.T) {
	m := mixedModel(t)
	m.Config.Alerts.Desktop = true
	var mu sync.Mutex
	var notified []string
	m.Notify = func(text string) {
		mu.Lock()
		defer mu.Unlock()
		notified = append(notified, text)
	}
	log := host.LoadEventLog(filepath.Join(t.TempDir(), "events.json"))
	cursor := int64(0)
	ask := func(at time.Duration, id string) state.Attention {
		return state.Attention{ID: id, Kind: "question", CreatedAt: state.Time{Time: remoteNow.Add(at)}}
	}
	serve := func(hook *state.HookFile, open ...state.Attention) {
		f := factory.Factory{Repo: "/srv/cowork", Slug: "csv", ID: "csv", State: &state.State{ID: "csv", Attention: open}}
		log.Record([]host.Item{{Factory: f, Hook: hook}})
	}
	poll := func() {
		batch := log.After(cursor)
		m.heard("homelab", "s-1", &batch)
		w, _ := remote.LoadWatch(m.Config.StateDir, "homelab")
		cursor = w.Cursor
	}

	serve(nil, ask(-time.Hour, "q-1"))
	poll()
	permission := &state.HookFile{Prompt: &state.PendingPrompt{At: remoteNow}}
	serve(permission, ask(-time.Hour, "q-1"), ask(0, "q-2"))
	poll()
	serve(permission, ask(-time.Hour, "q-1"))
	serve(permission, ask(-time.Hour, "q-1"), ask(time.Minute, "q-3"))
	serve(permission, ask(-time.Hour, "q-1"), ask(2*time.Minute, "q-2"))
	missed := log.After(cursor)
	m.heard("homelab", "s-1", &missed)
	m.heard("homelab", "s-1", &missed)

	want := []string{"csv on homelab needs you", "csv on homelab needs you", "csv on homelab needs you"}
	got := func() []string {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(notified)
	}
	for deadline := time.Now().Add(2 * time.Second); len(got()) < len(want) && time.Now().Before(deadline); {
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(200 * time.Millisecond)
	if !slices.Equal(got(), want) {
		t.Fatalf("notified %q; want one alert each for q-2, the permission prompt and q-2 asked again, none for q-1 seen at first contact or q-3 asked and settled during the outage, and none twice on a replay", got())
	}
}

func TestRemoteAnswerKeepsTheDraftWhenTheServerIsAwayOrSilent(t *testing.T) {
	m := mixedModel(t)
	it, ok := m.snap.Named("csv-export")
	if !ok {
		t.Fatal("csv-export is not listed")
	}
	key := it.Factory.Key() + "|q-csv-1"
	m.choose(it.Factory.Key())
	m.panel.tab = panel.Attention

	m.links["homelab"].ok = false
	m.links["homelab"].contact = remoteNow.Add(-time.Minute)
	m.deliver("handled")
	if len(m.later) != 0 || !strings.Contains(m.panel.refused[key], "is unreachable.") {
		t.Fatalf("an answer to an unreachable server queued %d sends and said %q; want nothing sent and the draft kept", len(m.later), m.panel.refused[key])
	}

	m.links["homelab"].ok = true
	m.deliver("handled")
	mark, sent := m.claim(key)
	if len(m.later) != 1 || !sent {
		t.Fatalf("a reachable server got %d sends", len(m.later))
	}
	m.answered(answeredMsg{mark: mark, err: remote.ErrUncertain})
	if _, still := m.claim(key); still || !strings.Contains(m.panel.refused[key], "Outcome unknown") {
		t.Fatalf("a lost reply reads %q; want outcome unknown, never a claim that it was sent or not", m.panel.refused[key])
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

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
