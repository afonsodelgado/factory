package host

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/afonsodelgado/factory/internal/factory"
	"github.com/afonsodelgado/factory/internal/recipe"
	"github.com/afonsodelgado/factory/internal/state"
)

type Background interface {
	Start(key string, work func(context.Context) Message)
	Cancel(key string)
}

type BrowserLease struct {
	Factory  string `json:"factory"`
	Repo     string `json:"repo"`
	Slug     string `json:"slug"`
	Token    string `json:"token"`
	Result   string `json:"result"`
	Worktree string `json:"worktree"`
	Evidence string `json:"evidence"`
}

type Browsers interface {
	Check(profile state.Profile, worktree string) error
	Prepare(ctx context.Context, lease BrowserLease, profile state.Profile, waiting func(reason string)) (string, error)
	Release(factory, token string)
	Lost(factory, token string) bool
	Recover() []BrowserLease
}

type BrowserReady struct {
	Factory   string `json:"factory"`
	Token     string `json:"token"`
	MCPConfig string `json:"mcp_config,omitempty"`
	Error     string `json:"error,omitempty"`
	Failure   string `json:"failure,omitempty"`
}

const (
	FailRecipe  = "recipe_missing"
	FailTools   = "npx_missing"
	FailStack   = "stack_start"
	FailHealth  = "stack_health"
	FailSeed    = "seed"
	FailSignIn  = "sign_in"
	FailBrowser = "browser_start"
	FailCrash   = "chrome_crash"
	FailPane    = "qa_pane"
	FailRestart = "node_restart"
)

type BrowserFailure struct {
	Name string
	Err  error
}

func (f BrowserFailure) Error() string { return f.Err.Error() }

func (f BrowserFailure) Unwrap() error { return f.Err }

func failureName(err error) string {
	var f BrowserFailure
	if errors.As(err, &f) {
		return f.Name
	}
	return FailBrowser
}

type browserLease struct {
	BrowserReady
	paneSeen bool
}

var errBrowserGone = errors.New("QA's headless browser closed when its run ended; start a fresh QA stage with factory stage start --role qa")

func QADir(f factory.Factory) string {
	return filepath.Join(f.Worktree, ".claude", "qa", f.Slug)
}

func EvidenceDir(f factory.Factory, run state.StageRun) string {
	return filepath.Join(QADir(f), fmt.Sprintf("%s-%d", run.Role, run.Round))
}

func browserJob(key string) string { return "browser " + key }

func (m *Runtime) browsesForQA(it Item, role string) (state.Profile, bool) {
	_, profile := m.repoConfig(it.Factory.Repo)
	if resolved, err := recipe.Resolve(it.Factory.Worktree); err == nil && resolved.Profile.QA.Mode != "" {
		profile = resolved.Profile
	}
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
	f, browsers := it.Factory, m.Browsers
	m.stopBrowser(f.Key())
	lease := BrowserLease{Factory: f.Key(), Repo: f.Repo, Slug: f.Slug, Token: run.Token, Result: run.Result, Worktree: f.Worktree, Evidence: EvidenceDir(f, run)}
	m.preparing[lease.Factory] = run.Token
	waiting := func(reason string) { m.note(f, "browser QA is waiting for a headless browser: "+reason) }
	m.Jobs.Start(browserJob(lease.Factory), func(ctx context.Context) Message {
		config, err := browsers.Prepare(ctx, lease, profile, waiting)
		ready := BrowserReady{Factory: lease.Factory, Token: lease.Token, MCPConfig: config}
		if err != nil {
			ready.Error, ready.Failure = err.Error(), failureName(err)
		}
		data, _ := json.Marshal(ready)
		return Message{T: "browser", Data: data}
	})
}

func (m *Runtime) BrowserReady(b BrowserReady) error {
	if m.preparing[b.Factory] == b.Token {
		delete(m.preparing, b.Factory)
	}
	it, found := m.snap.Find(b.Factory)
	run, current := m.currentQARun(it.Factory, b.Token)
	switch {
	case !found || !current || stagesEnded(it) || run.Finished():
		m.Browsers.Release(b.Factory, b.Token)
		return nil
	case b.Error != "":
		return m.failBrowser(it.Factory, run, b.Failure, b.Error)
	}
	m.leases[b.Factory] = browserLease{BrowserReady: b}
	launch := m.StageLaunch(it, run, "")
	if err := m.OpenStagePane(it, run, launch.Command()); err != nil {
		return m.failBrowser(it.Factory, run, FailPane, "the QA pane did not open: "+err.Error())
	}
	return nil
}

func (m *Runtime) failBrowser(f factory.Factory, run state.StageRun, failure, reason string) error {
	m.Browsers.Release(f.Key(), run.Token)
	if m.leases[f.Key()].Token == run.Token {
		delete(m.leases, f.Key())
	}
	return m.closeQA(f, run, failure, reason, EvidenceDir(f, run))
}

func (m *Runtime) closeQA(f factory.Factory, run state.StageRun, failure, reason, evidence string) error {
	if m.Engine != nil {
		m.Engine.KillStages(f.Repo, f.Slug, run.Role)
	}
	m.note(f, "browser QA failed at "+failure+": "+reason)
	return WriteFile(run.Result, failedResult(f.State, run, failure, reason, evidence))
}

func (m *Runtime) currentQARun(f factory.Factory, token string) (state.StageRun, bool) {
	runs, err := state.LoadStageRuns(state.StageRunsPath(m.Config.StateDir, f.Repo, f.Slug))
	if err != nil {
		return state.StageRun{}, false
	}
	latest, ok := runs.Latest("qa")
	return latest, ok && latest.Token == token
}

func (m *Runtime) stopBrowser(key string) {
	if m.Browsers == nil || m.Jobs == nil {
		return
	}
	if token, ok := m.preparing[key]; ok {
		m.Jobs.Cancel(browserJob(key))
		m.Browsers.Release(key, token)
		delete(m.preparing, key)
	}
	if lease, ok := m.leases[key]; ok {
		m.Browsers.Release(key, lease.Token)
		delete(m.leases, key)
	}
}

func (m *Runtime) ReleaseFinishedBrowsers() {
	for key, token := range m.preparing {
		it, found := m.snap.Find(key)
		if _, current := m.currentQARun(it.Factory, token); !found || !current || stagesEnded(it) {
			m.stopBrowser(key)
		}
	}
	for key, lease := range m.leases {
		it, found := m.snap.Find(key)
		run, current := m.currentQARun(it.Factory, lease.Token)
		_, live := LiveStagePane(stagePaneOf(m.snap.Panes, it.Factory, "qa", lease.Token))
		switch {
		case !found || !current || stagesEnded(it) || run.Finished():
			m.stopBrowser(key)
		case m.Browsers.Lost(key, lease.Token):
			m.failBrowser(it.Factory, run, FailCrash, "headless Chrome exited while QA was using it")
		case lease.paneSeen && !live:
			m.failBrowser(it.Factory, run, FailPane, "the QA pane closed before it wrote a result")
		case live && !lease.paneSeen:
			lease.paneSeen = true
			m.leases[key] = lease
		}
	}
}

func (m *Runtime) recoverBrowsers() {
	if m.Browsers == nil {
		return
	}
	for _, lease := range m.Browsers.Recover() {
		f := factory.Factory{Repo: lease.Repo, Slug: lease.Slug, Worktree: lease.Worktree}
		if run, current := m.currentQARun(f, lease.Token); current && !run.Finished() {
			m.closeQA(f, run, FailRestart, "the node stopped while this QA ran; its browser, stack and profile were removed", lease.Evidence)
		}
	}
}

func stagesEnded(it Item) bool {
	return it.Status == factory.Done || it.Status == factory.Gone || it.Factory.Archived()
}

func failedResult(snapshot *state.State, run state.StageRun, failure, reason, evidence string) string {
	var heads []state.StackBranch
	for _, active := range snapshot.Workflow.Runs {
		if active.ID == run.ID {
			heads = active.InputHeads
		}
	}
	data, _ := json.Marshal(map[string]any{
		"schema_version": 2,
		"run_id":         run.ID,
		"input_heads":    heads,
		"output_heads":   snapshot.Stack,
		"status":         "failed",
		"summary":        "browser QA failed at " + failure + ": " + reason,
		"outputs":        []string{},
		"questions":      []string{},
		"data":           map[string]any{"failure": failure, "evidence": evidence},
	})
	return string(data) + "\n"
}

func evidenceNote(dir string) string {
	return "Save every screenshot and console or network excerpt from this run under " + dir + ". The node appends the page's console errors and failed requests to " + dir + "/console.log while it runs and adds a final screenshot.png when the run ends."
}
