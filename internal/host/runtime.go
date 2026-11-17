package host

import (
	"fmt"
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
	Operation  string    `json:",omitempty"`
	Device     string    `json:",omitempty"`
	Source     string    `json:",omitempty"`
	Action     string    `json:",omitempty"`
	Outcome    string    `json:",omitempty"`
	Reason     string    `json:",omitempty"`
	SettledAt  time.Time `json:",omitzero"`
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
	Log      *EventLog
	Push     *Pusher
	Jobs     Background
	Browsers Browsers
	GitHub   *GitHub
	Machine  string

	welcomed    bool
	snap        Snapshot
	typeQueue   []QueuedType
	typedAt     map[string]time.Time
	inboxQueued map[string]bool
	notice      string
	receipts    receiptFile
	repoNames   map[string]string
	leases      map[string]browserLease
	preparing   map[string]string
	prs         map[string][]PR
	prsPolled   time.Time
}

func New(r Runtime) *Runtime {
	if r.Now == nil {
		r.Now = time.Now
	}
	r.typedAt = map[string]time.Time{}
	r.inboxQueued = map[string]bool{}
	r.receipts = receiptFile{Claims: map[string]Answer{}}
	r.repoNames = map[string]string{}
	r.leases = map[string]browserLease{}
	r.preparing = map[string]string{}
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
	m.SettleReceipts(m.snap.Items, func(string) time.Time { return m.snap.At })
	m.QueueInbox()
	m.FlushTypeQueue()
	m.CloseFinishedStages()
	m.ReleaseFinishedBrowsers()
	m.SyncReview()
	m.watch()
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
