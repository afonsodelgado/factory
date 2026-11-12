package hub

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/afonsodelgado/factory/internal/factory"
	"github.com/afonsodelgado/factory/internal/host"
	"github.com/afonsodelgado/factory/internal/state"
	"github.com/afonsodelgado/factory/internal/tmux"
	"github.com/afonsodelgado/factory/internal/ui/dialog"
	"github.com/afonsodelgado/factory/internal/ui/footer"
	"github.com/afonsodelgado/factory/internal/ui/panel"
	"github.com/afonsodelgado/factory/internal/ui/selector"
)

var popupSizes = map[string][2]int{
	"new":     {88, 30},
	"switch":  {112, 14},
	"pause":   {68, 9},
	"archive": {80, 12},
	"delete":  {84, 14},
	"keys":    {124, 17},
}

var popupTitles = map[string]string{
	"new":     " + New factory ",
	"switch":  " ⌕ Factories ",
	"pause":   " ‖ Pause ",
	"archive": " ▭ Archive ",
	"delete":  " ✕ Delete ",
	"keys":    " Keys ",
}

type dialogClosedMsg struct {
	kind string
	err  error
}

func (m *Model) openDialog(kind string, context any) tea.Cmd {
	if m.Engine == nil || m.DialogCommand == nil {
		return nil
	}
	data, err := json.Marshal(context)
	if err != nil {
		return nil
	}
	m.dialogs[kind] = data
	m.focus = footer.Dialog
	m.dialog = kind
	size := popupSizes[kind]
	width, height := min(size[0], max(m.window[0]-4, 20)), min(size[1], max(m.window[1]-4, 6))
	title := popupTitles[kind]
	if kind == "pause" || kind == "archive" || kind == "delete" {
		if it, ok := m.selectedItem(); ok {
			title = strings.TrimRight(title, " ") + " " + it.Factory.Label() + "? "
		}
	}
	tm := m.Engine.TM
	window := m.Layout.Window
	command := m.DialogCommand(kind)
	return func() tea.Msg {
		clients, err := tm.Clients(window)
		if err != nil || len(clients) == 0 {
			return dialogClosedMsg{kind, errors.New("dialogs need a terminal attached to the hub's session")}
		}
		err = tm.Popup(tmux.Popup{Client: clients[0], Title: title, Width: fmt.Sprint(width), Height: fmt.Sprint(height), Command: command})
		return dialogClosedMsg{kind, err}
	}
}

func (m *Model) dialogClosed(msg dialogClosedMsg) {
	if m.dialog == msg.kind {
		m.dialog = ""
		m.focus = footer.Factories
	}
	if msg.err != nil {
		m.notice = problem(msg.err.Error())
	}
}

func (m *Model) dialogHello(e Event) {
	kind := strings.TrimPrefix(e.Pane, "dialog:")
	context, ok := m.dialogs[kind]
	if !ok {
		e.From.Send(Message{T: "error", Error: "no " + kind + " dialog is open"})
		return
	}
	e.From.Send(Message{T: "context", Data: context})
}

func (m *Model) dialogResult(e Event) tea.Cmd {
	kind := strings.TrimPrefix(e.Pane, "dialog:")
	delete(m.dialogs, kind)
	var err error
	var cmd tea.Cmd
	switch kind {
	case "new":
		var r dialog.NewResult
		if err = json.Unmarshal(e.Data, &r); err == nil {
			cmd, err = m.newResult(r)
		}
	case "switch":
		var r dialog.KeyResult
		if err = json.Unmarshal(e.Data, &r); err == nil {
			m.choose(r.Key)
		}
	case "pause":
		var r dialog.KeyResult
		if err = json.Unmarshal(e.Data, &r); err == nil {
			err = m.pause(r.Key)
		}
	case "archive":
		var r dialog.ArchiveResult
		if err = json.Unmarshal(e.Data, &r); err == nil {
			err = m.archive(r)
		}
	case "delete":
		var r dialog.DeleteResult
		if err = json.Unmarshal(e.Data, &r); err == nil && r.Confirmed {
			_, err = m.delete(r.Key)
		}
	}
	reply := Message{T: "done"}
	if err != nil {
		reply = Message{T: "error", Error: err.Error()}
		m.notice = problem(err.Error())
	}
	e.From.Send(reply)
	return tea.Batch(cmd, m.load())
}

func (m *Model) newContext() dialog.NewContext {
	c := dialog.NewContext{Tools: m.toolOptions()}
	selectedRepo := m.here
	if it, ok := m.selectedItem(); ok && !slices.Contains(m.Config.Repos, m.here) {
		selectedRepo = it.Factory.Repo
	}
	for i, repo := range m.Config.Repos {
		cfg, profile := m.repoConfig(repo)
		option := dialog.RepoOption{Name: filepath.Base(repo), Path: repo, Display: tilde(repo), Browser: profile.BrowserQA(), CodeRabbit: profile.CodeRabbit == nil || *profile.CodeRabbit, RemoteControl: cfg.RemoteControl(), Supervisor: cfg.Supervisor}
		if pipelines, err := m.Config.PipelinesFor(repo); err != nil {
			option.PipelineProblem = err.Error()
		} else {
			option.Pipelines, option.DefaultPipeline = pipelines.List, pipelines.Default
		}
		if agents, err := m.Config.AgentsFor(repo); err != nil {
			option.PipelineProblem = err.Error()
		} else {
			for _, role := range state.AgentRoles {
				option.Agents = append(option.Agents, dialog.AgentOption{Role: role, AgentSetting: agents[role]})
			}
		}
		for _, it := range m.snap.Items {
			if it.Factory.Repo != repo {
				continue
			}
			stage := ""
			if it.Factory.State != nil {
				stage = it.Factory.State.Stage
			}
			option.Existing = append(option.Existing, dialog.Existing{Key: it.Factory.Key(), Worktree: it.Factory.Slug, Label: it.Factory.Label(), Status: it.Status.String(), Stage: stage, Archived: it.Factory.Archived(), Retired: retired(it.Factory)})
		}
		if repo == selectedRepo {
			c.Repo, c.Here = i, repo == m.here
		}
		c.Repos = append(c.Repos, option)
	}
	return c
}

func (m *Model) switchContext() dialog.SwitchContext {
	var c dialog.SwitchContext
	items := slices.Clone(m.snap.Items)
	sort.SliceStable(items, func(i, j int) bool { return urgency(items[i].Status) < urgency(items[j].Status) })
	for _, it := range items {
		if it.Factory.Archived() {
			continue
		}
		stage := stageOf(it.Factory)
		if stage == "" {
			stage = "no state yet"
		}
		c.Rows = append(c.Rows, dialog.SwitchRow{Key: it.Factory.Key(), Label: it.Factory.Label(), Title: it.Factory.Title(), Repo: it.Factory.RepoName(), Stage: stage, Status: it.Status.String(), Needs: it.Status == factory.NeedsYou})
	}
	return c
}

func urgency(s factory.Status) int {
	switch s {
	case factory.NeedsYou:
		return 0
	case factory.Running, factory.Starting:
		return 1
	case factory.WaitingAgent, factory.WaitingCI:
		return 2
	case factory.Done:
		return 4
	}
	return 3
}

func factoryDir(f factory.Factory) string {
	if f.Dir == "" {
		return ".claude/factory/" + f.Label()
	}
	linkedOutOfWorktree := f.Worktree != "" && within(f.Dir, f.Worktree) && !within(resolved(f.Dir), resolved(f.Worktree))
	if linkedOutOfWorktree {
		return tilde(resolved(f.Dir))
	}
	return tilde(f.Dir)
}

func (m *Model) pauseContext(it Item) dialog.PauseContext {
	return dialog.PauseContext{Key: it.Factory.Key(), Label: it.Factory.Label(), Title: it.Factory.Title(), FactoryDir: factoryDir(it.Factory)}
}

func (m *Model) deleteContext(it Item) dialog.DeleteContext {
	removal := factory.PlanRemoval(it.Factory, m.Config)
	work := factory.CheckWork(removal)
	return dialog.DeleteContext{
		Key:          it.Factory.Key(),
		Label:        it.Factory.Label(),
		Title:        it.Factory.Title(),
		Worktree:     tilde(removal.Worktree),
		WorktreeGone: !dirExists(removal.Worktree),
		Branches:     removal.Branches,
		Dirty:        work.Dirty,
		Unpushed:     work.Unpushed,
	}
}

func (m *Model) archiveContext(it Item) dialog.ArchiveContext {
	c := dialog.ArchiveContext{Key: it.Factory.Key(), Label: it.Factory.Label(), Worktree: tilde(it.Factory.Worktree), FactoryDir: factoryDir(it.Factory)}
	if it.Factory.Worktree == "" {
		c.Problem = "this factory has no worktree on record"
		return c
	}
	check := CheckRemoval(it.Factory.Worktree)
	c.Branch, c.Dirty, c.Unpushed, c.NoUpstream, c.Problem = check.Branch, check.Dirty, check.Unpushed, check.NoUpstream, check.Problem
	if target, inside := FactoryDirRescue(it.Factory.Repo, it.Factory.Worktree, it.Factory.Dir); inside {
		c.FactoryDirMovesTo = tilde(target)
		if _, err := os.Lstat(target); err == nil && c.Problem == "" {
			c.Problem = tilde(target) + " already exists, so the factory directory has nowhere to go"
		}
	}
	c.Removable = check.Removable() && c.Problem == ""
	return c
}

func (m *Model) newResult(r dialog.NewResult) (tea.Cmd, error) {
	if r.Select != "" {
		if err := m.selectExisting(r.Select); err != nil {
			return nil, err
		}
		if r.Wake {
			return nil, m.wake(r.Select)
		}
		return nil, nil
	}
	return nil, m.create(r)
}

func (m *Model) selectExisting(key string) error {
	if it, ok := m.snap.Find(key); ok && it.Factory.Archived() && !m.showArchived {
		return fmt.Errorf("%s is archived. %s", it.Factory.Label(), dialog.UnarchiveAdvice)
	}
	m.choose(key)
	return nil
}

func (m *Model) unarchive(it Item) error {
	if err := m.execution().Unarchive(it); err != nil {
		return err
	}
	m.notice = news(it.Factory.Label() + " is back in the list.")
	return nil
}

func (m *Model) create(r dialog.NewResult) error {
	key, err := m.execution().Create(host.CreateRequest{Repo: r.Repo, Slug: r.Slug, Prompt: r.Prompt, Pipeline: r.Pipeline, Stages: r.Stages, Agents: r.Agents, Supervisor: r.Supervisor})
	if key != "" {
		m.selected = key
	}
	return err
}

func retired(f factory.Factory) bool { return host.Retired(f) }

func (m *Model) wake(key string) error { return m.execution().Wake(key) }

func (m *Model) wakeSelected() {
	if it, ok := m.selectedItem(); ok && it.Status == factory.Done {
		m.notice = problem(it.Factory.Label() + " is done, so there is nothing to wake.")
		return
	}
	m.report(m.wake(m.selected))
}

func (m *Model) wakeAll() error { return m.execution().WakeAll() }

func (m *Model) pause(key string) error { return m.execution().Pause(key) }

func (m *Model) delete(key string) ([]factory.RemovalStep, error) {
	it, ok := m.snap.Find(key)
	if !ok || m.Loader == nil {
		return nil, fmt.Errorf("the hub does not list %s", key)
	}
	return m.deleteFactory(it.Factory)
}

func (m *Model) deleteFactory(f factory.Factory) ([]factory.RemovalStep, error) {
	steps, err := m.execution().DeleteFactory(f)
	if m.Chrome != nil {
		m.Chrome.CloseFactory(reviewKey(f))
	}
	if m.selected == f.Key() {
		m.choose(m.rowAfter(f.Key()))
	}
	m.forgetFactory(f.Key())
	if err == nil {
		m.notice = news(f.Label() + " is deleted.")
	}
	return steps, err
}

func (m *Model) rowAfter(key string) string {
	rows := selector.Visible(m.data())
	i := slices.IndexFunc(rows, func(r selector.Row) bool { return r.Key == key })
	switch {
	case i < 0:
		return ""
	case i+1 < len(rows):
		return rows[i+1].Key
	case i > 0:
		return rows[i-1].Key
	}
	return ""
}

func (m *Model) forgetFactory(key string) {
	ownDraft := func(draftKey string) bool { return strings.HasPrefix(draftKey, key+"|") }
	maps.DeleteFunc(m.panel.drafts, func(k string, _ *panel.Draft) bool { return ownDraft(k) })
	maps.DeleteFunc(m.panel.sent, func(k string, _ sentMark) bool { return ownDraft(k) })
	maps.DeleteFunc(m.panel.refused, func(k string, _ string) bool { return ownDraft(k) })
	delete(m.panel.prs, key)
}

func (m *Model) archive(r dialog.ArchiveResult) error {
	return m.execution().Archive(host.ArchiveRequest{Key: r.Key, Remove: r.Remove})
}

func dirExists(path string) bool { return host.DirExists(path) }

func archivable(it Item) bool {
	return it.Status == factory.Done || it.Status == factory.Gone || it.Status == factory.Exited || it.Status == factory.FailedToStart
}
