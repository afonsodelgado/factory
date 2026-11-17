package host

import (
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"syscall"

	"github.com/afonsodelgado/factory/internal/tmux"
)

func (m *Runtime) quiesce(it Item) error {
	blockers := slices.Concat(m.runningStages(it), m.undelivered(it), m.uncertain(it.Factory.Key()))
	if len(blockers) > 0 {
		return fmt.Errorf("%s cannot be checkpointed yet: %s", it.Factory.Label(), strings.Join(blockers, "; "))
	}
	f := it.Factory
	m.stopStack(f.Key())
	m.stopBrowser(f.Key())
	if m.Engine != nil && HasStagePanes(m.snap.Panes, f) {
		return m.Engine.KillStages(f.Repo, f.Slug, "")
	}
	return nil
}

func OfFactory(p tmux.Pane, repo, slug string) bool {
	return p.Repo == repo && (p.Slug == slug || p.StageOf == slug)
}

func FactoryProcesses(panes []tmux.Pane, repo, slug string) []int {
	var roots []int
	for _, p := range panes {
		if OfFactory(p, repo, slug) && p.PID > 0 {
			roots = append(roots, p.PID)
		}
	}
	return Descendants(roots)
}

func Descendants(roots []int) []int {
	out, err := exec.Command("ps", "-A", "-o", "pid=,ppid=").Output()
	if err != nil || len(roots) == 0 {
		return nil
	}
	parents, children := map[int]int{}, map[int][]int{}
	for _, line := range strings.Split(string(out), "\n") {
		var pid, ppid int
		if _, err := fmt.Sscan(line, &pid, &ppid); err == nil {
			parents[pid] = ppid
			children[ppid] = append(children[ppid], pid)
		}
	}
	spared := map[int]bool{}
	for pid := os.Getpid(); pid > 1 && !spared[pid]; pid = parents[pid] {
		spared[pid] = true
	}
	var found []int
	queue := slices.Clone(roots)
	for len(queue) > 0 {
		pid := queue[0]
		queue = queue[1:]
		for _, child := range children[pid] {
			if !spared[child] {
				found = append(found, child)
			}
		}
		queue = append(queue, children[pid]...)
	}
	return found
}

func ShellInForeground(shell int) bool {
	out, err := exec.Command("ps", "-o", "tpgid=", "-p", strconv.Itoa(shell)).Output()
	if err != nil {
		return true
	}
	foreground, err := strconv.Atoi(strings.TrimSpace(string(out)))
	return err != nil || foreground == shell
}

func Terminate(pids []int) {
	for _, pid := range pids {
		syscall.Kill(pid, syscall.SIGTERM)
	}
}

func CloseFactoryPanes(tm tmux.Client, repo, slug string) error {
	panes, err := tm.Panes()
	if err != nil {
		return err
	}
	strays := FactoryProcesses(panes, repo, slug)
	closed := map[string]bool{}
	for _, p := range panes {
		if !OfFactory(p, repo, slug) || closed[p.WindowID] {
			continue
		}
		if inHubWindow(panes, p) {
			err = tm.KillPane(p.ID)
		} else {
			closed[p.WindowID] = true
			err = tm.KillWindow(p.WindowID)
		}
		if err != nil {
			return err
		}
	}
	Terminate(strays)
	return nil
}
