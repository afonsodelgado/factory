package hub

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/afonsodelgado/factory/internal/factory"
	"github.com/afonsodelgado/factory/internal/review"
	"github.com/afonsodelgado/factory/internal/state"
)

const reviewWait = 10 * time.Second

type ReviewRequest struct {
	Action  string `json:"action"`
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

func (m *Model) reviewRequest(e Event) tea.Cmd {
	var r ReviewRequest
	var reply ReviewReply
	err := json.Unmarshal(e.Data, &r)
	if err == nil {
		reply, err = m.review(r)
	}
	if err != nil {
		e.respond(Message{T: "error", Error: err.Error()})
		return nil
	}
	data, _ := json.Marshal(reply)
	e.respond(Message{T: "done", Data: data})
	return m.load()
}

func (m *Model) review(r ReviewRequest) (ReviewReply, error) {
	if m.Review == nil {
		return ReviewReply{}, errors.New("the review server is not running")
	}
	it, ok := m.reviewItem(r)
	if !ok {
		return ReviewReply{}, errors.New("no factory owns this page or session")
	}
	store, err := m.Review.Store(reviewFactory(it))
	if err != nil {
		return ReviewReply{}, err
	}
	switch r.Action {
	case "status":
		return m.reviewStatus(it, r.Page), nil
	case "send":
		return m.sendReview(it, store, r.Page)
	case "approve":
		return m.approveFromPage(it, r.Page)
	case "reply":
		return m.replyToThread(it, store, r)
	case "threads":
		return ReviewReply{Threads: store.Threads(r.Page)}, nil
	}
	return ReviewReply{}, fmt.Errorf("unknown review action %q", r.Action)
}

func (m *Model) reviewItem(r ReviewRequest) (Item, bool) {
	if r.Factory != "" {
		for _, it := range m.snap.Items {
			if reviewKey(it.Factory) == r.Factory {
				return it, true
			}
		}
		return Item{}, false
	}
	if it, ok := m.itemForStage(r.Token, r.Cwd); ok {
		return it, true
	}
	for _, it := range m.snap.Items {
		if it.Factory.Dir != "" && within(r.Cwd, it.Factory.Dir) {
			return it, true
		}
	}
	return Item{}, false
}

func reviewKey(f factory.Factory) string {
	return state.FileStem(f.Repo, f.Slug)
}

func reviewFactory(it Item) review.Factory {
	return review.Factory{Key: reviewKey(it.Factory), Label: it.Factory.Label(), Dir: it.Factory.Dir}
}

func (m *Model) reviewStatus(it Item, page string) ReviewReply {
	stage := stageOfPage(it.Factory, page)
	reply := ReviewReply{Owner: state.RoleOf(stage)}
	if gate, ok := openGate(it.Factory, stage); ok {
		reply.Gate = gate.ID
	}
	return reply
}

func (m *Model) sendReview(it Item, store *review.Store, page string) (ReviewReply, error) {
	drafts := store.Drafts(page)
	if len(drafts) == 0 {
		return ReviewReply{}, errors.New("there are no unsent comments on this page")
	}
	batch := store.NextBatch()
	id := review.BatchID(it.Factory.Label(), batch)
	path, text, err := review.WriteBatch(it.Factory.Dir, id, page, drafts, batch)
	if err != nil {
		return ReviewReply{}, err
	}
	target, err := m.deliverReview(it, page, id, path, text)
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
	m.Review.ThreadsChanged(reviewFactory(it))
	return ReviewReply{Target: target, Batch: path, Count: len(drafts)}, nil
}

func (m *Model) deliverReview(it Item, page, id, path, text string) (string, error) {
	if role := state.RoleOf(stageOfPage(it.Factory, page)); role != "" && m.reachesRole(it, role, text) {
		return role, nil
	}
	if it.Pane == nil {
		return "", fmt.Errorf("%s has no supervisor pane; wake it, then send again", it.Factory.Label())
	}
	m.queueType(queuedType{Pane: it.Pane.ID, Token: it.Pane.Token, Label: it.Factory.Label(), Text: id + " comments: " + path})
	return "supervisor", nil
}

func (m *Model) reachesRole(it Item, role, text string) bool {
	runs, err := state.LoadStageRuns(state.StageRunsPath(m.Config.StateDir, it.Factory.Repo, it.Factory.Slug))
	if err != nil {
		return false
	}
	latest, ran := runs.Latest(role)
	if !ran {
		return false
	}
	if _, alive := livePane(stagePaneOf(m.snap.Panes, it.Factory, role, latest.Token)); !alive && m.Engine == nil {
		return false
	}
	_, err = m.sendStage(it, runs, StageRequest{Role: role, Text: text, review: true})
	return err == nil
}

func (m *Model) approveFromPage(it Item, page string) (ReviewReply, error) {
	gate, ok := openGate(it.Factory, stageOfPage(it.Factory, page))
	if !ok {
		return ReviewReply{}, errors.New("this page has no open gate to approve")
	}
	if it.Pane == nil {
		return ReviewReply{}, fmt.Errorf("%s has no supervisor pane; wake it, then approve again", it.Factory.Label())
	}
	text := factory.ForSupervisor(it.Factory.Supervisor().ToolName(), GateApprove(gate.Stage(), gate.ID, ""))
	m.queueType(queuedType{Pane: it.Pane.ID, Token: it.Pane.Token, Label: it.Factory.Label(), Text: text})
	return ReviewReply{Target: "supervisor", Gate: gate.ID}, nil
}

func (m *Model) replyToThread(it Item, store *review.Store, r ReviewRequest) (ReviewReply, error) {
	if r.Thread == "" {
		return ReviewReply{}, errors.New("name the thread with --thread")
	}
	thread, err := store.Reply(r.Thread, m.speaker(it, r.Token), r.Text, r.Resolve)
	if err != nil {
		return ReviewReply{}, err
	}
	m.Review.ThreadsChanged(reviewFactory(it))
	return ReviewReply{Threads: []review.Thread{thread}}, nil
}

func (m *Model) speaker(it Item, token string) string {
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

func stageOfPage(f factory.Factory, page string) string {
	if page == review.App {
		return "walkthrough"
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

func openGate(f factory.Factory, stage string) (state.Attention, bool) {
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

func (m *Model) syncReview() {
	if m.Review == nil {
		return
	}
	factories := make([]review.Factory, 0, len(m.snap.Items))
	for _, it := range m.snap.Items {
		factories = append(factories, reviewFactory(it))
	}
	m.Review.SetFactories(factories)
}

func (m *Model) reviewURL(url string) string {
	if m.Review == nil {
		return url
	}
	if page, ok := m.Review.PageURL(url); ok {
		return page
	}
	return url
}

func (m *Model) threadCounter() threadCounter {
	if m.Review == nil {
		return nil
	}
	return m.Review.OpenCount
}
