package hub

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/exp/golden"

	"github.com/afonsodelgado/factory/internal/factory"
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

type quietTypist struct{}

func (quietTypist) Type(string, string) error { return nil }

func TestRemoteAnswerKeepsTheDraftWhenTheServerIsAwayOrSilent(t *testing.T) {
	m := mixedModel(t)
	m.Typist = quietTypist{}
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
	if len(m.later) != 0 || !strings.Contains(m.panel.refused[key], "unreachable. Draft kept.") {
		t.Fatalf("an answer to an unreachable server queued %d sends and said %q; want nothing sent and the draft kept", len(m.later), m.panel.refused[key])
	}

	m.links["homelab"].ok = true
	m.deliver("handled")
	mark, sent := m.panel.sent[key]
	if len(m.later) != 1 || !sent {
		t.Fatalf("a reachable server got %d sends", len(m.later))
	}
	m.answered(answeredMsg{mark: mark, err: remote.ErrUncertain})
	if _, still := m.panel.sent[key]; still || !strings.Contains(m.panel.refused[key], "Outcome unknown") {
		t.Fatalf("a lost reply reads %q; want outcome unknown, never a claim that it was sent or not", m.panel.refused[key])
	}
}

func TestAHubHoldingTheServerSocketAnswersRemoteViews(t *testing.T) {
	m := NewModel(Model{Config: &state.Config{StateDir: t.TempDir()}, Now: func() time.Time { return remoteNow }})
	replies := make(chan Message, 1)
	m.handleEvent(Event{Message: Message{T: "view", Pane: "cli"}, Reply: replies})
	select {
	case reply := <-replies:
		if reply.T != "done" {
			t.Fatalf("view reply %+v; want done, since a silent hub makes the laptop mark the server unreachable", reply)
		}
	default:
		t.Fatal("the hub left a remote view unanswered")
	}
}
