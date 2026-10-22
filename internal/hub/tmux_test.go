package hub

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/afonsodelgado/factory/internal/factory"
	"github.com/afonsodelgado/factory/internal/state"
	"github.com/afonsodelgado/factory/internal/tmux"
)

var servers atomic.Int32

func scratchServer(t *testing.T) tmux.Client {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		if os.Getenv("CI") == "" {
			t.Skip("tmux not installed")
		}
		t.Fatal("tmux not installed")
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("SHELL", "/bin/sh")
	t.Setenv("TMUX", "")
	tm := tmux.Client{Socket: fmt.Sprintf("factory-test-%d-%d", os.Getpid(), servers.Add(1)), ConfigFile: "/dev/null"}
	t.Cleanup(func() {
		socket, _ := tm.Run("display-message", "-p", "#{socket_path}")
		tm.Run("kill-server")
		if socket != "" {
			os.Remove(socket)
		}
	})
	return tm
}

func hubFixture(t *testing.T) (*Engine, Layout) {
	t.Helper()
	tm := scratchServer(t)
	self, err := tm.NewSession("factory", HubWindow, 144, 46, "exec sleep 600")
	if err != nil {
		t.Fatal(err)
	}
	e := &Engine{TM: tm, Session: "factory", ViewCommand: func(string) string { return "exec sleep 600" }}
	width, height, err := tm.WindowSize(self)
	if err != nil {
		t.Fatal(err)
	}
	l, err := e.Build(self, Shape{Width: width, Height: height})
	if err != nil {
		t.Fatal(err)
	}
	return e, l
}

func pane(t *testing.T, tm tmux.Client, id string) tmux.Pane {
	t.Helper()
	panes, err := tm.Panes()
	if err != nil {
		t.Fatal(err)
	}
	p, ok := byID(panes, id)
	if !ok {
		t.Fatalf("pane %s is gone", id)
	}
	return p
}

func TestParkedPaneKeepsTheSlotSizeAcrossSwaps(t *testing.T) {
	e, l := hubFixture(t)
	if w := pane(t, e.TM, l.Selector).Width; w != SelectorWidth {
		t.Errorf("selector width = %d, want %d", w, SelectorWidth)
	}
	if h := pane(t, e.TM, l.Footer).Height; h != 1 {
		t.Errorf("footer height = %d, want 1", h)
	}

	created, err := e.NewFactoryPane(l, NewPane{Repo: "/repo", Slug: "eng-1-x", Title: "ENG-1  title", Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	slotW, slotH, err := e.SlotSize(l)
	if err != nil {
		t.Fatal(err)
	}
	sized := func(when, id string) {
		t.Helper()
		if p := pane(t, e.TM, id); p.Width != slotW || p.Height != slotH {
			t.Errorf("%s: pane %s is %dx%d, want the slot's %dx%d", when, id, p.Width, p.Height, slotW, slotH)
		}
	}
	sized("parked", created)

	viewer := l.Slot
	if err := e.Select(&l, created); err != nil {
		t.Fatal(err)
	}
	selected := pane(t, e.TM, created)
	if selected.WindowName != HubWindow || selected.Slug != "eng-1-x" || selected.Title != "ENG-1  title" {
		t.Errorf("selected pane = %+v, want it in the hub window with its tags", selected)
	}
	sized("selected", created)
	sized("viewer parked", viewer)
	if name := pane(t, e.TM, viewer).WindowName; name != SlotParking {
		t.Errorf("the slot viewer was parked in %q, want %q", name, SlotParking)
	}

	if err := e.Select(&l, viewer); err != nil {
		t.Fatal(err)
	}
	if name := pane(t, e.TM, created).WindowName; name != ParkName("eng-1-x") {
		t.Errorf("deselected factory pane is in %q, want its own parking window", name)
	}
	sized("parked again", created)
}

func TestRebuildPutsTheSlotBackAboveThePanelWhenTheSessionPaneCloses(t *testing.T) {
	e, l := hubFixture(t)
	created, err := e.NewFactoryPane(l, NewPane{Repo: "/repo", Slug: "eng-1-x", Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Select(&l, created); err != nil {
		t.Fatal(err)
	}
	if err := e.TM.KillPane(created); err != nil {
		t.Fatal(err)
	}
	width, height, err := e.TM.WindowSize(l.Selector)
	if err != nil {
		t.Fatal(err)
	}
	rebuilt, err := e.Build(l.Selector, Shape{Width: width, Height: height})
	if err != nil {
		t.Fatal(err)
	}
	slot, panel := pane(t, e.TM, rebuilt.Slot), pane(t, e.TM, rebuilt.Panel)
	if slot.UI != "slot" || slot.WindowName != HubWindow {
		t.Fatalf("slot = %+v, want the empty-slot viewer back in the hub window", slot)
	}
	top := func(id string) int {
		out, err := e.TM.Display(id, "#{pane_top} #{pane_left}")
		if err != nil {
			t.Fatal(err)
		}
		var y, x int
		fmt.Sscan(out, &y, &x)
		return y*1000 + x
	}
	if top(slot.ID) >= top(panel.ID) || slot.Width != panel.Width {
		t.Errorf("slot at %d (%dx%d), panel at %d (%dx%d): the slot must sit above the panel in the right column", top(slot.ID), slot.Width, slot.Height, top(panel.ID), panel.Width, panel.Height)
	}
}

func TestAdoptsOnlyLegacyFactorySessions(t *testing.T) {
	e, l := hubFixture(t)
	repo, sameSlugElsewhere := t.TempDir(), t.TempDir()
	for _, r := range []string{repo, sameSlugElsewhere} {
		if err := os.MkdirAll(filepath.Join(factory.WorktreesDir(r), "eng-2-y"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.TM.Run("new-session", "-d", "-s", "eng-2-y", "-c", filepath.Join(factory.WorktreesDir(repo), "eng-2-y"), "exec sleep 600"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.TM.NewSession("notes", "main", 80, 24, "exec sleep 600"); err != nil {
		t.Fatal(err)
	}

	adopted, err := e.Adopt(l, []string{sameSlugElsewhere, repo})
	if err != nil {
		t.Fatal(err)
	}
	if len(adopted) != 1 || adopted[0] != "eng-2-y" {
		t.Fatalf("adopted %v, want [eng-2-y]", adopted)
	}
	if e.TM.HasSession("eng-2-y") {
		t.Error("the legacy session still exists after adoption")
	}
	if !e.TM.HasSession("notes") {
		t.Error("an unrelated session was adopted")
	}
	panes, err := e.TM.Panes()
	if err != nil {
		t.Fatal(err)
	}
	p, ok := FactoryPane(panes, repo, "eng-2-y")
	if !ok || p.WindowName != ParkName("eng-2-y") || p.Session != "factory" {
		t.Errorf("adopted pane = %+v, want it tagged to %s, whose worktree it runs in, and parked: the first repo with the slug would get a second claude on wake", p, repo)
	}
}

func TestDeliverTypesIntoAnIdlePaneAndRefusesCopyMode(t *testing.T) {
	e, l := hubFixture(t)
	out := filepath.Join(t.TempDir(), "typed")
	id, err := e.TM.NewWindow(tmux.Window{Session: "factory", Name: ParkName("eng-3-z"), Command: "exec cat > " + out})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.FitParking(l); err != nil {
		t.Fatal(err)
	}
	waitForCommand(t, e.TM, id, "cat")
	idle := &state.HookFile{Main: &state.HookEvent{Event: "Stop", At: time.Now()}}
	target := func() Target {
		p := pane(t, e.TM, id)
		return Target{Label: "ENG-3", Pane: &p, Hook: idle}
	}

	reason, err := Deliver(e, target(), "q-3-1 answers:\n1) recommended")
	if err != nil || reason != "" {
		t.Fatalf("delivery refused: %q %v", reason, err)
	}
	want := "q-3-1 answers:\n1) recommended\n"
	if got := waitForFile(t, out, want); got != want {
		t.Errorf("the pane received %q, want %q", got, want)
	}

	if _, err := e.TM.Run("copy-mode", "-t", id); err != nil {
		t.Fatal(err)
	}
	reason, err = Deliver(e, target(), "second message")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(reason, "copy mode") {
		t.Errorf("refusal = %q, want it to name copy mode", reason)
	}
	time.Sleep(300 * time.Millisecond)
	if got, _ := os.ReadFile(out); string(got) != want {
		t.Errorf("text reached a pane in copy mode: %q", got)
	}
}

func waitForCommand(t *testing.T, tm tmux.Client, id, command string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for pane(t, tm, id).Command != command {
		if time.Now().After(deadline) {
			t.Fatalf("pane %s never ran %s", id, command)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func waitForFile(t *testing.T, path, want string) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		got, _ := os.ReadFile(path)
		if string(got) == want || time.Now().After(deadline) {
			return string(got)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestShellPaneReadsExitedAndMissingPaneReadsGone(t *testing.T) {
	e, l := hubFixture(t)
	if _, err := e.NewFactoryPane(l, NewPane{Repo: "/repo", Slug: "exited", Dir: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	running, err := e.TM.NewWindow(tmux.Window{Session: "factory", Name: ParkName("running"), Command: "exec sleep 600"})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Tag(running, "/repo", "running", ""); err != nil {
		t.Fatal(err)
	}
	waitForCommand(t, e.TM, running, "sleep")
	panes, err := e.TM.Panes()
	if err != nil {
		t.Fatal(err)
	}

	cases := map[string]factory.Status{"exited": factory.Exited, "running": factory.Running, "gone": factory.Gone}
	for slug, want := range cases {
		f := factory.Factory{Repo: "/repo", Slug: slug, State: &state.State{Stage: "implement"}}
		live := factory.Live{Pane: LivePane(panes, f), Hook: &state.HookFile{Main: &state.HookEvent{Event: "PreToolUse", At: time.Now()}}}
		if got := factory.StatusOf(f, live, time.Now(), time.Hour); got != want {
			t.Errorf("%s: status = %s, want %s", slug, got, want)
		}
	}
}
