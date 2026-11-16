package hub

import (
	"cmp"
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/afonsodelgado/factory/internal/host"
	"github.com/afonsodelgado/factory/internal/state"
	"github.com/afonsodelgado/factory/internal/tmux"
)

type fakeTypist struct {
	typed []string
}

func (f *fakeTypist) Type(pane, text string) error {
	f.typed = append(f.typed, pane+": "+text)
	return nil
}

type testNode struct {
	mu sync.Mutex
	r  *host.Runtime
}

func (n *testNode) Call(_ context.Context, m host.Message) (host.Message, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.r.Handle(m), nil
}

func (n *testNode) present(s Snapshot) presentationMsg {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.r.SetSnapshot(localOnly(s))
	n.r.SettleReceipts(n.r.Snapshot().Items, func(string) time.Time { return s.At })
	n.r.QueueInbox()
	n.r.FlushTypeQueue()
	n.r.CloseFinishedStages()
	return presentationMsg(n.r.Presentation())
}

func (n *testNode) show(m *Model, s Snapshot) {
	m.applyPresentation(host.Presentation(n.present(s)))
}

func withNode(m Model, r host.Runtime) (*Model, *testNode) {
	r.Config = cmp.Or(r.Config, m.Config)
	if r.Now == nil {
		r.Now = m.Now
	}
	n := &testNode{r: host.New(r)}
	if r.Config != nil && r.Config.StateDir != "" {
		n.r.Recover()
	}
	m.Node = n
	return NewModel(m), n
}

func TestDeliveryGate(t *testing.T) {
	at := time.Date(2026, 10, 22, 14, 44, 0, 0, time.UTC)
	idle := &state.HookFile{Main: &state.HookEvent{Event: "Stop", At: at}, Last: state.HookEvent{Event: "Stop", At: at}}
	busy := &state.HookFile{Main: &state.HookEvent{Event: "UserPromptSubmit", At: at}, Last: state.HookEvent{Event: "UserPromptSubmit", At: at}}
	pane := &tmux.Pane{ID: "%7", Command: "claude"}
	copying := &tmux.Pane{ID: "%7", Command: "claude", InMode: true}

	cases := []struct {
		name    string
		target  Target
		refusal string
	}{
		{"busy per the hook file", Target{Label: "ENG-1", Pane: pane, Hook: busy}, "busy"},
		{"no hook file", Target{Label: "ENG-1", Pane: pane}, "no hook events"},
		{"copy mode", Target{Label: "ENG-1", Pane: copying, Hook: idle}, "copy mode"},
		{"idle at its prompt", Target{Label: "ENG-1", Pane: pane, Hook: idle}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			typist := &fakeTypist{}
			reason, err := Deliver(typist, c.target, "q-1-1 answers:\n1) recommended")
			if err != nil {
				t.Fatal(err)
			}
			if c.refusal == "" {
				if reason != "" || len(typist.typed) != 1 || typist.typed[0] != "%7: q-1-1 answers:\n1) recommended" {
					t.Errorf("refused %q, typed %q; want the message typed into %%7", reason, typist.typed)
				}
				return
			}
			if !strings.Contains(reason, c.refusal) || len(typist.typed) != 0 {
				t.Errorf("reason %q, typed %q; want a refusal naming %q and nothing typed", reason, typist.typed, c.refusal)
			}
		})
	}
}
