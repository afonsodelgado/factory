package hub

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/google/uuid"

	"github.com/afonsodelgado/factory/internal/factory"
	"github.com/afonsodelgado/factory/internal/state"
	"github.com/afonsodelgado/factory/internal/tmux"
	"github.com/afonsodelgado/factory/internal/ui/dialog"
	"github.com/afonsodelgado/factory/internal/ui/footer"
)

var popupSizes = map[string][2]int{
	"new":     {88, 24},
	"switch":  {112, 14},
	"kill":    {68, 9},
	"archive": {80, 12},
	"keys":    {116, 14},
}

var popupTitles = map[string]string{
	"new":     " + New factory ",
	"switch":  " ⌕ Factories ",
	"kill":    " ■ Kill ",
	"archive": " ▭ Archive ",
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
	if kind == "kill" || kind == "archive" {
		if it, ok := m.selectedItem(); ok {
			title = strings.TrimRight(title, " ") + " " + it.Factory.Label() + "? "
		}
	}
	tm := m.Engine.TM
	session := m.Engine.Session
	command := m.DialogCommand(kind)
	return func() tea.Msg {
		clients, err := tm.Clients(session)
		if err != nil || len(clients) == 0 {
			return dialogClosedMsg{kind, errors.New("dialogs need a terminal attached to the factory session")}
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
		m.notice = msg.err.Error()
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
	case "kill":
		var r dialog.KeyResult
		if err = json.Unmarshal(e.Data, &r); err == nil {
			err = m.kill(r.Key)
		}
	case "archive":
		var r dialog.ArchiveResult
		if err = json.Unmarshal(e.Data, &r); err == nil {
			err = m.archive(r)
		}
	}
	reply := Message{T: "done"}
	if err != nil {
		reply = Message{T: "error", Error: err.Error()}
		m.notice = err.Error()
	}
	e.From.Send(reply)
	return tea.Batch(cmd, m.load())
}

func (m *Model) newContext() dialog.NewContext {
	c := dialog.NewContext{RemoteControl: m.Config.RemoteControl()}
	selectedRepo := ""
	if it, ok := m.selectedItem(); ok {
		selectedRepo = it.Factory.Repo
	}
	for i, repo := range m.Config.Repos {
		profile, _ := state.ReadProfile(repo)
		option := dialog.RepoOption{Name: filepath.Base(repo), Path: repo, Display: tilde(repo), Browser: profile.BrowserQA(), CodeRabbit: profile.CodeRabbit == nil || *profile.CodeRabbit}
		for _, it := range m.snap.Items {
			if it.Factory.Repo != repo {
				continue
			}
			stage := ""
			if it.Factory.State != nil {
				stage = it.Factory.State.Stage
			}
			option.Existing = append(option.Existing, dialog.Existing{Key: it.Factory.Key(), Worktree: it.Factory.Slug, Label: it.Factory.Label(), Status: it.Status.String(), Stage: stage, Archived: it.Factory.Archived()})
		}
		if repo == selectedRepo {
			c.Repo = i
		}
		c.Repos = append(c.Repos, option)
	}
	return c
}

func (m *Model) switchContext() dialog.SwitchContext {
	var c dialog.SwitchContext
	for _, it := range m.snap.Items {
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

func (m *Model) killContext(it Item) dialog.KillContext {
	return dialog.KillContext{Key: it.Factory.Key(), Label: it.Factory.Label(), Title: it.Factory.Title(), FactoryDir: factoryDir(it.Factory)}
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
	if m.Loader == nil {
		return nil
	}
	err := m.Loader.UpdateRegistry(func(reg *state.Registry) {
		if entry := reg.Find(it.Factory.Repo, it.Factory.Slug); entry != nil {
			entry.Archived = false
		}
	})
	if err != nil {
		return err
	}
	m.Loader.Note(it.Factory, "unarchived")
	m.notice = it.Factory.Label() + " is back in the list."
	return nil
}

func (m *Model) create(r dialog.NewResult) error {
	if r.Repo == "" || r.Slug == "" {
		return errors.New("a factory needs a repo and a name")
	}
	if existing, ok := m.existingFactory(r.Repo, r.Ticket, r.Slug); ok {
		return m.selectExisting(existing)
	}
	if m.Engine == nil || m.Loader == nil {
		return errors.New("the hub is not attached to tmux")
	}
	profile, _ := state.ReadProfile(r.Repo)
	launch := factory.Launch{Repo: r.Repo, Slug: r.Slug, Ticket: r.Ticket, Brief: r.Brief, SessionID: uuid.NewString(), Chrome: profile.BrowserQA(), RemoteControl: m.Config.RemoteControl()}
	title := firstLine(r.Brief)
	entry := state.Entry{Repo: r.Repo, Slug: r.Slug, Ticket: r.Ticket, Title: title, SessionID: launch.SessionID, CreatedAt: m.Now(), Created: true}
	if err := m.Loader.UpdateRegistry(func(reg *state.Registry) { reg.Upsert(entry) }); err != nil {
		return err
	}
	label := r.Ticket
	if label == "" {
		label = r.Slug
	}
	pane, err := m.Engine.NewFactoryPane(m.Layout, NewPane{Repo: r.Repo, Slug: r.Slug, Title: strings.TrimSpace(label + "  " + title), Dir: r.Repo, Env: []string{"FACTORY_TUI=1"}})
	if err != nil {
		return err
	}
	if err := m.Engine.Type(pane, launch.CreateCommand()); err != nil {
		return err
	}
	note := "created by factory-tui"
	if r.Brief != "" {
		note += fmt.Sprintf(" with a %d-line brief", strings.Count(strings.TrimRight(r.Brief, "\n"), "\n")+1)
	}
	m.Loader.Note(factory.Factory{Repo: r.Repo, Slug: r.Slug}, note)
	m.selected = state.Key(r.Repo, r.Slug)
	return nil
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	if len([]rune(line)) > 80 {
		line = string([]rune(line)[:79]) + "…"
	}
	return line
}

func (m *Model) existingFactory(repo, ticket, slug string) (string, bool) {
	for _, it := range m.snap.Items {
		f := it.Factory
		if f.Repo != repo {
			continue
		}
		if (ticket != "" && factory.MatchTicket(f.Slug, ticket)) || f.Slug == slug {
			return f.Key(), true
		}
	}
	if ticket != "" {
		if wt, ok := factory.FindWorktree(repo, ticket); ok {
			return state.Key(repo, filepath.Base(wt)), true
		}
	} else if wt := filepath.Join(factory.WorktreesDir(repo), slug); slug != "" && dirExists(wt) {
		return state.Key(repo, slug), true
	}
	return "", false
}

func (m *Model) wake(key string) error {
	it, ok := m.snap.Find(key)
	if !ok || !it.Status.Asleep() || m.Engine == nil {
		return nil
	}
	profile, _ := state.ReadProfile(it.Factory.Repo)
	hooks, _ := state.ReadHookFiles(m.Config.StateDir)
	plan := PlanWake(it, profile, m.Config.RemoteControl(), hooks)
	switch plan.Kind {
	case WakeNewPane:
		dir := it.Factory.Worktree
		if !dirExists(dir) {
			return fmt.Errorf("the worktree %s is gone", tilde(dir))
		}
		pane, err := m.Engine.NewFactoryPane(m.Layout, NewPane{Repo: it.Factory.Repo, Slug: it.Factory.Slug, Title: paneTitle(it.Factory), Dir: dir, Env: []string{"FACTORY_TUI=1"}})
		if err != nil {
			return err
		}
		if err := m.Engine.Type(pane, plan.Command); err != nil {
			return err
		}
	case WakeShell:
		if now := m.freshTarget(it).Pane; now == nil || !factory.IsShell(now.Command) {
			return fmt.Errorf("%s's pane is no longer at a shell prompt, so wake typed nothing", it.Factory.Label())
		}
		if err := m.Engine.Type(it.Pane.ID, plan.Command); err != nil {
			return err
		}
	case WakeResume:
		reason, err := Deliver(m.Engine, m.freshTarget(it), plan.Command)
		if err != nil {
			return err
		}
		if reason != "" {
			return errors.New(reason)
		}
	}
	if m.Loader != nil {
		m.Loader.Note(it.Factory, plan.Note())
	}
	return nil
}

func (m *Model) wakeAll() error {
	var problems []string
	for _, it := range m.snap.Items {
		if it.Status.Asleep() && !it.Factory.Archived() {
			if err := m.wake(it.Factory.Key()); err != nil {
				problems = append(problems, it.Factory.Label()+": "+err.Error())
			}
		}
	}
	if len(problems) > 0 {
		return errors.New(strings.Join(problems, "; "))
	}
	return nil
}

func (m *Model) kill(key string) error {
	it, ok := m.snap.Find(key)
	if !ok || m.Engine == nil {
		return nil
	}
	if err := m.Engine.Kill(&m.Layout, it.Factory.Repo, it.Factory.Slug); err != nil {
		return err
	}
	if m.Loader != nil {
		m.Loader.Note(it.Factory, "killed from factory-tui")
	}
	return nil
}

func (m *Model) archive(r dialog.ArchiveResult) error {
	it, ok := m.snap.Find(r.Key)
	if !ok || m.Loader == nil {
		return nil
	}
	movedTo := ""
	if r.Remove {
		var err error
		if movedTo, err = RemoveWorktree(it.Factory.Repo, it.Factory.Worktree, it.Factory.Dir); err != nil {
			return err
		}
	}
	err := m.Loader.UpdateRegistry(func(reg *state.Registry) {
		entry := reg.Find(it.Factory.Repo, it.Factory.Slug)
		if entry == nil {
			entry = reg.Upsert(state.Entry{Repo: it.Factory.Repo, Slug: it.Factory.Slug, CreatedAt: it.Factory.CreatedAt()})
		}
		entry.Archived = true
		if movedTo != "" {
			entry.Dir = movedTo
		}
	})
	if err == nil {
		m.Loader.Note(it.Factory, "archived")
	}
	return err
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func archivable(it Item) bool {
	return it.Status == factory.Done || it.Status == factory.Gone || it.Status == factory.Exited || it.Status == factory.FailedToStart
}
