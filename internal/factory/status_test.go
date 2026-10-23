package factory

import (
	"testing"
	"time"

	"github.com/afonsodelgado/factory/internal/state"
)

func TestStatusOf(t *testing.T) {
	now := time.Date(2026, 10, 22, 15, 0, 0, 0, time.UTC)
	ago := func(d time.Duration) time.Time { return now.Add(-d) }
	alive := Pane{Found: true}
	hook := func(main string, mainAt time.Time) *state.HookFile {
		return &state.HookFile{Main: &state.HookEvent{Event: main, At: mainAt}, Last: state.HookEvent{Event: main, At: mainAt}}
	}
	v2 := func(waitingOn string, items ...state.Attention) *state.State {
		return &state.State{Stage: "implement", WaitingOn: waitingOn, Attention: append([]state.Attention{}, items...)}
	}
	v1 := func(waitingOn string) *state.State {
		return &state.State{Stage: "implement", WaitingOn: waitingOn}
	}
	openQuestion := state.Attention{ID: "q-1-1", Kind: "question"}
	resolved := state.Attention{ID: "q-1-1", Kind: "question", ResolvedAt: state.Time{Time: ago(time.Hour)}}
	prompting := hook("PermissionRequest", ago(time.Second))
	prompting.Prompt = &state.PendingPrompt{At: ago(time.Second)}
	approved := &state.HookFile{
		Main: &state.HookEvent{Event: "Notification", Matcher: "permission_prompt", At: ago(5 * time.Minute)},
		Last: state.HookEvent{Event: "PostToolUse", Matcher: "Bash", AgentID: "a1", At: ago(time.Minute)},
	}
	subagentBusy := &state.HookFile{
		Main: &state.HookEvent{Event: "Stop", At: ago(2 * time.Hour)},
		Last: state.HookEvent{Event: "PreToolUse", AgentID: "a1", At: ago(time.Minute)},
	}

	cases := []struct {
		name    string
		factory Factory
		live    Live
		want    Status
	}{
		{"open question", Factory{State: v2("user", openQuestion), StateMod: ago(time.Minute)}, Live{alive, hook("Stop", ago(time.Minute))}, NeedsYou},
		{"answered question, stage agent working", Factory{State: v2("agent", resolved), StateMod: ago(time.Minute)}, Live{alive, hook("Stop", ago(time.Minute))}, WaitingAgent},
		{"permission prompt", Factory{State: v2("agent"), StateMod: ago(time.Minute)}, Live{alive, prompting}, NeedsYou},
		{"approved prompt, tool ran", Factory{State: v2("agent"), StateMod: ago(time.Minute)}, Live{alive, approved}, WaitingAgent},
		{"old format, idle, waiting_on missing", Factory{State: v1(""), StateMod: ago(time.Minute)}, Live{alive, hook("Stop", ago(time.Minute))}, NeedsYou},
		{"old format, no hook file at all", Factory{State: v1(""), StateMod: ago(time.Minute)}, Live{alive, nil}, NeedsYou},
		{"old format, busy", Factory{State: v1(""), StateMod: ago(time.Minute)}, Live{alive, hook("PreToolUse", ago(time.Second))}, Running},
		{"waiting on an agent past the threshold", Factory{State: v2("agent"), StateMod: ago(30 * time.Minute)}, Live{alive, hook("Stop", ago(30*time.Minute))}, Stalled},
		{"waiting on an agent within the threshold", Factory{State: v2("agent"), StateMod: ago(19 * time.Minute)}, Live{alive, hook("Stop", ago(19*time.Minute))}, WaitingAgent},
		{"long CI wait with a fresh heartbeat", Factory{State: v2("ci"), StateMod: ago(2 * time.Hour), Heartbeat: ago(time.Minute)}, Live{alive, hook("Stop", ago(2*time.Hour))}, WaitingCI},
		{"subagent tool call after the supervisor stopped", Factory{State: v2("agent"), StateMod: ago(2 * time.Hour)}, Live{alive, subagentBusy}, WaitingAgent},
		{"exited beats needs you", Factory{State: v2("user", openQuestion)}, Live{Pane{Found: true, Shell: true}, hook("Stop", ago(time.Minute))}, Exited},
		{"no pane", Factory{State: v2("agent")}, Live{Pane{}, nil}, Gone},
		{"done", Factory{State: &state.State{Stages: map[string]state.Stage{"handoff": {Status: "done"}}}}, Live{Pane{}, nil}, Done},
		{"created a minute ago", Factory{Entry: &state.Entry{Created: true, CreatedAt: ago(time.Minute)}}, Live{alive, nil}, Starting},
		{"no state five minutes after creation, claude still up", Factory{Entry: &state.Entry{Created: true, CreatedAt: ago(6 * time.Minute)}}, Live{alive, nil}, FailedToStart},
		{"claude exited right after launch, so wake can reuse the shell", Factory{Entry: &state.Entry{Created: true, CreatedAt: ago(time.Minute)}}, Live{Pane{Found: true, Shell: true}, nil}, Exited},
		{"login shell still loading before claude starts", Factory{Entry: &state.Entry{Created: true, CreatedAt: ago(2 * time.Second)}}, Live{Pane{Found: true, Shell: true}, nil}, Starting},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := StatusOf(c.factory, c.live, now, 20*time.Minute); got != c.want {
				t.Errorf("status = %s, want %s", got, c.want)
			}
		})
	}

	if !IdleAtPrompt(subagentBusy) {
		t.Error("a supervisor at its prompt reads as busy while a stage agent runs, so delivery would be refused for the agent's whole run")
	}
	compacting := &state.HookFile{Main: &state.HookEvent{Event: "SessionStart", Matcher: "compact", At: ago(time.Second)}}
	if IdleAtPrompt(compacting) {
		t.Error("auto-compaction in the middle of a turn reads as idle, so delivery would type into a busy session")
	}
}
