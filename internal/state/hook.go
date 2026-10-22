package state

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

type HookFile struct {
	SessionID      string     `json:"session_id"`
	CWD            string     `json:"cwd"`
	TranscriptPath string     `json:"transcript_path"`
	Main           *HookEvent `json:"main,omitempty"`
	Last           HookEvent  `json:"last"`
}

type HookEvent struct {
	Event     string    `json:"event"`
	Matcher   string    `json:"matcher,omitempty"`
	At        time.Time `json:"at"`
	AgentID   string    `json:"agent_id,omitempty"`
	AgentType string    `json:"agent_type,omitempty"`
}

type hookPayload struct {
	SessionID        string `json:"session_id"`
	CWD              string `json:"cwd"`
	TranscriptPath   string `json:"transcript_path"`
	HookEventName    string `json:"hook_event_name"`
	AgentID          string `json:"agent_id"`
	AgentType        string `json:"agent_type"`
	Source           string `json:"source"`
	NotificationType string `json:"notification_type"`
	ToolName         string `json:"tool_name"`
	Reason           string `json:"reason"`
}

func (p hookPayload) matcher() string {
	for _, m := range []string{p.Source, p.NotificationType, p.ToolName, p.Reason} {
		if m != "" {
			return m
		}
	}
	return ""
}

func IsFactoryDir(cwd string) bool {
	if strings.Contains(cwd, string(filepath.Separator)+filepath.Join(".claude", "worktrees")+string(filepath.Separator)) {
		return true
	}
	info, err := os.Stat(filepath.Join(cwd, ".claude", "factory"))
	return err == nil && info.IsDir()
}

func RecordHook(stateDir string, payload []byte, now time.Time) error {
	var p hookPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return err
	}
	if p.SessionID == "" || p.HookEventName == "" || !IsFactoryDir(p.CWD) {
		return nil
	}
	dir := SessionsDir(stateDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	unlock, err := lockFile(filepath.Join(dir, p.SessionID+".lock"))
	if err != nil {
		return err
	}
	defer unlock()

	path := filepath.Join(dir, p.SessionID+".json")
	file, err := ReadHookFile(path)
	if err != nil {
		file = &HookFile{}
	}
	event := HookEvent{Event: p.HookEventName, Matcher: p.matcher(), At: now, AgentID: p.AgentID, AgentType: p.AgentType}
	file.SessionID = p.SessionID
	file.CWD = p.CWD
	file.TranscriptPath = p.TranscriptPath
	if !now.Before(file.Last.At) {
		file.Last = event
	}
	mainThread := p.AgentID == ""
	if mainThread && (file.Main == nil || !now.Before(file.Main.At)) {
		file.Main = &event
	}
	data, err := json.Marshal(file)
	if err != nil {
		return err
	}
	return WriteFileAtomic(path, data)
}

func lockFile(path string) (func(), error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, err
	}
	return func() {
		syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}, nil
}

func ReadHookFile(path string) (*HookFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var file HookFile
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, err
	}
	return &file, nil
}

func ReadHookFiles(stateDir string) ([]*HookFile, error) {
	paths, err := filepath.Glob(filepath.Join(SessionsDir(stateDir), "*.json"))
	if err != nil {
		return nil, err
	}
	var files []*HookFile
	for _, path := range paths {
		file, err := ReadHookFile(path)
		if err != nil {
			continue
		}
		files = append(files, file)
	}
	return files, nil
}

func (f *HookFile) MainEvent() HookEvent {
	if f == nil || f.Main == nil {
		return HookEvent{}
	}
	return *f.Main
}

func (e HookEvent) Is(event string, matchers ...string) bool {
	if e.Event != event {
		return false
	}
	if len(matchers) == 0 {
		return true
	}
	for _, m := range matchers {
		if e.Matcher == m {
			return true
		}
	}
	return false
}
