package host

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/afonsodelgado/factory/internal/factory"
	"github.com/afonsodelgado/factory/internal/state"
)

type Background interface {
	Start(key string, work func(context.Context) Message)
}

type Browsers interface {
	Check(profile state.Profile, worktree string) error
	Prepare(ctx context.Context, key string, profile state.Profile, worktree string) (string, error)
	Release(key string)
}

type BrowserReady struct {
	Factory   string `json:"factory"`
	Token     string `json:"token"`
	MCPConfig string `json:"mcp_config,omitempty"`
	Error     string `json:"error,omitempty"`
}

var errBrowserGone = errors.New("QA's headless browser closed when its run ended; start a fresh QA stage with factory stage start --role qa")

func (m *Runtime) browsesForQA(it Item, role string) (state.Profile, bool) {
	_, profile := m.repoConfig(it.Factory.Repo)
	return profile, role == "qa" && profile.BrowserQA()
}

func (m *Runtime) checkBrowser(profile state.Profile, worktree string) error {
	if m.Browsers == nil || m.Jobs == nil {
		return errors.New("browser QA runs only inside factory node run, which drives headless Chrome")
	}
	if err := m.Browsers.Check(profile, worktree); err != nil {
		return fmt.Errorf("browser QA did not start and no browser was launched: %w", err)
	}
	return nil
}

func (m *Runtime) prepareBrowser(it Item, run state.StageRun, profile state.Profile) {
	key, worktree, browsers := it.Factory.Key(), it.Factory.Worktree, m.Browsers
	delete(m.leases, key)
	m.Jobs.Start("browser "+key, func(ctx context.Context) Message {
		ready := BrowserReady{Factory: key, Token: run.Token}
		config, err := browsers.Prepare(ctx, key, profile, worktree)
		if err != nil {
			ready.Error = err.Error()
		}
		ready.MCPConfig = config
		data, _ := json.Marshal(ready)
		return Message{T: "browser", Data: data}
	})
}

func (m *Runtime) BrowserReady(b BrowserReady) error {
	it, found := m.snap.Find(b.Factory)
	run, current := m.currentQARun(it, b.Token)
	switch {
	case !found || !current || stagesEnded(it) || run.Finished():
		m.Browsers.Release(b.Factory)
		return nil
	case b.Error != "":
		return m.failBrowser(it, run, b.Error)
	}
	m.leases[b.Factory] = b
	launch := m.StageLaunch(it, run, "")
	if err := m.OpenStagePane(it, run, launch.Command()); err != nil {
		delete(m.leases, b.Factory)
		return m.failBrowser(it, run, "the QA pane did not open: "+err.Error())
	}
	return nil
}

func (m *Runtime) failBrowser(it Item, run state.StageRun, reason string) error {
	m.Browsers.Release(it.Factory.Key())
	m.note(it.Factory, "browser QA did not start: "+reason)
	return WriteFile(run.Result, failedResult("browser QA did not start: "+reason))
}

func (m *Runtime) currentQARun(it Item, token string) (state.StageRun, bool) {
	runs, err := state.LoadStageRuns(state.StageRunsPath(m.Config.StateDir, it.Factory.Repo, it.Factory.Slug))
	if err != nil {
		return state.StageRun{}, false
	}
	latest, ok := runs.Latest("qa")
	return latest, ok && latest.Token == token
}

func (m *Runtime) ReleaseFinishedBrowsers() {
	for key, lease := range m.leases {
		it, found := m.snap.Find(key)
		run, current := m.currentQARun(it, lease.Token)
		if !found || stagesEnded(it) || !current || run.Finished() {
			m.Browsers.Release(key)
			delete(m.leases, key)
		}
	}
}

func stagesEnded(it Item) bool {
	return it.Status == factory.Done || it.Status == factory.Gone || it.Factory.Archived()
}

func failedResult(summary string) string {
	data, _ := json.Marshal(map[string]any{"status": "failed", "summary": summary, "outputs": []string{}, "questions": []string{}, "data": map[string]any{}})
	return string(data) + "\n"
}
