package hub

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/afonsodelgado/factory/internal/factory"
	"github.com/afonsodelgado/factory/internal/host"
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

	framed(t, e, l, "after build")

	created, err := e.NewFactoryPane(l, NewPane{Repo: "/repo", Slug: "eng-1-x", Dir: t.TempDir()})
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
	if selected.WindowName != HubWindow || selected.Slug != "eng-1-x" {
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
	if name := pane(t, e.TM, created).WindowName; name != host.ParkName("eng-1-x") {
		t.Errorf("deselected factory pane is in %q, want its own parking window", name)
	}
	sized("parked again", created)

	for _, size := range [][2]int{{160, 44}, {120, 30}, {160, 44}} {
		if err := e.TM.ResizeWindow(l.Window, size[0], size[1]); err != nil {
			t.Fatal(err)
		}
		if err := e.Arrange(l, Shape{Width: size[0], Height: size[1]}); err != nil {
			t.Fatal(err)
		}
		when := fmt.Sprintf("after resizing to %dx%d", size[0], size[1])
		framed(t, e, l, when)
		if slotW, slotH, err = e.SlotSize(l); err != nil {
			t.Fatal(err)
		}
		sized(when, created)
	}
}

func TestSplitsStayWithTheFactoryTheyWereMadeIn(t *testing.T) {
	e, l := hubFixture(t)
	a, err := e.NewFactoryPane(l, NewPane{Repo: "/repo", Slug: "a", Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	b, err := e.NewFactoryPane(l, NewPane{Repo: "/repo", Slug: "b", Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Select(&l, a); err != nil {
		t.Fatal(err)
	}
	beside, err := e.TM.Split(tmux.Split{Target: a, Horizontal: true, Command: "exec sleep 600"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.TM.Split(tmux.Split{Target: beside, Command: "exec sleep 600"}); err != nil {
		t.Fatal(err)
	}
	split, err := e.groupOf(l.Window, l.Slot)
	if err != nil {
		t.Fatal(err)
	}
	made := split.cell.String()

	if err := e.Select(&l, b); err != nil {
		t.Fatal(err)
	}
	if shown, _ := e.groupOf(l.Window, l.Slot); !shown.cell.Leaf() {
		t.Errorf("b shows %v; want b alone, without the splits made beside a", shown.cell.Leaves())
	}
	framed(t, e, l, "with b shown")
	for _, id := range split.cell.Leaves() {
		if name := pane(t, e.TM, id).WindowName; name != host.ParkName("a") {
			t.Errorf("a's pane %s is in %q, want it parked with a", id, name)
		}
	}

	if err := e.Select(&l, a); err != nil {
		t.Fatal(err)
	}
	if back, _ := e.groupOf(l.Window, l.Slot); back.cell.String() != made {
		t.Errorf("a came back as %s; want the splits as they were made, %s", back.cell, made)
	}

	agentWidth := pane(t, e.TM, a).Width
	stage, err := e.NewStagePane(l, StagePane{Repo: "/repo", Slug: "a", Role: "engineer", Token: "t", Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.ShowStage(&l, stage); err != nil {
		t.Fatal(err)
	}
	if got, want := areaOf(t, e.TM, stage).h, areaOf(t, e.TM, l.Frame.Left).h-4; got != want {
		t.Errorf("stage is %d rows tall, want %d: it belongs beside all of a's panes, not beside one of them", got, want)
	}
	if err := e.HideStage(&l); err != nil {
		t.Fatal(err)
	}
	if got := pane(t, e.TM, a).Width; got < agentWidth-2 || got > agentWidth+2 {
		t.Errorf("a is %d columns wide after the stage left, want about %d: the split beside it took the stage's room", got, agentWidth)
	}
}

type area struct{ x, y, w, h int }

func areaOf(t *testing.T, tm tmux.Client, id string) area {
	t.Helper()
	out, err := tm.Display(id, "#{pane_left} #{pane_top} #{pane_width} #{pane_height}")
	if err != nil {
		t.Fatal(err)
	}
	var a area
	fmt.Sscan(out, &a.x, &a.y, &a.w, &a.h)
	return a
}

func framed(t *testing.T, e *Engine, l Layout, when string) {
	t.Helper()
	slot, left, right, top, bottom := areaOf(t, e.TM, l.Slot), areaOf(t, e.TM, l.Frame.Left), areaOf(t, e.TM, l.Frame.Right), areaOf(t, e.TM, l.Frame.Top), areaOf(t, e.TM, l.Frame.Bottom)
	thin := left.w == 1 && right.w == 1 && top.h == 1 && bottom.h == 1
	inset := slot.x == left.x+2 && slot.y == top.y+2 && right.x == slot.x+slot.w+1 && bottom.y == slot.y+slot.h+1
	spanning := left.y == top.y && left.h == slot.h+4 && right.h == left.h && top.w == slot.w && bottom.w == slot.w
	if !thin || !inset || !spanning {
		t.Errorf("%s: slot %+v, left %+v, right %+v, top %+v, bottom %+v; want one-cell strips around the slot, inset by two cells: grown strips reflow Claude", when, slot, left, right, top, bottom)
	}
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
	if top(slot.ID) >= top(panel.ID) || slot.Width != panel.Width-4 {
		t.Errorf("slot at %d (%dx%d), panel at %d (%dx%d): the slot must sit above the panel in the right column, inside its frame", top(slot.ID), slot.Width, slot.Height, top(panel.ID), panel.Width, panel.Height)
	}
	framed(t, e, rebuilt, "after the rebuild")
}

func TestAdoptsOnlyLegacyFactorySessions(t *testing.T) {
	e, l := hubFixture(t)
	repo, sameSlugElsewhere := t.TempDir(), t.TempDir()
	for _, r := range []string{repo, sameSlugElsewhere} {
		if err := os.MkdirAll(filepath.Join(factory.WorktreesDir(r), "eng-2-y"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	legacy, err := e.TM.Run("new-session", "-d", "-P", "-F", "#{pane_id}", "-s", "eng-2-y", "-c", filepath.Join(factory.WorktreesDir(repo), "eng-2-y"), "exec sleep 600")
	if err != nil {
		t.Fatal(err)
	}
	waitForCommand(t, e.TM, legacy, "sleep")
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
	p, ok := host.FactoryPane(panes, repo, "eng-2-y")
	if !ok || p.WindowName != host.ParkName("eng-2-y") || p.Session != "factory" {
		t.Errorf("adopted pane = %+v, want it tagged to %s, whose worktree it runs in, and parked: the first repo with the slug would get a second claude on wake", p, repo)
	}
}

func TestDeliverTypesIntoAnIdlePaneAndRefusesCopyMode(t *testing.T) {
	e, l := hubFixture(t)
	out := filepath.Join(t.TempDir(), "typed")
	id, err := e.TM.NewWindow(tmux.Window{Session: "factory", Name: host.ParkName("eng-3-z"), Command: "exec cat > " + out})
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

func TestWakeTypesNothingIntoAPaneThatLeftItsShell(t *testing.T) {
	e, l := hubFixture(t)
	out := filepath.Join(t.TempDir(), "typed")
	id, err := e.TM.NewWindow(tmux.Window{Session: "factory", Name: host.ParkName("eng-3-z"), Command: "exec cat > " + out})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Tag(id, "/repo", "eng-3-z"); err != nil {
		t.Fatal(err)
	}
	waitForCommand(t, e.TM, id, "cat")
	polledAtTheShell := pane(t, e.TM, id)
	polledAtTheShell.Command = "zsh"
	exited := Item{Status: factory.Exited, Pane: &polledAtTheShell, Factory: factory.Factory{Repo: "/repo", Slug: "eng-3-z", Worktree: t.TempDir(), Entry: &state.Entry{SessionID: "s3"}}}
	panes := &host.Panes{TM: e.TM, Session: e.Session}
	model, node := withNode(Model{Config: &state.Config{Repos: []string{"/repo"}, StateDir: t.TempDir()}, Engine: e, Layout: l}, host.Runtime{Engine: panes, Typist: panes, TM: e.TM})
	node.show(model, Snapshot{At: time.Now(), Items: []Item{exited}})

	err = model.wake(exited.Factory.Key())
	time.Sleep(300 * time.Millisecond)
	if typed, _ := os.ReadFile(out); err == nil || len(typed) != 0 {
		t.Errorf("wake returned %v and typed %q; the user restarted claude since the last poll, so the resume command would land in its prompt", err, typed)
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
	running, err := e.TM.NewWindow(tmux.Window{Session: "factory", Name: host.ParkName("running"), Command: "exec sleep 600"})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Tag(running, "/repo", "running"); err != nil {
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
		live := factory.Live{Pane: host.LivePane(panes, f), Hook: &state.HookFile{Main: &state.HookEvent{Event: "PreToolUse", At: time.Now()}}}
		if got := factory.StatusOf(f, live, time.Now(), time.Hour); got != want {
			t.Errorf("%s: status = %s, want %s", slug, got, want)
		}
	}
}

func TestPausingTheShownFactoryKeepsTheHubWhenItsViewerIsGone(t *testing.T) {
	e, l := hubFixture(t)
	created, err := e.NewFactoryPane(l, NewPane{Repo: "/repo", Slug: "x", Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	viewer := l.Slot
	if err := e.Select(&l, created); err != nil {
		t.Fatal(err)
	}
	if err := e.TM.KillPane(viewer); err != nil {
		t.Fatal(err)
	}
	if err := e.Unshow(&l, "/repo", "x"); err != nil {
		t.Fatal(err)
	}
	if err := (&host.Panes{TM: e.TM, Session: e.Session}).Pause("/repo", "x"); err != nil {
		t.Fatal(err)
	}
	panes, _ := e.TM.Panes()
	if _, ok := byID(panes, l.Selector); !ok {
		t.Fatal("pausing or deleting the shown factory killed the hub window, because no viewer was left to swap back into the slot")
	}
	if _, ok := host.FactoryPane(panes, "/repo", "x"); ok {
		t.Error("the factory pane survived the pause")
	}
}

func TestClosingAFactoryWithoutTheHubStopsItsStagesAndStrayProcesses(t *testing.T) {
	e, l := hubFixture(t)
	created, err := e.NewFactoryPane(l, NewPane{Repo: "/repo", Slug: "x", Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.NewStagePane(l, StagePane{Repo: "/repo", Slug: "x", Role: "engineer", Token: "t", Dir: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	pidFile := filepath.Join(t.TempDir(), "pid")
	if err := e.Type(created, "nohup sleep 600 >/dev/null 2>&1 & echo $! > "+pidFile); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	var stray int
	for stray == 0 && time.Now().Before(deadline) {
		data, _ := os.ReadFile(pidFile)
		fmt.Sscan(string(data), &stray)
		time.Sleep(20 * time.Millisecond)
	}
	if stray == 0 {
		t.Fatal("the factory pane never started its background process")
	}
	t.Cleanup(func() { syscall.Kill(stray, syscall.SIGKILL) })

	if err := CloseFactoryPanes(e.TM, "/repo", "x"); err != nil {
		t.Fatal(err)
	}
	panes, _ := e.TM.Panes()
	for _, p := range panes {
		if ofFactory(p, "/repo", "x") {
			t.Errorf("pane %s (%s) survived: deleting with the hub down left the factory's stage running", p.ID, p.WindowName)
		}
	}
	for syscall.Kill(stray, 0) == nil {
		if time.Now().After(deadline.Add(5 * time.Second)) {
			t.Fatalf("process %s started from the factory pane outlived the delete", strconv.Itoa(stray))
		}
		time.Sleep(20 * time.Millisecond)
	}
}
