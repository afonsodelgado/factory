package dialog

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"

	"github.com/afonsodelgado/factory/internal/install"
	"github.com/afonsodelgado/factory/internal/state"
	"github.com/afonsodelgado/factory/internal/ui/theme"
)

func (m *NewFactoryModel) addingRepo() bool { return m.repo == len(m.Context.Repos) }

func sharedRepoDirectory(repos []RepoOption) string {
	if len(repos) == 0 {
		return ""
	}
	common := filepath.Dir(repos[0].Path)
	for _, repo := range repos[1:] {
		dir := filepath.Dir(repo.Path)
		for common != string(filepath.Separator) && dir != common && !strings.HasPrefix(dir, common+string(filepath.Separator)) {
			common = filepath.Dir(common)
		}
	}
	return strings.TrimSuffix(install.HomeRelative(common), string(filepath.Separator)) + string(filepath.Separator)
}

func sameRepo(a, b string) bool {
	if filepath.Clean(a) == filepath.Clean(b) {
		return true
	}
	ai, ae := os.Stat(a)
	bi, be := os.Stat(b)
	return ae == nil && be == nil && os.SameFile(ai, bi)
}

func (m *NewFactoryModel) prepareSettings(path string) tea.Cmd {
	context, err := m.prepareRepo(path)
	if err != nil {
		m.problem = err.Error()
		return nil
	}
	m.result = &NewResult{Repo: context.Repo, Settings: &context}
	return tea.Quit
}

func (m *NewFactoryModel) prepareRepo(path string) (SettingsContext, error) {
	var context SettingsContext
	path = state.ExpandHome(path)
	info, err := os.Stat(path)
	if err != nil {
		return context, err
	}
	if !info.IsDir() {
		return context, fmt.Errorf("%s is not a directory", path)
	}
	repo, _, err := install.MainCheckout(path)
	if err != nil {
		return context, err
	}
	listed := false
	for _, known := range m.Context.Repos {
		if sameRepo(repo, known.Path) {
			repo, listed = known.Path, true
			break
		}
	}
	context.Repo = repo
	_, statErr := os.Stat(state.RepoConfigPath(repo))
	if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return context, statErr
	}
	needsSetup := !listed || errors.Is(statErr, os.ErrNotExist) || m.setupRetry[repo]
	if !needsSetup {
		context.Banner = filepath.Base(repo) + " is already in the hub, so nothing was set up again."
		return context, nil
	}
	if m.setupRetry == nil {
		m.setupRetry = map[string]bool{}
	}
	m.setupRetry[repo] = true
	result, err := install.Init(repo, m.Context.StateDir)
	if err != nil {
		return context, err
	}
	delete(m.setupRetry, repo)
	context.Banner = "✓ Added " + filepath.Base(repo) + " to the hub. Setup wrote these values; check them, then save."
	top, err := exec.Command("git", "-C", path, "rev-parse", "--show-toplevel").Output()
	switch {
	case err == nil && !sameRepo(strings.TrimSpace(string(top)), repo):
		context.Banner = "✓ Added " + filepath.Base(repo) + ", the main checkout of the worktree you typed."
	case result.CopiedFrom != "":
		context.Banner = "✓ Copied .claude/factory.yml to .factory/config.yml. The form edits the new file."
	case listed:
		context.Banner = "✓ Set up the missing repo config. Check these values, then save."
	}
	return context, nil
}

type directoryCompletion struct {
	suffix  string
	matches []string
	problem string
}

func completeDirectory(input string) directoryCompletion {
	expanded := state.ExpandHome(input)
	if input == "~" {
		expanded += string(filepath.Separator)
	}
	dir, prefix := filepath.Split(expanded)
	if dir == "" {
		dir = "."
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return directoryCompletion{problem: err.Error()}
	}
	var matches []string
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), prefix) {
			continue
		}
		info, err := os.Stat(filepath.Join(dir, entry.Name()))
		if err == nil && info.IsDir() {
			matches = append(matches, entry.Name()+string(filepath.Separator))
		}
	}
	if len(matches) == 0 {
		return directoryCompletion{problem: "No matching directories."}
	}
	common := matches[0]
	for _, match := range matches[1:] {
		for !strings.HasPrefix(match, common) {
			_, size := utf8.DecodeLastRuneInString(common)
			common = common[:len(common)-size]
		}
	}
	suffix := strings.TrimPrefix(common, prefix)
	if input == "~" {
		suffix = "/" + suffix
	}
	return directoryCompletion{suffix: suffix, matches: matches}
}

func (m *NewFactoryModel) drawRepoPath(c *theme.Canvas, y, w int) {
	c.Put(1, y, "Repo path", theme.Bright.B())
	y++
	drawInput(c, 1, y, w, "", []string{m.repoPath + "█"}, true, theme.Focused)
	completion := completeDirectory(m.repoPath)
	c.PutMax(3+theme.Width(m.repoPath)+1, y+1, completion.suffix, theme.Muted, max(w-5-theme.Width(m.repoPath), 0))
	y += 3
	dir, _ := filepath.Split(state.ExpandHome(m.repoPath))
	if m.repoPath == "~" {
		dir = state.ExpandHome("~")
	}
	for _, match := range completion.matches[:min(5, len(completion.matches))] {
		path := filepath.Join(dir, match)
		badge := ""
		if _, err := os.Stat(filepath.Join(path, ".git")); err == nil {
			badge = "git repo"
		}
		for _, repo := range m.Context.Repos {
			if sameRepo(path, repo.Path) {
				badge = "in the hub"
				break
			}
		}
		x := c.Put(3, y, match, theme.Quiet)
		c.Put(x+2, y, badge, theme.Muted)
		y++
	}
	if completion.problem != "" {
		c.PutMax(1, y, completion.problem, theme.Muted, w)
		y++
	}
	if m.problem != "" {
		for _, line := range theme.Wrap("✗ "+m.problem, w) {
			c.Put(1, y, line, theme.FgInk(theme.Red))
			y++
		}
	}
	c.Put(1, c.H-1, "⏎ add and configure   tab complete   ↑↓ repo   esc cancel", theme.Text)
}
