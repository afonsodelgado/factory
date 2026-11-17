package host

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/google/uuid"

	"github.com/afonsodelgado/factory/internal/catalog"
	"github.com/afonsodelgado/factory/internal/factory"
	"github.com/afonsodelgado/factory/internal/state"
)

type CreateRequest struct {
	Repo       string
	Slug       string
	Prompt     string
	Pipeline   string
	Stages     []string
	Agents     map[string]state.AgentSetting
	Supervisor state.AgentSetting
}

func (m *Runtime) Create(r CreateRequest) (string, error) {
	if r.Repo == "" || r.Slug == "" {
		return "", errors.New("a factory needs a repo and a name")
	}
	if existing, ok := m.ExistingFactory(r.Repo, r.Slug); ok {
		return m.selectExisting(existing)
	}
	if m.Loader == nil {
		return "", errors.New("the hub is not attached to tmux")
	}
	pipelines, err := m.Config.PipelinesFor(r.Repo)
	if err != nil {
		return "", err
	}
	pipeline, err := ChosenPipeline(pipelines, r)
	if err != nil {
		return "", err
	}
	if _, err := m.Config.AgentsFor(r.Repo); err != nil {
		return "", err
	}
	cfg, profile := m.repoConfig(r.Repo)
	agents := state.ResolveAgents(m.Config, profile, r.Agents)
	chosen := map[string]state.AgentSetting{}
	for role := range r.Agents {
		chosen[role] = agents[role]
	}
	if err := state.ValidateAgents("--agent", chosen); err != nil {
		return "", err
	}
	supervisor := r.Supervisor.Over(cfg.Supervisor)
	if err := state.ValidateSupervisor("--supervisor", supervisor); err != nil {
		return "", err
	}
	catalogs := catalog.Local(m.Config.StateDir)
	if err := catalog.Refusal(catalogs.ValidateAgents("--agent", chosen)); err != nil {
		return "", err
	}
	if err := catalog.Refusal(catalogs.Validate("--supervisor", "supervisor", supervisor)); err != nil {
		return "", err
	}
	supervisor = catalogs.WithDefault(supervisor)
	earlier := m.RetiredFactories(r.Repo, r.Slug)
	r.Slug = factory.FreshSlug(r.Slug, m.SlugTaken(r.Repo))
	launch := factory.Launch{Repo: r.Repo, Slug: r.Slug, Prompt: r.Prompt, Pipeline: pipeline.Name, Stages: CustomStages(pipeline), Agents: r.Agents, Supervisor: supervisor}
	if supervisor.OnClaude() {
		launch.SessionID = uuid.NewString()
	} else {
		launch.AddDirs = m.WritableDirs(r.Repo, "", supervisor)
	}
	title := FirstLine(r.Prompt)
	entry := state.Entry{Repo: r.Repo, Slug: r.Slug, Title: title, SessionID: launch.SessionID, CreatedAt: m.Now(), Created: true, Supervisor: supervisor, Agents: agents, Incarnation: uuid.NewString()}
	err = m.Loader.UpdateRegistry(func(reg *state.Registry) {
		PinDirs(reg, earlier)
		reg.Upsert(entry)
	})
	if err != nil {
		return "", err
	}
	if m.Engine == nil {
		return "", errors.New("the hub is not attached to tmux")
	}
	pane, err := m.Engine.NewFactoryPane(NewPane{Repo: r.Repo, Slug: r.Slug, Dir: r.Repo, Env: supervisorEnv(supervisor)})
	if err != nil {
		return "", err
	}
	if err := m.typeSupervisor(pane, factory.Factory{Repo: r.Repo, Slug: r.Slug, Entry: &entry}, launch.CreateCommand()); err != nil {
		return "", err
	}
	note := "created by factory"
	if r.Prompt != "" {
		note += fmt.Sprintf(" with a %d-line prompt", strings.Count(strings.TrimRight(r.Prompt, "\n"), "\n")+1)
	}
	m.Loader.Note(factory.Factory{Repo: r.Repo, Slug: r.Slug}, note)
	return state.Key(r.Repo, r.Slug), nil
}

func ChosenPipeline(pipelines state.Pipelines, r CreateRequest) (state.Pipeline, error) {
	if len(r.Stages) > 0 {
		return pipelines.Custom(r.Stages)
	}
	return pipelines.Find(r.Pipeline)
}

func CustomStages(p state.Pipeline) []string {
	if p.Name != state.CustomPipeline {
		return nil
	}
	return p.Stages
}

func FirstLine(s string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	if len([]rune(line)) > 80 {
		line = string([]rune(line)[:79]) + "…"
	}
	return line
}

func Retired(f factory.Factory) bool {
	finished := f.Done() || f.Archived()
	return finished && !DirExists(f.Worktree)
}

func Named(f factory.Factory, id string) bool {
	return f.Slug == id || strings.ToLower(f.Label()) == id
}

func (m *Runtime) RetiredMatch(repos []string, id string) (string, bool) {
	for _, it := range m.snap.Items {
		f := it.Factory
		if Retired(f) && slices.Contains(repos, f.Repo) && Named(f, id) {
			return f.Repo, true
		}
	}
	return "", false
}

func (m *Runtime) RetiredFactories(repo, id string) []factory.Factory {
	var earlier []factory.Factory
	for _, it := range m.snap.Items {
		f := it.Factory
		if f.Repo == repo && f.Dir != "" && Retired(f) && Named(f, id) {
			earlier = append(earlier, f)
		}
	}
	return earlier
}

func PinDirs(reg *state.Registry, factories []factory.Factory) {
	for _, f := range factories {
		entry := reg.Find(f.Repo, f.Slug)
		if entry == nil {
			entry = reg.Upsert(state.Entry{Repo: f.Repo, Slug: f.Slug, CreatedAt: f.CreatedAt()})
		}
		entry.Dir = resolved(f.Dir)
	}
}

func (m *Runtime) SlugTaken(repo string) func(string) bool {
	return func(slug string) bool {
		_, known := m.snap.Find(state.Key(repo, slug))
		return known || m.Loader.Known(repo, slug) || DirExists(filepath.Join(factory.WorktreesDir(repo), slug))
	}
}

func (m *Runtime) ExistingFactory(repo, id string) (string, bool) {
	for _, it := range m.snap.Items {
		f := it.Factory
		if f.Repo == repo && !Retired(f) && Named(f, id) {
			return f.Key(), true
		}
	}
	if id != "" && DirExists(filepath.Join(factory.WorktreesDir(repo), id)) {
		return state.Key(repo, id), true
	}
	return "", false
}

func DirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func (m *Runtime) Wake(key string) error {
	it, ok := m.snap.Find(key)
	if !ok || !it.Status.Asleep() || m.Engine == nil {
		return nil
	}
	if err := m.unverified(key); err != nil {
		return err
	}
	_, err := m.wake(it)
	return err
}

func (m *Runtime) wake(it Item) (string, error) {
	hooks, _ := state.ReadHookFiles(m.Config.StateDir)
	plan := PlanWake(it, WithoutTokens(hooks, m.snap.StageTokens), m.WritableDirs(it.Factory.Repo, it.Factory.Worktree, it.Factory.Supervisor()))
	var pane string
	switch plan.Kind {
	case WakeNewPane:
		dir := it.Factory.Worktree
		if !DirExists(dir) {
			return "", fmt.Errorf("the worktree %s is gone", dir)
		}
		created, err := m.Engine.NewFactoryPane(NewPane{Repo: it.Factory.Repo, Slug: it.Factory.Slug, Dir: dir, Env: supervisorEnv(it.Factory.Supervisor())})
		if err != nil {
			return "", err
		}
		if err := m.typeSupervisor(created, it.Factory, plan.Command); err != nil {
			return "", err
		}
		m.typeAfterStart(plan, created, it)
		pane = created
	case WakeShell:
		if now := m.refreshed(Target{Label: it.Factory.Label(), Pane: it.Pane, Hook: it.Hook}).Pane; now == nil || !factory.IsShell(now.Command) {
			return "", fmt.Errorf("%s's pane is no longer at a shell prompt, so wake typed nothing", it.Factory.Label())
		}
		if err := m.typeSupervisor(it.Pane.ID, it.Factory, plan.Command); err != nil {
			return "", err
		}
		m.typeAfterStart(plan, it.Pane.ID, it)
		pane = it.Pane.ID
	case WakeResume:
		reason, err := Deliver(m.Engine, m.refreshed(Target{Label: it.Factory.Label(), Pane: it.Pane, Hook: it.Hook}), plan.Command)
		if err != nil {
			return "", err
		}
		if reason != "" {
			return "", errors.New(reason)
		}
		pane = it.Pane.ID
	}
	m.markPaused(it.Factory, false)
	if m.Loader != nil {
		m.Loader.Note(it.Factory, plan.Note())
	}
	return pane, nil
}

func (m *Runtime) WakeAll() error {
	var problems []string
	for _, it := range m.snap.Items {
		if it.Status.Asleep() && !it.Factory.Archived() {
			if err := m.Wake(it.Factory.Key()); err != nil {
				problems = append(problems, it.Factory.Label()+": "+err.Error())
			}
		}
	}
	if len(problems) > 0 {
		return errors.New(strings.Join(problems, "; "))
	}
	return nil
}

func (m *Runtime) Pause(key string) error {
	it, ok := m.snap.Find(key)
	if !ok || m.Engine == nil {
		return nil
	}
	prior := m.markPaused(it.Factory, true)
	m.stopBrowser(it.Factory.Key())
	m.stopStack(it.Factory.Key())
	if err := m.Engine.Pause(it.Factory.Repo, it.Factory.Slug); err != nil {
		m.markPaused(it.Factory, prior)
		return err
	}
	if m.Loader != nil {
		m.Loader.Note(it.Factory, "paused from factory")
	}
	return nil
}

func (m *Runtime) markPaused(f factory.Factory, paused bool) bool {
	var prior bool
	if m.Loader == nil {
		return prior
	}
	m.Loader.UpdateRegistry(func(r *state.Registry) {
		if e := r.Find(f.Repo, f.Slug); e != nil {
			prior, e.Paused = e.Paused, paused
		}
	})
	return prior
}

func resolved(path string) string {
	if real, err := filepath.EvalSymlinks(path); err == nil {
		return real
	}
	return filepath.Clean(path)
}
