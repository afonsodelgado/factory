package node

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/afonsodelgado/factory/internal/state"
)

const (
	opAdd    = "add"
	opRemove = "remove"
	opHome   = "home"
)

type Member struct {
	ID      string `json:"id"`
	Peer    string `json:"peer,omitempty"`
	Name    string `json:"name"`
	Owner   string `json:"owner"`
	Tailnet string `json:"tailnet"`
	OS      string `json:"os,omitempty"`
}

func (m Member) short() string {
	name, _, _ := strings.Cut(m.Name, ".")
	return name
}

type Clock map[string]int64

func (c Clock) covers(op Op) bool { return c[op.Author] >= op.Clock[op.Author] }

type Op struct {
	ID     string    `json:"id"`
	Author string    `json:"author"`
	Clock  Clock     `json:"clock"`
	Kind   string    `json:"kind"`
	Member Member    `json:"member"`
	At     time.Time `json:"at"`
}

func (op Op) observed(earlier Op) bool { return op.Clock.covers(earlier) }

type Ledger struct {
	Roster string `json:"roster"`
	Home   string `json:"home,omitempty"`
	Ops    []Op   `json:"ops"`
}

func (l Ledger) clock() Clock {
	c := Clock{}
	for _, op := range l.Ops {
		for author, n := range op.Clock {
			c[author] = max(c[author], n)
		}
	}
	return c
}

func (l Ledger) author(self, kind, id string, m Member, at time.Time) Op {
	c := l.clock()
	c[self]++
	return Op{ID: id, Author: self, Clock: c, Kind: kind, Member: m, At: at}
}

func (l Ledger) with(ops ...Op) Ledger {
	known := map[string]bool{}
	merged := slices.Clone(l.Ops)
	for _, op := range merged {
		known[op.ID] = true
	}
	for _, op := range ops {
		if !known[op.ID] {
			known[op.ID] = true
			merged = append(merged, op)
		}
	}
	l.Ops = merged
	return l
}

func (l Ledger) present() map[string]Member {
	byMember := map[string][]Op{}
	for _, op := range l.Ops {
		byMember[op.Member.ID] = append(byMember[op.Member.ID], op)
	}
	members := map[string]Member{}
	for id, ops := range byMember {
		if latest, ok := standingAdd(ops); ok {
			members[id] = latest.Member
		}
	}
	return members
}

func standingAdd(ops []Op) (Op, bool) {
	var removals, standing []Op
	for _, op := range ops {
		if op.Kind == opRemove {
			removals = append(removals, op)
		}
	}
	for _, op := range ops {
		observesEveryRemoval := !slices.ContainsFunc(removals, func(r Op) bool { return !op.observed(r) })
		if op.Kind == opAdd && observesEveryRemoval {
			standing = append(standing, op)
		}
	}
	if len(standing) == 0 {
		return Op{}, false
	}
	return slices.MaxFunc(standing, func(a, b Op) int { return cmp.Or(a.At.Compare(b.At), strings.Compare(a.ID, b.ID)) }), true
}

func (l Ledger) removals(id string) []Op {
	return slices.DeleteFunc(slices.Clone(l.Ops), func(op Op) bool { return op.Kind != opRemove || op.Member.ID != id })
}

func (l Ledger) lastKnown(id string) Member {
	var m Member
	for _, op := range l.Ops {
		if op.Kind == opAdd && op.Member.ID == id {
			m = op.Member
		}
	}
	return m
}

func (l Ledger) homes() []Op {
	return slices.DeleteFunc(slices.Clone(l.Ops), func(op Op) bool { return op.Kind != opHome })
}

func clockSum(c Clock) int64 {
	var sum int64
	for _, n := range c {
		sum += n
	}
	return sum
}

func laterHome(a, b Op) int {
	return cmp.Or(cmp.Compare(clockSum(a.Clock), clockSum(b.Clock)), strings.Compare(a.Author, b.Author), strings.Compare(a.ID, b.ID))
}

func (l Ledger) home() string {
	homes := l.homes()
	if len(homes) == 0 {
		return l.Home
	}
	return "https://" + slices.MaxFunc(homes, laterHome).Member.Name
}

func (l Ledger) previousHomes() []string {
	current := l.home()
	var previous []string
	for _, origin := range append([]string{l.Home}, homeOrigins(l.homes())...) {
		if origin != "" && origin != current && !slices.Contains(previous, origin) {
			previous = append(previous, origin)
		}
	}
	return previous
}

func homeOrigins(ops []Op) []string {
	origins := make([]string, len(ops))
	for i, op := range ops {
		origins[i] = "https://" + op.Member.Name
	}
	return origins
}

type RosterMember struct {
	Member
	Self    bool `json:"self,omitempty"`
	Pending int  `json:"pending"`
}

type Roster struct {
	ID            string         `json:"id"`
	Home          string         `json:"home"`
	PreviousHomes []string       `json:"previous_homes,omitempty"`
	Self          string         `json:"self"`
	Removed       bool           `json:"removed,omitempty"`
	Members       []RosterMember `json:"members"`
	Leaving       []RosterMember `json:"leaving,omitempty"`
}

type book struct {
	Ledger
	Acks map[string]Clock `json:"acks,omitempty"`
}

func (b book) pending(id string) int {
	ack := b.Acks[id]
	n := 0
	for _, op := range b.Ops {
		if !ack.covers(op) {
			n++
		}
	}
	return n
}

var ErrOtherRoster = errors.New("that node keeps another node list")

type Directory struct {
	path string
	self string
	now  func() time.Time

	mu   sync.Mutex
	book book
}

func RosterPath(stateDir string) string { return filepath.Join(stateDir, "roster.json") }

func OpenDirectory(stateDir, self string, now func() time.Time) (*Directory, error) {
	d := &Directory{path: RosterPath(stateDir), self: self, now: now}
	data, err := os.ReadFile(d.path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return d, nil
	case err != nil:
		return nil, err
	}
	if err := json.Unmarshal(data, &d.book); err != nil {
		return nil, fmt.Errorf("%s: %w", d.path, err)
	}
	return d, nil
}

func (d *Directory) save(b book) error {
	data, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		return err
	}
	if err := state.WriteFileAtomic(d.path, data); err != nil {
		return err
	}
	d.book = b
	return nil
}

func (d *Directory) Seed(self Member, home string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.book.Roster != "" {
		return nil
	}
	l := Ledger{Roster: uuid.NewString(), Home: home}
	return d.save(book{Ledger: l.with(l.author(d.self, opAdd, uuid.NewString(), self, d.now()))})
}

func (d *Directory) Home() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.book.home()
}

func (d *Directory) MoveHome(m Member, op string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if slices.ContainsFunc(d.book.Ops, func(o Op) bool { return o.ID == op }) {
		return nil
	}
	b := d.book
	b.Ledger = b.with(b.author(d.self, opHome, op, m, d.now()))
	return d.save(b)
}

func (d *Directory) Ledger() Ledger {
	d.mu.Lock()
	defer d.mu.Unlock()
	l := d.book.Ledger
	l.Ops = slices.Clone(l.Ops)
	return l
}

func (d *Directory) Clock() Clock {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.book.clock()
}

func (d *Directory) Op(id string) (Op, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	i := slices.IndexFunc(d.book.Ops, func(op Op) bool { return op.ID == id })
	if i < 0 {
		return Op{}, false
	}
	return d.book.Ops[i], true
}

func (d *Directory) Member(id string) (Member, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	m, ok := d.book.present()[id]
	return m, ok
}

func (d *Directory) For(id string) Ledger {
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, ok := d.book.present()[id]; ok || len(d.book.removals(id)) == 0 {
		return d.book.Ledger
	}
	return Ledger{Roster: d.book.Roster, Ops: d.book.removals(id)}
}

func (d *Directory) Roster() Roster {
	d.mu.Lock()
	defer d.mu.Unlock()
	b := d.book
	present := b.present()
	r := Roster{ID: b.Roster, Home: b.home(), PreviousHomes: b.previousHomes(), Self: d.self}
	if _, here := present[d.self]; !here && len(b.removals(d.self)) > 0 {
		r.Removed = true
		r.Members = []RosterMember{{Member: b.lastKnown(d.self), Self: true}}
		return r
	}
	for _, id := range slices.Sorted(maps.Keys(present)) {
		r.Members = append(r.Members, RosterMember{Member: present[id], Self: id == d.self, Pending: b.pendingFor(id, d.self)})
	}
	slices.SortStableFunc(r.Members, func(a, b RosterMember) int {
		return cmp.Or(-compareBool(a.Self, b.Self), strings.Compare(a.Name, b.Name))
	})
	for _, id := range b.unacknowledgedRemovals(present, d.self) {
		r.Leaving = append(r.Leaving, RosterMember{Member: b.lastKnown(id), Pending: len(b.removals(id))})
	}
	return r
}

func compareBool(a, b bool) int {
	switch {
	case a == b:
		return 0
	case a:
		return 1
	}
	return -1
}

func (b book) pendingFor(id, self string) int {
	if id == self {
		return 0
	}
	return b.pending(id)
}

func (b book) unacknowledgedRemovals(present map[string]Member, self string) []string {
	var ids []string
	for _, op := range b.Ops {
		_, stillHere := present[op.Member.ID]
		acknowledged := b.Acks[op.Member.ID].covers(op)
		if op.Kind == opRemove && !stillHere && op.Member.ID != self && !acknowledged && !slices.Contains(ids, op.Member.ID) {
			ids = append(ids, op.Member.ID)
		}
	}
	return ids
}

func (d *Directory) Removed() []Member {
	d.mu.Lock()
	defer d.mu.Unlock()
	var gone []Member
	for _, id := range d.book.unacknowledgedRemovals(d.book.present(), d.self) {
		gone = append(gone, d.book.lastKnown(id))
	}
	return gone
}

func (d *Directory) Draft(m Member, op string, theirs Ledger) Ledger {
	l, _ := d.draft(m, op, theirs)
	return l
}

func (d *Directory) draft(m Member, op string, theirs Ledger) (Ledger, []Member) {
	d.mu.Lock()
	defer d.mu.Unlock()
	l := d.book.Ledger
	if theirs.Roster == l.Roster {
		l = l.with(theirs.Ops...)
	}
	var restored []Member
	present, current := l.present(), d.book.present()
	for _, id := range slices.Sorted(maps.Keys(current)) {
		if _, kept := present[id]; !kept && id != m.ID {
			l = l.with(l.author(d.self, opAdd, uuid.NewString(), current[id], d.now()))
			restored = append(restored, current[id])
		}
	}
	return l.with(l.author(d.self, opAdd, op, m, d.now())), restored
}

func (d *Directory) Merge(l Ledger) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if l.Roster != d.book.Roster {
		return ErrOtherRoster
	}
	merged := d.book
	merged.Ledger = merged.with(l.Ops...)
	if len(merged.Ops) == len(d.book.Ops) {
		return nil
	}
	return d.save(merged)
}

func (d *Directory) Adopt(l Ledger) error {
	d.mu.Lock()
	sameRoster := l.Roster == d.book.Roster
	alone := len(d.book.present()) <= 1
	d.mu.Unlock()
	switch {
	case sameRoster:
		return d.Merge(l)
	case !alone:
		return ErrOtherRoster
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.save(book{Ledger: Ledger{Roster: l.Roster, Home: l.Home, Ops: slices.Clone(l.Ops)}})
}

func (d *Directory) Remove(id, op string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if slices.ContainsFunc(d.book.Ops, func(o Op) bool { return o.ID == op }) {
		return nil
	}
	m, ok := d.book.present()[id]
	if !ok {
		return fmt.Errorf("no node %s in the list", id)
	}
	b := d.book
	b.Ledger = b.with(b.author(d.self, opRemove, op, Member{ID: id, Name: m.Name}, d.now()))
	return d.save(b)
}

func (d *Directory) Ack(id string, c Clock) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	b := d.book
	acks := maps.Clone(b.Acks)
	if acks == nil {
		acks = map[string]Clock{}
	}
	merged := maps.Clone(acks[id])
	if merged == nil {
		merged = Clock{}
	}
	for author, n := range c {
		merged[author] = max(merged[author], n)
	}
	if maps.Equal(merged, acks[id]) {
		return nil
	}
	acks[id] = merged
	b.Acks = acks
	return d.save(b)
}
