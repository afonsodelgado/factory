package hub

import (
	"github.com/afonsodelgado/factory/internal/factory"
	"github.com/afonsodelgado/factory/internal/host"
	"github.com/afonsodelgado/factory/internal/state"
)

type Item = host.Item
type Snapshot = host.Snapshot
type Loader = host.Loader

func withoutTokens(hooks []*state.HookFile, tokens map[string]bool) []*state.HookFile {
	return host.WithoutTokens(hooks, tokens)
}
func hookFor(f factory.Factory, token string, hooks []*state.HookFile) *state.HookFile {
	return host.HookFor(f, token, hooks)
}
func within(path, dir string) bool { return host.Within(path, dir) }
