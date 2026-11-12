package hub

import (
	"github.com/afonsodelgado/factory/internal/host"
	"github.com/afonsodelgado/factory/internal/tmux"
)

func ofFactory(p tmux.Pane, repo, slug string) bool { return host.OfFactory(p, repo, slug) }
func FactoryProcesses(panes []tmux.Pane, repo, slug string) []int {
	return host.FactoryProcesses(panes, repo, slug)
}
func Terminate(pids []int) { host.Terminate(pids) }
func CloseFactoryPanes(tm tmux.Client, repo, slug string) error {
	return host.CloseFactoryPanes(tm, repo, slug)
}
