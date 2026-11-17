package panel

import (
	"strings"
	"time"

	"github.com/afonsodelgado/factory/internal/host"
)

type Tab int

const (
	Attention Tab = iota
	PRs
	Artifacts
	Activity
)

func (t Tab) Next(delta int) Tab {
	return Tab((int(t) + delta + 4) % 4)
}

type Question = host.Question

type Item = host.AttentionItem

type Draft struct {
	Choice   []int    `json:"choice"`
	Typed    []string `json:"typed"`
	Question int      `json:"question"`
	Text     string   `json:"text"`
	Artifact int      `json:"artifact"`
	Editing  bool     `json:"-"`
	Cursor   int      `json:"-"`
}

func (d *Draft) Fit(questions int) {
	for len(d.Choice) < questions {
		d.Choice = append(d.Choice, -1)
	}
	for len(d.Typed) < questions {
		d.Typed = append(d.Typed, "")
	}
	d.Question = max(0, min(d.Question, questions))
}

func (d Draft) Picked(q Question, i int) int {
	if i < len(d.Choice) && d.Choice[i] >= 0 {
		return d.Choice[i]
	}
	return max(q.RecommendedIndex(), q.TopRow())
}

func (d Draft) Typing(q Question, i int) bool {
	return i < len(d.Choice) && d.Choice[i] == len(q.Options)
}

func (d Draft) Answered(q Question, i int) bool {
	switch {
	case d.Typing(q, i):
		return strings.TrimSpace(d.Typed[i]) != ""
	case i < len(d.Choice) && d.Choice[i] >= 0:
		return true
	}
	return q.Recommended != ""
}

func (d Draft) Unanswered(questions []Question) int {
	for i, q := range questions {
		if !d.Answered(q, i) {
			return i
		}
	}
	return -1
}

func (d Draft) Answer(q Question, i int) string {
	switch {
	case i >= len(d.Choice) || d.Choice[i] < 0:
		return "recommended"
	case d.Typing(q, i):
		return d.Typed[i]
	}
	return q.Options[d.Choice[i]]
}

type PR = host.PR

type ArtifactRow = host.ArtifactRow

type ActivityRow struct {
	At      time.Time
	Factory string
	Key     string
	Stage   string
	Source  string
	Text    string
}

type Send struct {
	Ready   bool
	Reason  string
	Preview string
	Refused string
}

type Progress int

const (
	Queued Progress = iota
	Sending
	Answered
)

type Settled struct {
	ID    string
	State Progress
}

type View struct {
	Label     string
	Tab       Tab
	Focused   bool
	Collapsed bool
	Dim       bool
	Now       time.Time

	Items   []Item
	Settled []Settled
	Next    string
	Item    int
	Draft   Draft
	Send    Send
	Counts  [3]int

	PRs       []PR
	Checked   time.Time
	Served    string
	Artifacts []ArtifactRow
	Activity  []ActivityRow
	All       bool
	Row       int
}
