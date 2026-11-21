package host

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"time"

	"github.com/afonsodelgado/factory/internal/factory"
	"github.com/afonsodelgado/factory/internal/state"
)

type CheckpointRequest struct {
	Key       string `json:"key"`
	Operation string `json:"operation"`
}

type Movable struct {
	Key         string     `json:"key"`
	Incarnation string     `json:"incarnation"`
	ID          string     `json:"id"`
	Slug        string     `json:"slug"`
	Repo        string     `json:"repo"`
	Worktree    string     `json:"worktree"`
	Dir         string     `json:"dir"`
	Evidence    string     `json:"evidence"`
	Branch      string     `json:"branch"`
	PRs         []state.PR `json:"prs"`
	Waiting     string     `json:"waiting,omitempty"`
}

func (m *Runtime) Movable(key string) (Movable, Item, error) {
	it, ok := m.snap.Find(key)
	f := it.Factory
	switch {
	case !ok:
		return Movable{}, it, fmt.Errorf("no factory %s", key)
	case f.Remote != nil:
		return Movable{}, it, fmt.Errorf("%s runs on %s, not here", f.Label(), f.Where())
	case f.State == nil || f.Dir == "" || f.Worktree == "":
		return Movable{}, it, fmt.Errorf("%s has no state.json in a worktree yet, so it has nothing to checkpoint", f.Label())
	case unpublished(f.State):
		return Movable{}, it, errors.New("cannot move unpublished implementation branches; publish through the PR stage first")
	case f.Archived():
		return Movable{}, it, fmt.Errorf("%s is archived", f.Label())
	}
	return Movable{
		Key: f.Key(), Incarnation: Incarnation(f), ID: f.Label(), Slug: f.Slug, Repo: f.Repo, Worktree: f.Worktree, Dir: f.Dir,
		Evidence: QADir(f), Branch: f.State.Branch, PRs: f.State.PRs,
	}, it, nil
}

func (m *Runtime) Checkpoint(r CheckpointRequest) (Movable, error) {
	movable, it, err := m.Movable(r.Key)
	if err != nil {
		return Movable{}, err
	}
	written := it.Factory.State.Checkpoint
	switch {
	case written == nil || written.Operation != r.Operation:
		movable.Waiting, err = m.requestCheckpoint(it, r.Operation)
		return movable, err
	case written.Status == state.CheckpointRefused:
		return Movable{}, fmt.Errorf("the supervisor of %s cannot checkpoint: %s", it.Factory.Label(), written.Reason)
	case written.Status != state.CheckpointReady:
		return Movable{}, fmt.Errorf("the supervisor of %s wrote checkpoint status %q, which is neither ready nor refused", it.Factory.Label(), written.Status)
	}
	return movable, m.quiesce(it)
}

func (m *Runtime) requestCheckpoint(it Item, operation string) (string, error) {
	if it.Pane == nil || it.Pane.Dead {
		return "", errors.New(it.Factory.Label() + " has no running supervisor to write its checkpoint; wake it first")
	}
	asked := "checkpoint " + operation
	if !m.inboxQueued[asked] {
		m.inboxQueued[asked] = true
		text := factory.ForSupervisor(it.Factory.Supervisor().ToolName(), "/factory checkpoint "+operation)
		m.QueueType(QueuedType{Pane: it.Pane.ID, Token: it.Pane.Token, Label: it.Factory.Label(), Text: text})
	}
	return "waiting for the supervisor of " + it.Factory.Label() + " to write checkpoint " + operation, nil
}

func (m *Runtime) undelivered(it Item) []string {
	var blockers []string
	for _, q := range m.typeQueue {
		if pane, found := byID(m.snap.Panes, q.Pane); found && OfFactory(pane, it.Factory.Repo, it.Factory.Slug) {
			blockers = append(blockers, "a message to "+q.Label+" is still waiting to be typed")
		}
	}
	record := DeliveredPath(m.Config.StateDir, it.Factory.Repo, it.Factory.Slug)
	if pending := len(InboxMessages(m.Config.StateDir, it, ReadDelivered(record), m.Now())); pending > 0 {
		blockers = append(blockers, fmt.Sprintf("%d inbox messages have not reached the supervisor yet", pending))
	}
	return blockers
}

type InboxMessage struct {
	Key  string
	Text string
}

func (m *Runtime) QueueInbox() {
	if m.Config.StateDir == "" {
		return
	}
	queued := len(m.typeQueue)
	for _, it := range m.snap.Items {
		if it.Pane == nil || it.Factory.Dir == "" || m.unverified(it.Factory.Key()) != nil {
			continue
		}
		record := DeliveredPath(m.Config.StateDir, it.Factory.Repo, it.Factory.Slug)
		checked := it
		browserStarting := it.Stage != nil && m.preparing[it.Factory.Key()] == it.Stage.Run.Token
		if browserStarting {
			checked.Stage = nil
		}
		for _, msg := range InboxMessages(m.Config.StateDir, checked, ReadDelivered(record), m.Now()) {
			if m.inboxQueued[msg.Key] {
				continue
			}
			m.inboxQueued[msg.Key] = true
			m.typeQueue = append(m.typeQueue, QueuedType{Pane: it.Pane.ID, Token: it.Pane.Token, Label: it.Factory.Label(), Text: msg.Text, Delivers: msg.Key, Record: record})
		}
	}
	if len(m.typeQueue) > queued {
		m.saveQueue(m.typeQueue)
	}
}

const lostSuffix = ".lost"

func InboxMessages(stateDir string, it Item, delivered []string, now time.Time) []InboxMessage {
	var messages []InboxMessage
	runs, _ := state.LoadStageRuns(state.StageRunsPath(stateDir, it.Factory.Repo, it.Factory.Slug))
	for _, run := range runs.Runs {
		if run.Finished() && !slices.Contains(delivered, run.Result) {
			messages = append(messages, InboxMessage{Key: run.Result, Text: "stage " + run.Role + " done: " + run.Result})
		}
	}
	if lost := it.Discovery; lost.Lost(now) && !slices.Contains(delivered, lost.Run.Result+lostSuffix) {
		messages = append(messages, InboxMessage{Key: lost.Run.Result + lostSuffix, Text: "stage discoverer lost: it stopped or its pane closed without writing " + lost.Run.Result + "; run factory stage discover again"})
	}
	if lost := it.Stage; awaited(it, lost) && lost.Lost(now) && !slices.Contains(delivered, lostKey(lost.Run)) {
		messages = append(messages, InboxMessage{Key: lostKey(lost.Run), Text: "stage " + lost.Run.Role + " lost: it stopped or its pane closed without writing " + lost.Run.Result + "; reconcile it as a lost run"})
	}
	files, _ := filepath.Glob(filepath.Join(it.Factory.Dir, "inbox", "*.json"))
	sort.Strings(files)
	for _, file := range files {
		var note struct {
			Text string `json:"text"`
		}
		data, err := os.ReadFile(file)
		if err != nil || json.Unmarshal(data, &note) != nil || note.Text == "" || slices.Contains(delivered, file) {
			continue
		}
		messages = append(messages, InboxMessage{Key: file, Text: note.Text})
	}
	return messages
}

func lostKey(run state.StageRun) string {
	return run.Token + "/" + filepath.Base(run.Result) + lostSuffix
}

func awaited(it Item, stage *StageView) bool {
	return stage != nil && it.Factory.State != nil && it.Factory.State.Workflow.ActiveRun == stage.Run.ID
}

func DeliveredPath(stateDir, repo, slug string) string {
	return filepath.Join(stateDir, "delivered", state.FileStem(repo, slug)+".json")
}

func ReadDelivered(path string) []string {
	var keys []string
	if data, err := os.ReadFile(path); err == nil {
		json.Unmarshal(data, &keys)
	}
	return keys
}

func MarkDelivered(path, key string) {
	keys := append(ReadDelivered(path), key)
	data, err := json.Marshal(keys)
	if err != nil || os.MkdirAll(filepath.Dir(path), 0o755) != nil {
		return
	}
	state.WriteFileAtomic(path, data)
}

func unpublished(s *state.State) bool {
	for _, branch := range s.Stack {
		found := false
		for _, pr := range s.PRs {
			if pr.Branch == branch.Branch && pr.Number > 0 {
				found = true
			}
		}
		if !found {
			return true
		}
	}
	return false
}
