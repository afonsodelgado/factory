package hub

import (
	"github.com/afonsodelgado/factory/internal/host"
)

type RemovalCheck = host.RemovalCheck

func CheckRemoval(worktree string) RemovalCheck { return host.CheckRemoval(worktree) }
func FactoryDirRescue(repo, worktree, dir string) (string, bool) {
	return host.FactoryDirRescue(repo, worktree, dir)
}
func RemoveWorktree(repo, worktree, factoryDir string) (string, error) {
	return host.RemoveWorktree(repo, worktree, factoryDir)
}
