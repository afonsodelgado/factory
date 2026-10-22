package panel

import (
	"time"

	"github.com/afonsodelgado/factory/internal/factory"
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

type Question struct {
	Text        string
	Options     []string
	Recommended string
}

func (q Question) RecommendedIndex() int {
	for i, o := range q.Options {
		if o == q.Recommended {
			return i
		}
	}
	return -1
}

type Item struct {
	ID          string
	Kind        string
	Title       string
	Body        string
	Questions   []Question
	Artifacts   []ArtifactRow
	ArtifactURL string
	ThreadID    string
	Stage       string
	Status      factory.Status
	Since       time.Time
	Detail      []string
	Action      string
}

func (i Item) Answerable() bool {
	switch i.Kind {
	case "question", "gate", "comment", "cap", "failure":
		return true
	}
	return false
}

type Draft struct {
	Choice   []int    `json:"choice"`
	Typed    []string `json:"typed"`
	Question int      `json:"question"`
	Text     string   `json:"text"`
	Artifact int      `json:"artifact"`
	Editing  bool     `json:"-"`
}

func (d *Draft) Fit(questions int) {
	for len(d.Choice) < questions {
		d.Choice = append(d.Choice, -1)
	}
	for len(d.Typed) < questions {
		d.Typed = append(d.Typed, "")
	}
	d.Question = max(0, min(d.Question, questions-1))
}

func (d Draft) Typing(q Question, i int) bool {
	return i < len(d.Choice) && d.Choice[i] == len(q.Options)
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

type PR struct {
	Number  int
	Title   string
	Draft   bool
	URL     string
	CI      string
	Done    int
	Total   int
	Loaded  bool
	Problem string
}

type ArtifactRow struct {
	Name        string
	Title       string
	URL         string
	OpenThreads *int
	CheckedAt   time.Time
}

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
	SentAt  time.Time
}

type View struct {
	Label     string
	Tab       Tab
	Focused   bool
	Collapsed bool
	Now       time.Time

	Items  []Item
	Item   int
	Draft  Draft
	Send   Send
	Counts [3]int

	PRs       []PR
	Checked   time.Time
	Artifacts []ArtifactRow
	Activity  []ActivityRow
	All       bool
	Row       int
}
