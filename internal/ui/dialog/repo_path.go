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

	"github.com/afonsodelgado/factory/internal/gitclone"
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
	setup, err := m.setUpRepo(path)
	if err != nil {
		m.problem = err.Error()
		return nil
	}
	context := SettingsContext{Repo: setup.repo, Banner: setup.banner()}
	m.result = &NewResult{Repo: setup.repo, Settings: &context}
	return tea.Quit
}

func (m *NewFactoryModel) addRepo(path string) tea.Cmd {
	setup, err := m.setUpRepo(path)
	if err != nil {
		m.problem = err.Error()
		return nil
	}
	added := setup.added()
	m.result = &NewResult{Repo: setup.repo, Added: &added}
	return tea.Quit
}

type repoSetup struct {
	repo, typed string
	listed      bool
	ran         bool
	retrying    bool
	result      install.InitResult
}

func (m *NewFactoryModel) setUpRepo(path string) (repoSetup, error) {
	path = state.ExpandHome(path)
	s := repoSetup{typed: path}
	info, err := os.Stat(path)
	if err != nil {
		return s, err
	}
	if !info.IsDir() {
		return s, fmt.Errorf("%s is not a directory", path)
	}
	repo, _, err := install.MainCheckout(path)
	if err != nil {
		return s, fmt.Errorf("%s is not a git repository. Clone the repo there first, or type the path of one that is", install.HomeRelative(path))
	}
	for _, known := range m.Context.Repos {
		if sameRepo(repo, known.Path) {
			repo, s.listed = known.Path, true
			break
		}
	}
	s.repo = repo
	_, statErr := os.Stat(state.RepoConfigPath(repo))
	if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return s, statErr
	}
	if s.listed && statErr == nil && !m.setupRetry[repo] {
		return s, nil
	}
	if m.setupRetry == nil {
		m.setupRetry = map[string]bool{}
	}
	s.retrying = m.setupRetry[repo]
	m.setupRetry[repo] = true
	if s.result, err = install.Init(repo, m.Context.StateDir); err != nil {
		return s, err
	}
	delete(m.setupRetry, repo)
	s.repo, s.ran = s.result.Repo, true
	return s, nil
}

func (s repoSetup) fromWorktree() bool {
	top, err := exec.Command("git", "-C", s.typed, "rev-parse", "--show-toplevel").Output()
	return err == nil && !sameRepo(strings.TrimSpace(string(top)), s.repo)
}

func (s repoSetup) banner() string {
	name := filepath.Base(s.repo)
	switch {
	case !s.ran:
		return name + " is already in the hub, so nothing was set up again."
	case s.fromWorktree():
		return "✓ Added " + name + ", the main checkout of the worktree you typed."
	case s.result.CopiedFrom != "":
		return "✓ Copied .claude/factory.yml to .factory/config.yml. The form edits the new file."
	case s.retrying:
		return "✓ Finished setting up " + name + ". Check these values, then save."
	case s.listed:
		return "✓ Set up the missing repo config. Check these values, then save."
	case !s.result.ProfileWritten:
		return "✓ Added " + name + " to the hub. It already had a config; these are its values."
	}
	return "✓ Added " + name + " on this machine. Blank commands are discovered by its first factory here."
}

func (s repoSetup) added() RepoAdded {
	name, config := filepath.Base(s.repo), install.HomeRelative(state.RepoConfigPath(s.repo))
	added := RepoAdded{Repo: s.repo, Notice: "✓ Added " + name + " on this machine."}
	_, remoteErr := gitclone.RemoteURL(s.repo)
	switch {
	case !s.ran:
		added.Notice = name + " is already in the hub, so nothing was set up again."
	case s.result.ProfileWritten && s.result.CopiedFrom == "":
		added.Detail = "Its first factory here works out how to build, lint, test and run it, runs each command on this machine and saves the ones that work in " + config + ". ctrl+e shows or edits them."
	default:
		added.Notice += " It already had a repo config, so its commands stay as they are; a factory discovers only the ones left blank."
	}
	if s.fromWorktree() {
		added.Notice = "✓ Added " + name + ", the main checkout of the worktree you typed."
	}
	switch {
	case errors.Is(remoteErr, gitclone.ErrNoNetworkRemote):
		added.Warning = "! " + name + " has no network git remote, so it stays on this machine: no other node can add it, and its factories can't move. Push it to a remote to change that."
	case remoteErr != nil:
		added.Warning = "! Other nodes can't use " + name + " yet: " + remoteErr.Error() + "."
	}
	return added
}

type directoryCompletion struct {
	suffix  string
	matches []string
	problem string
}

func splitDirectoryInput(input string) (string, string) {
	expanded := state.ExpandHome(input)
	separator := string(filepath.Separator)
	if input == "~" || strings.HasSuffix(input, separator) {
		return expanded, ""
	}
	return filepath.Split(expanded)
}

func completeDirectory(input string) directoryCompletion {
	dir, prefix := splitDirectoryInput(input)
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
	dir, _ := splitDirectoryInput(m.repoPath)
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
	paragraph(c, y+1, "Adds the repo on this machine only. Clone it and add its secrets on each machine that should run it. Its first factory here works out how to build, lint, test and run it, so there is nothing to fill in.", theme.Muted)
	c.Put(1, c.H-1, "⏎ add   tab complete   ↑↓ repo   esc cancel", theme.Text)
}
