package hub

import (
	"context"
	"os"
	"os/exec"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/afonsodelgado/factory/internal/catalog"
	"github.com/afonsodelgado/factory/internal/state"
	"github.com/afonsodelgado/factory/internal/ui/dialog"
)

const (
	catalogMaxAge  = 24 * time.Hour
	catalogTimeout = 20 * time.Second
)

func (m *Model) toolOptions() []dialog.ToolOption {
	codex := toolOption(state.ToolCodex, func() ([]catalog.Model, error) { return catalog.Codex(state.CodexHome()) })
	codex.Default = catalog.CodexDefault(state.CodexHome())
	openCode := toolOption(state.ToolOpenCode, func() ([]catalog.Model, error) { return catalog.OpenCode(m.Config.StateDir) })
	return []dialog.ToolOption{{Name: state.ToolClaude, Models: catalog.Claude()}, codex, openCode}
}

func toolOption(name string, list func() ([]catalog.Model, error)) dialog.ToolOption {
	option := dialog.ToolOption{Name: name}
	if _, err := exec.LookPath(name); err != nil {
		option.Problem = name + " is not installed."
		return option
	}
	models, err := list()
	if err != nil {
		option.Problem = err.Error() + "."
	}
	option.Models = models
	return option
}

func (m *Model) refreshCatalog() tea.Cmd {
	stateDir := m.Config.StateDir
	if stateDir == "" {
		return nil
	}
	return func() tea.Msg {
		if _, err := exec.LookPath(state.ToolOpenCode); err != nil {
			return nil
		}
		if info, err := os.Stat(catalog.OpenCodeCachePath(stateDir)); err == nil && time.Since(info.ModTime()) < catalogMaxAge {
			return nil
		}
		ctx, cancel := context.WithTimeout(context.Background(), catalogTimeout)
		defer cancel()
		catalog.RefreshOpenCode(ctx, stateDir)
		return nil
	}
}
