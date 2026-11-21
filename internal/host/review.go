package host

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/afonsodelgado/factory/internal/factory"
	"github.com/afonsodelgado/factory/internal/review"
	"github.com/afonsodelgado/factory/internal/state"
)

const reviewWait = 10 * time.Second

type ReviewRequest struct {
	Action  string                 `json:"action"`
	URL     string                 `json:"url,omitempty"`
	ID      string                 `json:"id,omitempty"`
	Token   string                 `json:"token,omitempty"`
	Cwd     string                 `json:"cwd,omitempty"`
	Factory string                 `json:"factory,omitempty"`
	Page    string                 `json:"page,omitempty"`
	Thread  string                 `json:"thread,omitempty"`
	Text    string                 `json:"text,omitempty"`
	Resolve bool                   `json:"resolve,omitempty"`
	Gate    string                 `json:"gate,omitempty"`
	Capture *review.CaptureRequest `json:"capture,omitempty"`
	Run     string                 `json:"run,omitempty"`
}

type ReviewReply struct {
	Target  string          `json:"target,omitempty"`
	Batch   string          `json:"batch,omitempty"`
	Count   int             `json:"count,omitempty"`
	Gate    string          `json:"gate,omitempty"`
	Owner   string          `json:"owner,omitempty"`
	Threads []review.Thread `json:"threads,omitempty"`
	Stack   *StackStatus    `json:"stack,omitempty"`
	Capture *CaptureStatus  `json:"capture,omitempty"`
}

const (
	CaptureRunning = "running"
	CaptureDone    = "done"
	CaptureFailed  = "failed"
)

type CaptureStatus struct {
	Factory string               `json:"factory"`
	Run     string               `json:"run"`
	State   string               `json:"state"`
	Result  review.CaptureResult `json:"result"`
	Reason  string               `json:"reason,omitempty"`
}

func ReviewActions(events chan<- Event) func(review.Action) (any, error) {
	return func(a review.Action) (any, error) {
		data, err := json.Marshal(ReviewRequest{Action: a.Kind, Factory: a.Factory, Page: a.Page, Gate: a.Gate})
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
		return m.ApproveFromPage(it, r.Page, r.Gate)
	case "reply":
		return m.ReplyToThread(it, store, r)
	case "threads":
		return ReviewReply{Threads: store.Threads(r.Page)}, nil
	case "open", "link":
		return m.checkOpenable(r.URL)
	case "stack":
		return m.startStack(it, r.Page)
	case "stack-status":
		return m.stackStatus(it)
	case "capture":
		return m.startCapture(it, r.Capture)
	case "capture-status":
		return m.captureStatus(it, r.Run)
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
	var id, target string
	sent, err := store.Send(page, func(drafts []review.Thread, batch int) error {
		id = review.BatchID(it.Factory.Label(), batch)
		path, _, err := review.WriteBatch(it.Factory.Dir, id, page, drafts, store.TakeConsole(), batch)
		if err != nil {
			return err
		}
		if target, err = m.DeliverReview(it, page, id, path); err != nil {
			os.Remove(path)
		}
		return err
	})
	if err != nil {
		return ReviewReply{}, err
	}
	m.Pages.ThreadsChanged(ReviewFactory(it))
	return ReviewReply{Target: target, Batch: id, Count: len(sent)}, nil
}

func (m *Runtime) DeliverReview(it Item, page, id, path string) (string, error) {
	if it.Pane == nil {
		return "", fmt.Errorf("%s has no supervisor pane; wake it, then send again", it.Factory.Label())
	}
	m.QueueType(QueuedType{Pane: it.Pane.ID, Token: it.Pane.Token, Label: it.Factory.Label(), Text: id + " comments: " + path + batchInstruction(it.Factory, page)})
	return "supervisor", nil
}

func batchInstruction(f factory.Factory, page string) string {
	switch StageOfPage(f, page) {
	case "design":
		return "; submit a design_comments event with this batch ID as the receipt before dispatching the designer"
	case "plan":
		gate := "the plan gate"
		if open, ok := OpenGate(f, "plan"); ok {
			gate = "plan gate " + open.ID
		}
		return "; these are plan comments for the architect: submit them as a changes answer on " + gate + " with this batch ID as the receipt"
	}
	return ""
}

func (m *Runtime) ApproveFromPage(it Item, page, seen string) (ReviewReply, error) {
	gate, ok := OpenGate(it.Factory, StageOfPage(it.Factory, page))
	if !ok {
		return ReviewReply{}, errors.New("this page has no open gate to approve")
	}
	if seen != "" && seen != gate.ID {
		return ReviewReply{}, errors.New("the gate on this page changed since it loaded; reload the page, then approve again")
	}
	if it.Pane == nil {
		return ReviewReply{}, fmt.Errorf("%s has no supervisor pane; wake it, then approve again", it.Factory.Label())
	}
	text := factory.ForSupervisor(it.Factory.Supervisor().ToolName(), strings.TrimSpace(fmt.Sprintf("/factory approve %s %s", gate.Stage(), gate.ID)))
	mark := Answer{Factory: it.Factory.Key(), ItemID: gate.ID, ItemSince: gate.CreatedAt.Time, Head: state.PromptHead(text), Source: SourcePage, Action: ActApprove}
	m.QueueAnswer(it, mark, text)
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
		return "design"
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

func (m *Runtime) startCapture(it Item, r *review.CaptureRequest) (ReviewReply, error) {
	switch {
	case m.Capture == nil || m.Jobs == nil:
		return ReviewReply{}, errors.New("design capture runs only inside factory node run")
	case r == nil || len(r.Screens) == 0 || len(r.Viewports) == 0:
		return ReviewReply{}, errors.New("name the source, at least one --screen and a viewport")
	case m.captures[it.Factory.Key()].State == CaptureRunning:
		return ReviewReply{}, fmt.Errorf("a design capture is already running for %s; wait for it to finish, then capture again", it.Factory.Label())
	}
	req := *r
	req.Dir = it.Factory.Dir
	if req.Source == review.Prototype {
		page, err := m.prototypePage(it, req.URL)
		if err != nil {
			return ReviewReply{}, err
		}
		req.URL, req.Entry = page, filepath.Join(it.Factory.Worktree, filepath.FromSlash(req.Entry))
	}
	key := it.Factory.Key()
	status := CaptureStatus{Factory: key, Run: uuid.NewString(), State: CaptureRunning}
	m.captures[key] = status
	capture := m.Capture
	m.Jobs.Start("capture "+key, func(ctx context.Context) Message {
		done := CaptureStatus{Factory: key, Run: status.Run, State: CaptureDone}
		result, err := capture(ctx, req)
		if err != nil {
			done.State, done.Reason = CaptureFailed, err.Error()
		}
		done.Result = result
		data, _ := json.Marshal(done)
		return Message{T: "capture", Data: data}
	})
	return ReviewReply{Capture: &status}, nil
}

func (m *Runtime) prototypePage(it Item, route string) (string, error) {
	stack, ok := m.reviewStack(it.Factory.Key())
	if !ok || stack.State != StackReady || stack.App == "" {
		return "", fmt.Errorf("%s has no running prototype; start it with factory review stack --for design, then capture again", it.Factory.Label())
	}
	app, err := url.Parse(stack.App)
	if err != nil {
		return "", err
	}
	page, err := url.Parse(route)
	if err != nil || route == "" || page.IsAbs() {
		return "", fmt.Errorf("--route %q must be the prototype page's path on the app, such as /prototypes/%s/variant-a.html", route, it.Factory.Label())
	}
	return app.ResolveReference(page).String(), nil
}

func (m *Runtime) captureStatus(it Item, run string) (ReviewReply, error) {
	status, ok := m.captures[it.Factory.Key()]
	if !ok || status.Run != run {
		return ReviewReply{}, errors.New("this design capture is no longer known to the node, which restarted or ran a later one; capture again")
	}
	return ReviewReply{Capture: &status}, nil
}

func (m *Runtime) captureFinished(c CaptureStatus) {
	if m.captures[c.Factory].Run == c.Run {
		m.captures[c.Factory] = c
	}
}
