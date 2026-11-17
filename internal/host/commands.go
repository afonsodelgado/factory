package host

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"time"

	"github.com/afonsodelgado/factory/internal/factory"
	"github.com/afonsodelgado/factory/internal/state"
)

type Presentation struct {
	Snapshot   Snapshot
	Claims     map[string]Answer
	Settled    []Answer
	Notice     string
	Repos      []string
	Threads    map[string]int
	PRs        map[string][]PR
	Checked    time.Time
	FirstStart string
	Holdings   map[string]Holding
}

func (m *Runtime) Presentation() Presentation {
	return Presentation{
		Snapshot: m.snap, Claims: m.receipts.Claims, Settled: m.receipts.Settled, Notice: m.TakeNotice(), Repos: m.Config.Repos,
		Threads: m.openThreads(), PRs: m.prs, Checked: m.prsChecked(), FirstStart: m.takeFirstStart(),
		Holdings: maps.Clone(m.holdings),
	}
}

func (m *Runtime) openThreads() map[string]int {
	if m.Pages == nil {
		return nil
	}
	open := map[string]int{}
	for _, it := range m.snap.Items {
		if it.Factory.State == nil {
			continue
		}
		for _, a := range it.Factory.State.Artifacts() {
			if n, counted := m.Pages.OpenCount(a.URL); counted {
				open[a.URL] = n
			}
		}
	}
	return open
}

type QueuedAnswer struct {
	Answer Answer
	Text   string
}

func (m *Runtime) QueueAnswerFor(q QueuedAnswer) (Answer, error) {
	it, ok := m.snap.Find(q.Answer.Factory)
	if !ok {
		return Answer{}, fmt.Errorf("no factory %s", q.Answer.Factory)
	}
	if err := m.unverified(q.Answer.Factory); err != nil {
		return Answer{}, err
	}
	if claimed, ok := m.Claim(q.Answer.Key()); ok && claimed.ItemSince.Equal(q.Answer.ItemSince) {
		return claimed, nil
	}
	item, open := openItem(it, q.Answer.ItemID, q.Answer.ItemSince)
	if !open {
		return Answer{}, errors.New("This item was answered or replaced since it was loaded. Nothing was sent.")
	}
	if reason := m.answer(it, item, q.Answer, q.Text); reason != "" {
		return Answer{}, errors.New("Not sent. " + reason)
	}
	m.note(it.Factory, "answered "+q.Answer.ItemID+" from the hub")
	claimed, _ := m.Claim(q.Answer.Key())
	return claimed, nil
}

func (m *Runtime) Register(r RepoRequest) {
	if r.Forget {
		m.ForgetRepo(r.Repo)
		return
	}
	m.AddRepo(r.Repo)
}

type Removal struct {
	Removal factory.Removal
	Work    factory.Work
	Digest  string
}

func WorkDigest(w factory.Work) string {
	data, _ := json.Marshal(w)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func (m *Runtime) deletable(r DeleteRequest) (factory.Factory, error) {
	it, ok := m.snap.Find(state.Key(r.Repo, r.Slug))
	if !ok && m.Loader != nil {
		it.Factory, ok = m.Loader.Lookup(r.Repo, r.Slug)
	}
	if !ok {
		return factory.Factory{}, fmt.Errorf("no factory %s", r.Slug)
	}
	return it.Factory, nil
}

func (m *Runtime) Removal(r DeleteRequest) (Removal, error) {
	f, err := m.deletable(r)
	if err != nil {
		return Removal{}, err
	}
	removal := factory.PlanRemoval(f, m.Config)
	work := factory.CheckWork(removal)
	return Removal{Removal: removal, Work: work, Digest: WorkDigest(work)}, nil
}

func (m *Runtime) workChanged(f factory.Factory, confirmed string) bool {
	work := factory.CheckWork(factory.PlanRemoval(f, m.Config))
	return !work.Empty() && WorkDigest(work) != confirmed
}

type ResolveRequest struct {
	Dir       string `json:"dir"`
	ID        string `json:"id"`
	WaitingOn string `json:"waiting_on,omitempty"`
}

func (m *Runtime) Resolve(r ResolveRequest) (string, error) {
	it, ok := m.itemInDir(r.Dir)
	if !ok {
		return "", fmt.Errorf("no factory the node runs keeps its state in %s", r.Dir)
	}
	if it.Factory.State == nil {
		return "", fmt.Errorf("%s has no readable state.json", it.Factory.Label())
	}
	isOpen := func(a state.Attention) bool { return a.ID == r.ID }
	if !slices.ContainsFunc(it.Factory.State.OpenAttention(), isOpen) {
		return "", fmt.Errorf("%s has no open attention item %q; it is already resolved or was never raised", it.Factory.Label(), r.ID)
	}
	return ResolutionInstruction(filepath.Join(it.Factory.Dir, "state.json"), r, m.Now()), nil
}

func (m *Runtime) itemInDir(dir string) (Item, bool) {
	for _, it := range m.snap.Items {
		if it.Factory.Dir != "" && resolved(it.Factory.Dir) == resolved(dir) {
			return it, true
		}
	}
	return Item{}, false
}

func ResolutionInstruction(path string, r ResolveRequest, now time.Time) string {
	stamp := now.UTC().Format(time.RFC3339)
	waiting := ""
	switch r.WaitingOn {
	case "":
	case "none":
		waiting = `, set "waiting_on" and "waiting_reason" to null`
	default:
		waiting = fmt.Sprintf(`, set "waiting_on" to %q`, r.WaitingOn)
	}
	return fmt.Sprintf(`Resolve attention item %q yourself: in %s set its "resolved_at" to %q%s, and set "updated_at" to %q. Only the supervisor writes state.json; the node changed nothing.`, r.ID, path, stamp, waiting, stamp)
}

type AttachRequest struct {
	Key   string `json:"key"`
	Role  string `json:"role,omitempty"`
	Token string `json:"token"`
}

func (m *Runtime) Attach(r AttachRequest) (string, error) {
	it, ok := m.snap.Find(r.Key)
	if !ok {
		return "", fmt.Errorf("no factory %s", r.Key)
	}
	return PrepareView(m.TM, it.Factory, r.Role, r.Token)
}
