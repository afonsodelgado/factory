package node

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/afonsodelgado/factory/internal/phone"
)

type PushOutcome struct {
	Fingerprint string `json:"fingerprint,omitempty"`
	Phones      int    `json:"phones"`
	Shared      bool   `json:"shared"`
	Conflict    string `json:"conflict,omitempty"`
}

type PushRecords struct {
	From string `json:"from"`
	phone.Replica
}

func (n *Nodes) keyState() phone.KeyState {
	if n.Phone == nil {
		return phone.KeyState{}
	}
	state, _ := n.Phone.KeyState()
	return state
}

func (n *Nodes) pushState() *phone.KeyState {
	if state := n.keyState(); state.Public != "" {
		return &state
	}
	return nil
}

func (n *Nodes) unstagePush() {
	if n.Phone != nil {
		n.Phone.Unstage()
	}
}

func phones(count int) string {
	if count == 1 {
		return "1 phone"
	}
	return fmt.Sprintf("%d phones", count)
}

func (n *Nodes) pushCheck(c checked) checked {
	mine, theirs := n.keyState(), c.hello.Push
	self, short := n.Self.short(), c.peer.Short()
	row := Check{Name: "push", Mark: MarkOK, At: n.now()}
	switch {
	case mine.Public == "" || theirs == nil:
		return c
	case mine.Conflict != "":
		row.Mark, row.Detail = MarkWarn, self+" kept its own push key when it joined, so it has no shared key to give "+short
	case theirs.Fingerprint == mine.Fingerprint:
		row.Detail = self + "'s key already"
	case theirs.Phones == 0:
		row.Detail = "no key in use yet: it gets " + self + "'s when added"
	default:
		row.Mark, row.Detail = MarkWarn, fmt.Sprintf("%s has its own push key, and %s subscribed to it", short, phones(theirs.Phones))
		c.report.PushConflict = true
	}
	c.report.Checks = append(c.report.Checks, row)
	return c
}

func (n *Nodes) pushToSend(theirs *phone.KeyState, replace bool) (*phone.Shared, string, error) {
	mine := n.keyState()
	keepsItsOwn := theirs != nil && theirs.Fingerprint != mine.Fingerprint && theirs.Phones > 0 && !replace
	switch {
	case mine.Public == "" || mine.Conflict != "" || theirs == nil:
		return nil, "", nil
	case keepsItsOwn:
		return nil, mine.Fingerprint, nil
	}
	shared, err := n.Phone.Share()
	return &shared, "", err
}

func (n *Nodes) stagePush(c Commit) error {
	if n.Phone == nil || c.Push == nil {
		return nil
	}
	return n.Phone.Stage(*c.Push, c.ReplaceKey, n.now())
}

func (n *Nodes) takePush(c Commit) (*phone.KeyState, error) {
	if n.Phone == nil {
		return nil, nil
	}
	var err error
	switch {
	case c.Push != nil:
		_, err = n.Phone.Promote()
	case c.SharedKey != "":
		err = n.Phone.Refuse(c.SharedKey, n.now())
	}
	state, stateErr := n.Phone.KeyState()
	return &state, errors.Join(err, stateErr)
}

func (n *Nodes) pushOutcome(short string, theirs *phone.KeyState) PushOutcome {
	mine := n.keyState()
	if theirs == nil || mine.Public == "" {
		return PushOutcome{}
	}
	o := PushOutcome{Fingerprint: theirs.Fingerprint, Phones: theirs.Phones, Shared: theirs.Fingerprint == mine.Fingerprint}
	switch {
	case o.Shared:
	case mine.Conflict != "":
		o.Conflict = n.Self.short() + " kept its own push key when it joined, so it had no shared key to give " + short + ", and " + short + " can't notify your phone"
	default:
		o.Conflict = short + " is added, but it keeps its own push key, so it can't notify your phone: one key serves every node. To switch it, run factory node push-key export --file <file> on " + n.Self.short() + " and factory node push-key import --replace --file <file> on " + short + "; the phone subscribed to its old key then subscribes again"
	}
	return o
}

func (n *Nodes) helloPush(ctx context.Context, m Member) PushOutcome {
	ctx, cancel := context.WithTimeout(ctx, n.probeWait())
	defer cancel()
	var a answer
	if Remote(m.Name, n.HTTP).Do(ctx, http.MethodGet, "/api/node/hello", nil, &a) != nil {
		return PushOutcome{}
	}
	return n.pushOutcome(m.short(), a.Push)
}

func (n *Nodes) sendRecords(ctx context.Context, m Member) {
	if n.Phone == nil {
		return
	}
	replica, err := n.Phone.Replica()
	if err != nil || replica.Key == "" {
		return
	}
	Remote(m.Name, n.HTTP).Do(ctx, http.MethodPost, "/api/node/push/records", PushRecords{From: n.Self.ID, Replica: replica}, nil)
}

func (n *Nodes) readinessWithPush(w http.ResponseWriter, r *http.Request, _ string) {
	report := n.readiness(r)
	mine := n.keyState()
	switch {
	case mine.Public == "":
	case mine.Conflict != "":
		report.Checks = append(report.Checks, Check{Name: "push", Mark: MarkWarn, Detail: "keeps its own key " + mine.Fingerprint + ", so it can't notify the phone subscribed with the shared key " + mine.Conflict, Fix: "factory node push-key import --replace --file <file exported from another node>", On: n.Self.short(), At: n.now()})
	default:
		report.Checks = append(report.Checks, Check{Name: "push", Mark: MarkOK, Detail: "key " + mine.Fingerprint + " · " + phones(mine.Phones) + " subscribed", At: n.now()})
	}
	reply(w, http.StatusOK, report)
}

func (n *Nodes) recordsRoute(w http.ResponseWriter, r *http.Request, _ string) {
	var body PushRecords
	if err := decode(r, &body); err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	_, member := n.Dir.Member(body.From)
	switch {
	case n.removed() || !member:
		fail(w, http.StatusGone, "that node is not in this node list")
		return
	case n.Phone == nil:
		fail(w, http.StatusNotFound, "this node keeps no phone subscriptions")
		return
	}
	err := n.Phone.Merge(body.Replica, n.now())
	if errors.Is(err, phone.ErrOtherKey) {
		fail(w, http.StatusConflict, err.Error())
		return
	}
	n.answer(w, struct{}{}, err)
}
