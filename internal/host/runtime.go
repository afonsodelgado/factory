package host

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"time"

	"github.com/afonsodelgado/factory/internal/factory"
	"github.com/afonsodelgado/factory/internal/review"
	"github.com/afonsodelgado/factory/internal/state"
	"github.com/afonsodelgado/factory/internal/tmux"
)

type StageEngine interface {
	NewFactoryPane(NewPane) (string, error)
	Pause(repo, slug string) error
	NewStagePane(StagePane) (string, error)
	KillStages(repo, slug, role string) error
	Type(pane, text string) error
}

type Answer struct {
	At         time.Time
	ItemSince  time.Time
	Factory    string
	ItemID     string
	Head       string
	Draft      string
	Received   time.Time
	LeavesOpen bool
	Operation  string       `json:",omitempty"`
	Device     string       `json:",omitempty"`
	Source     string       `json:",omitempty"`
	Action     string       `json:",omitempty"`
	Outcome    string       `json:",omitempty"`
	Reason     string       `json:",omitempty"`
	SettledAt  time.Time    `json:",omitzero"`
	Reply      *state.Reply `json:",omitempty"`
}

type Runtime struct {
	Pages    *review.Server
	Config   *state.Config
	Loader   *Loader
	Engine   StageEngine
	TM       tmux.Client
	Session  string
	Typist   Typist
	Now      func() time.Time
	Console  Console
	Jobs     Background
	Browsers Browsers
	Stacks   Stacks
	GitHub   *GitHub
	Machine  string

	welcomed    bool
	snap        Snapshot
	typeQueue   []QueuedType
	typedAt     map[string]time.Time
	inboxQueued map[string]bool
	notice      string
	receipts    receiptFile
	remotes     map[string]repoRemote
	leases      map[string]browserLease
	preparing   map[string]string
	prs         map[string][]PR
	prsPolled   time.Time
	holdings    map[string]Holding
	readings    map[string]reading
	promptsSeen map[string]time.Time
}

const (
	quietHooks   = 30 * time.Second
	captureEvery = 5 * time.Second
)

type reading struct {
	at     time.Time
	since  time.Time
	token  string
	screen string
	menu   *Menu
}

type watchedPane struct {
	role string
	pane *tmux.Pane
	hook *state.HookFile
}

func New(r Runtime) *Runtime {
	if r.Now == nil {
		r.Now = time.Now
	}
	r.typedAt = map[string]time.Time{}
	r.inboxQueued = map[string]bool{}
	r.receipts = receiptFile{Claims: map[string]Answer{}}
	r.remotes = map[string]repoRemote{}
	r.leases = map[string]browserLease{}
	r.preparing = map[string]string{}
	r.holdings = map[string]Holding{}
	r.readings = map[string]reading{}
	return &r
}

func (m *Runtime) SetSnapshot(s Snapshot) { m.snap = s }

func (m *Runtime) Snapshot() Snapshot { return m.snap }

func (m *Runtime) TakeNotice() string {
	notice := m.notice
	m.notice = ""
	return notice
}

func (m *Runtime) takeFirstStart() string {
	if m.welcomed || m.Machine == "" {
		return ""
	}
	m.welcomed = true
	marker := filepath.Join(m.Config.StateDir, "node-welcomed")
	if _, err := os.Lstat(marker); err == nil {
		return ""
	}
	if err := os.WriteFile(marker, nil, 0o600); err != nil {
		return ""
	}
	return m.Machine
}

func (m *Runtime) Tick() {
	if m.Loader != nil {
		m.snap = m.Loader.Load()
	}
	m.ReadScreens()
	m.SettleReceipts(m.snap.Items, func(string) time.Time { return m.snap.At })
	m.QueueInbox()
	m.FlushTypeQueue()
	m.CloseFinishedStages()
	m.ReleaseFinishedBrowsers()
	m.ReleaseFinishedStacks()
	m.SyncReview()
}

func (m *Runtime) ReadScreens() {
	now, previous := m.Now(), m.readings
	m.readings = map[string]reading{}
	shown := map[string]time.Time{}
	for i := range m.snap.Items {
		it := &m.snap.Items[i]
		it.Screen = m.screenOf(*it, now, previous)
		if it.Screen != nil && it.Screen.Menu != nil {
			it.Status, it.Unblocked = factory.NeedsYou, factory.NeedsYou
			shown[it.Screen.Menu.Fingerprint] = it.Screen.Since
		}
	}
	m.rememberPrompts(shown)
}

func (m *Runtime) promptsPath() string { return filepath.Join(m.Config.StateDir, "prompts.json") }

func (m *Runtime) seenPrompts() map[string]time.Time {
	if m.promptsSeen == nil {
		m.promptsSeen = map[string]time.Time{}
		if data, err := os.ReadFile(m.promptsPath()); err == nil {
			json.Unmarshal(data, &m.promptsSeen)
		}
	}
	return m.promptsSeen
}

func (m *Runtime) rememberPrompts(shown map[string]time.Time) {
	if maps.Equal(shown, m.seenPrompts()) {
		return
	}
	m.promptsSeen = shown
	if data, err := json.Marshal(shown); err == nil {
		state.WriteFileAtomic(m.promptsPath(), data)
	}
}

func (m *Runtime) screenOf(it Item, now time.Time, previous map[string]reading) *Screen {
	nothingToRead := it.Status == factory.Done || it.Status == factory.Gone || it.Status == factory.Exited
	if nothingToRead || it.Factory.Archived() {
		return nil
	}
	for _, w := range watchedPanes(it) {
		if !w.quiet(now) {
			continue
		}
		if r, ok := m.read(w.pane, now, previous); ok && r.menu != nil {
			return &Screen{Node: m.Machine, Role: w.role, Pane: w.pane, Lines: ScreenTail(r.screen), Menu: r.menu, Since: r.since}
		}
	}
	if !it.Unreported {
		return nil
	}
	screen := &Screen{Node: m.Machine, Pane: it.Pane}
	if r, ok := m.read(it.Pane, now, previous); ok {
		screen.Lines = ScreenTail(r.screen)
	}
	return screen
}

func watchedPanes(it Item) []watchedPane {
	supervisor := watchedPane{pane: it.Pane, hook: it.Hook}
	if it.Stage == nil {
		return []watchedPane{supervisor}
	}
	return []watchedPane{{role: it.Stage.Run.Role, pane: it.Stage.Pane, hook: it.Stage.Hook}, supervisor}
}

func (w watchedPane) quiet(now time.Time) bool {
	live := w.pane != nil && !w.pane.Dead && !w.pane.InMode
	hooksQuiet := w.hook == nil || now.Sub(w.hook.Last.At) >= quietHooks
	return live && hooksQuiet && !factory.PermissionPrompt(w.hook)
}

func (m *Runtime) read(p *tmux.Pane, now time.Time, previous map[string]reading) (reading, bool) {
	if p == nil {
		return reading{}, false
	}
	last, seen := m.readings[p.ID]
	if !seen {
		last, seen = previous[p.ID]
	}
	if seen && last.token == p.Token && now.Sub(last.at) < captureEvery {
		m.readings[p.ID] = last
		return last, true
	}
	screen, err := m.console().Capture(p.ID)
	if err != nil {
		return reading{}, false
	}
	r := reading{at: now, since: now, token: p.Token, screen: screen}
	if menu, ok := ParseMenu(screen); ok {
		menu.Fingerprint = promptFingerprint(p, menu)
		r.menu = &menu
		if seen && last.menu != nil && last.menu.Fingerprint == menu.Fingerprint {
			r.since = last.since
		} else if before, ok := m.seenPrompts()[menu.Fingerprint]; ok {
			r.since = before
		}
	}
	m.readings[p.ID] = r
	return r, true
}

func (m *Runtime) repoConfig(repo string) (*state.Config, state.Profile) {
	profile, _ := state.ReadProfile(repo)
	return m.Config.WithProfile(profile), profile
}

func (m *Runtime) currentPanes() []tmux.Pane {
	panes, _ := m.TM.Panes()
	return panes
}

func (m *Runtime) refreshed(t Target) Target {
	if m.Engine != nil && t.Pane != nil {
		if panes, err := m.TM.Panes(); err == nil {
			id := t.Pane.ID
			t.Pane = nil
			if p, ok := byID(panes, id); ok {
				t.Pane = &p
			}
		}
	}
	if t.Hook != nil && m.Config.StateDir != "" {
		if hook, err := state.ReadHookFile(filepath.Join(m.Config.SessionsDir(), t.Hook.SessionID+".json")); err == nil {
			t.Hook = hook
		}
	}
	return t
}

func byID(panes []tmux.Pane, id string) (tmux.Pane, bool) {
	for _, p := range panes {
		if p.ID == id {
			return p, true
		}
	}
	return tmux.Pane{}, false
}

func FactoryPane(panes []tmux.Pane, repo, slug string) (tmux.Pane, bool) {
	for _, p := range panes {
		if p.Repo == repo && p.Slug == slug {
			return p, true
		}
	}
	return tmux.Pane{}, false
}

func LivePane(panes []tmux.Pane, f factory.Factory) factory.Pane {
	p, ok := FactoryPane(panes, f.Repo, f.Slug)
	if !ok || p.Dead {
		return factory.Pane{}
	}
	return factory.Pane{Found: true, Shell: factory.IsShell(p.Command)}
}

func stagePaneOf(panes []tmux.Pane, f factory.Factory, role, token string) (tmux.Pane, bool) {
	for _, p := range panes {
		if p.Repo == f.Repo && p.StageOf == f.Slug && p.Stage == role && p.Token == token {
			return p, true
		}
	}
	return tmux.Pane{}, false
}

func factoryBin() string {
	self, err := os.Executable()
	if err != nil {
		return "FACTORY_BIN=factory"
	}
	return "FACTORY_BIN=" + self
}

func (m *Runtime) ClearTyped(pane string) { delete(m.typedAt, pane) }

func (m *Runtime) selectExisting(key string) (string, error) {
	if err := m.movedAway(key); err != nil {
		return "", err
	}
	if it, ok := m.snap.Find(key); ok && it.Factory.Archived() {
		return "", fmt.Errorf("%s is archived. In the factory list, H shows archived factories and a on the row unarchives it.", it.Factory.Label())
	}
	return key, nil
}

func supervisorEnv(supervisor state.AgentSetting) []string {
	env := []string{"FACTORY=1", "FACTORY_SUPERVISOR=" + supervisor.ToolName(), factoryBin()}
	if !supervisor.OnClaude() {
		env = append(env, "CLAUDE_CODE_SESSION_ID=")
	}
	return env
}

func (m *Runtime) typeSupervisor(pane string, f factory.Factory, command string) error {
	if f.Supervisor().OnClaude() {
		return m.Engine.Type(pane, command)
	}
	return m.TypeLaunch(pane, state.LaunchScriptPath(m.Config.StateDir, f.Repo, f.Slug, state.SupervisorLaunch), command)
}

func (m *Runtime) typeAfterStart(plan Wake, pane string, it Item) {
	if plan.ThenType != "" {
		m.QueueType(QueuedType{Pane: pane, SessionID: plan.Launch.SessionID, Label: it.Factory.Label(), Text: plan.ThenType, NotBefore: m.Now().Add(resumeSettle)})
	}
}

func (m *Runtime) note(f factory.Factory, text string) {
	if m.Loader != nil {
		m.Loader.Note(f, text)
	}
}

func (m *Runtime) ReloadShared() error {
	fresh, err := state.LoadConfig()
	if err != nil {
		return err
	}
	m.Config.TakeShared(fresh)
	return nil
}
