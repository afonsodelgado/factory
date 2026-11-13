package host

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/afonsodelgado/factory/internal/factory"
	"github.com/afonsodelgado/factory/internal/review"
	"github.com/afonsodelgado/factory/internal/state"
)

const reviewWait = 10 * time.Second

type ReviewRequest struct {
	Action  string `json:"action"`
	URL     string `json:"url,omitempty"`
	ID      string `json:"id,omitempty"`
	Token   string `json:"token,omitempty"`
	Cwd     string `json:"cwd,omitempty"`
	Factory string `json:"factory,omitempty"`
	Page    string `json:"page,omitempty"`
	Thread  string `json:"thread,omitempty"`
	Text    string `json:"text,omitempty"`
	Resolve bool   `json:"resolve,omitempty"`
}

type ReviewReply struct {
	Target  string          `json:"target,omitempty"`
	Batch   string          `json:"batch,omitempty"`
	Count   int             `json:"count,omitempty"`
	Gate    string          `json:"gate,omitempty"`
	Owner   string          `json:"owner,omitempty"`
	Threads []review.Thread `json:"threads,omitempty"`
}

func ReviewActions(events chan<- Event) func(review.Action) (any, error) {
	return func(a review.Action) (any, error) {
		data, err := json.Marshal(ReviewRequest{Action: a.Kind, Factory: a.Factory, Page: a.Page})
		if err != nil {
			return nil, err
		}
		reply := make(chan Message, 1)
		timeout := time.After(reviewWait)
		select {
		case events <- Event{Message: Message{T: "review", Data: data}, Reply: reply}:
		case <-timeout:
			return nil, errors.New("the hub is busy; try again")
		}
		select {
		case m := <-reply:
			if m.T == "error" {
				return nil, errors.New(m.Error)
			}
			return json.RawMessage(m.Data), nil
		case <-timeout:
			return nil, errors.New("the hub did not answer in time")
		}
	}
}

func (m *Runtime) Review(r ReviewRequest) (ReviewReply, error) {
	if m.Pages == nil {
		return ReviewReply{}, errors.New("the review server is not running")
	}
	it, ok := m.ReviewItem(r)
	if !ok {
		return ReviewReply{}, errors.New("no factory owns this page or session")
	}
	store, err := m.Pages.Store(ReviewFactory(it))
	if err != nil {
		return ReviewReply{}, err
	}
	switch r.Action {
	case "status":
		return m.ReviewStatus(it, r.Page), nil
	case "send":
		return m.SendReview(it, store, r.Page)
	case "approve":
		return m.ApproveFromPage(it, r.Page)
	case "reply":
		return m.ReplyToThread(it, store, r)
	case "threads":
		return ReviewReply{Threads: store.Threads(r.Page)}, nil
	}
	return ReviewReply{}, fmt.Errorf("unknown review action %q", r.Action)
}

func (m *Runtime) ReviewItem(r ReviewRequest) (Item, bool) {
	if r.ID != "" {
		return m.snap.Named(r.ID)
	}
	if r.Factory != "" {
		for _, it := range m.snap.Items {
			if ReviewKey(it.Factory) == r.Factory {
				return it, true
			}
		}
		return Item{}, false
	}
	if it, ok := m.ItemForStage(r.Token, r.Cwd); ok {
		return it, true
	}
	for _, it := range m.snap.Items {
		if it.Factory.Dir != "" && Within(r.Cwd, it.Factory.Dir) {
			return it, true
		}
	}
	return Item{}, false
}

func ReviewKey(f factory.Factory) string {
	return state.FileStem(f.Repo, f.Slug)
}

func ReviewFactory(it Item) review.Factory {
	return review.Factory{Key: ReviewKey(it.Factory), Label: it.Factory.Label(), Dir: it.Factory.Dir}
}

func (m *Runtime) ReviewStatus(it Item, page string) ReviewReply {
	stage := StageOfPage(it.Factory, page)
	reply := ReviewReply{Owner: state.RoleOf(stage)}
	if gate, ok := OpenGate(it.Factory, stage); ok {
		reply.Gate = gate.ID
	}
	return reply
}

func (m *Runtime) SendReview(it Item, store *review.Store, page string) (ReviewReply, error) {
	drafts := store.Drafts(page)
	if len(drafts) == 0 {
		return ReviewReply{}, errors.New("there are no unsent comments on this page")
	}
	batch := store.NextBatch()
	id := review.BatchID(it.Factory.Label(), batch)
	path, text, err := review.WriteBatch(it.Factory.Dir, id, page, drafts, store.TakeConsole(), batch)
	if err != nil {
		return ReviewReply{}, err
	}
	target, err := m.DeliverReview(it, page, id, path, text)
	if err != nil {
		os.Remove(path)
		return ReviewReply{}, err
	}
	ids := make([]string, len(drafts))
	for i, t := range drafts {
		ids[i] = t.ID
	}
	if err := store.MarkSent(batch, ids); err != nil {
		return ReviewReply{}, err
	}
	m.Pages.ThreadsChanged(ReviewFactory(it))
	return ReviewReply{Target: target, Batch: path, Count: len(drafts)}, nil
}

func (m *Runtime) DeliverReview(it Item, page, id, path, text string) (string, error) {
	if role := state.RoleOf(StageOfPage(it.Factory, page)); role != "" && m.ReachesRole(it, role, text) {
		return role, nil
	}
	if it.Pane == nil {
		return "", fmt.Errorf("%s has no supervisor pane; wake it, then send again", it.Factory.Label())
	}
	m.QueueType(QueuedType{Pane: it.Pane.ID, Token: it.Pane.Token, Label: it.Factory.Label(), Text: id + " comments: " + path})
	return "supervisor", nil
}

func (m *Runtime) ReachesRole(it Item, role, text string) bool {
	runs, err := state.LoadStageRuns(state.StageRunsPath(m.Config.StateDir, it.Factory.Repo, it.Factory.Slug))
	if err != nil {
		return false
	}
	latest, ran := runs.Latest(role)
	if !ran {
		return false
	}
	if _, alive := LiveStagePane(stagePaneOf(m.snap.Panes, it.Factory, role, latest.Token)); !alive && m.Engine == nil {
		return false
	}
	_, err = m.SendStage(it, runs, StageRequest{Role: role, Text: text, Review: true})
	return err == nil
}

func (m *Runtime) ApproveFromPage(it Item, page string) (ReviewReply, error) {
	gate, ok := OpenGate(it.Factory, StageOfPage(it.Factory, page))
	if !ok {
		return ReviewReply{}, errors.New("this page has no open gate to approve")
	}
	if it.Pane == nil {
		return ReviewReply{}, fmt.Errorf("%s has no supervisor pane; wake it, then approve again", it.Factory.Label())
	}
	text := factory.ForSupervisor(it.Factory.Supervisor().ToolName(), strings.TrimSpace(fmt.Sprintf("/factory approve %s %s", gate.Stage(), gate.ID)))
	mark := Answer{Factory: it.Factory.Key(), ItemID: gate.ID, ItemSince: gate.CreatedAt.Time, Head: state.PromptHead(text)}
	if m.Queued != nil {
		m.Queued(mark)
	}
	m.QueueType(QueuedType{Pane: it.Pane.ID, Token: it.Pane.Token, Label: it.Factory.Label(), Text: text, Answers: &mark})
	if m.Loader != nil {
		m.Loader.Note(it.Factory, "approved "+gate.ID+" from the review page")
	}
	return ReviewReply{Target: "supervisor", Gate: gate.ID}, nil
}

func (m *Runtime) ReplyToThread(it Item, store *review.Store, r ReviewRequest) (ReviewReply, error) {
	if r.Thread == "" {
		return ReviewReply{}, errors.New("name the thread with --thread")
	}
	thread, err := store.Reply(r.Thread, m.Speaker(it, r.Token), r.Text, r.Resolve)
	if err != nil {
		return ReviewReply{}, err
	}
	m.Pages.ThreadsChanged(ReviewFactory(it))
	return ReviewReply{Threads: []review.Thread{thread}}, nil
}

func (m *Runtime) Speaker(it Item, token string) string {
	if token == "" {
		return "agent"
	}
	if it.Pane != nil && it.Pane.Token == token {
		return "supervisor"
	}
	runs, err := state.LoadStageRuns(state.StageRunsPath(m.Config.StateDir, it.Factory.Repo, it.Factory.Slug))
	if err == nil {
		for _, run := range runs.Runs {
			if run.Token == token {
				return run.Role
			}
		}
	}
	return "agent"
}

func StageOfPage(f factory.Factory, page string) string {
	switch page {
	case review.App:
		return "walkthrough"
	case review.Prototype:
		return "design"
	}
	if f.State != nil && f.Dir != "" {
		url := "file://" + filepath.Join(f.Dir, filepath.FromSlash(page))
		for _, key := range state.StageKeys {
			for _, a := range f.State.Stages[key].Artifacts {
				if a.URL == url {
					return key
				}
			}
		}
	}
	switch {
	case strings.HasPrefix(page, "design/"):
		return "design"
	case page == "plan.html" || page == "schematic.html":
		return "plan"
	}
	return ""
}

func OpenGate(f factory.Factory, stage string) (state.Attention, bool) {
	if f.State == nil || stage == "" {
		return state.Attention{}, false
	}
	for _, a := range f.State.OpenAttention() {
		if a.Kind == "gate" && a.Stage() == stage {
			return a, true
		}
	}
	return state.Attention{}, false
}

func (m *Runtime) SyncReview() {
	if m.Pages == nil {
		return
	}
	factories := make([]review.Factory, 0, len(m.snap.Items))
	for _, it := range m.snap.Items {
		factories = append(factories, ReviewFactory(it))
	}
	m.Pages.SetFactories(factories)
}
