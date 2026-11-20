package phone

import (
	"cmp"
	"encoding/json"
	"net/url"
	"slices"
	"time"

	"github.com/google/uuid"

	"github.com/afonsodelgado/factory/internal/host"
)

const (
	pushFile     = "push.json"
	resolvedKept = 30 * 24 * time.Hour
	needsYou     = "Needs you. Tap to open it."
	workDone     = "Work is done. Mark it done or continue working?"
	resolvedBody = "Resolved elsewhere."
	workingBody  = "Working on your comments. You are told when it is ready to review again."
	revisedBody  = "Revised from your comments. Tap to review it again."
)

var notifiable = []string{"question", "gate", "design_return", "comment", "cap", "failure", "permission", "prompt", "silent", "asleep", "stack", "unreadable", "handoff", "commands"}

type Link struct {
	Route       string `json:"route"`
	Item        string `json:"item"`
	Incarnation string `json:"incarnation"`
	Open        bool   `json:"open"`
}

type tracked struct {
	Link
	Title    string    `json:"title"`
	Body     string    `json:"body,omitempty"`
	Alerted  bool      `json:"alerted,omitempty"`
	Owed     bool      `json:"owed,omitempty"`
	Revision int64     `json:"revision"`
	Changed  time.Time `json:"changed"`
}

type job struct {
	ID       string    `json:"id"`
	Device   string    `json:"device,omitempty"`
	Endpoint string    `json:"endpoint"`
	Revision int64     `json:"revision,omitempty"`
	Tag      string    `json:"tag"`
	Resolved bool      `json:"resolved,omitempty"`
	Payload  string    `json:"payload"`
	Attempts int       `json:"attempts,omitempty"`
	Due      time.Time `json:"due"`
	Sending  bool      `json:"sending,omitempty"`
}

type ledger struct {
	Baseline bool                `json:"baseline"`
	Items    map[string]*tracked `json:"items"`
	Jobs     []job               `json:"jobs"`
}

type Payload struct {
	Node     string `json:"node"`
	Tag      string `json:"tag"`
	Title    string `json:"title"`
	Body     string `json:"body"`
	URL      string `json:"url"`
	Revision int64  `json:"revision"`
	Resolved bool   `json:"resolved,omitempty"`
}

type alert struct {
	tracked
	active  bool
	reopens bool
}

func alertsOf(factories []host.LocalFactory) (open map[string]alert, active, working map[string]bool) {
	open, active, working = map[string]alert{}, map[string]bool{}, map[string]bool{}
	for _, f := range factories {
		active[f.Incarnation] = !f.Paused
		for _, item := range f.Items {
			if item.Revising != "" {
				working[item.Key] = true
				continue
			}
			if item.Answer != nil && item.Answer.Status == host.StatusDismissed {
				continue
			}
			if slices.Contains(notifiable, item.Kind) {
				link := Link{Route: f.Route, Item: item.ID, Incarnation: f.Incarnation, Open: true}
				open[item.Key] = alert{tracked: tracked{Link: link, Title: f.ID + " · " + kindWord(item.Kind), Body: alertBody(item.Kind), Revision: 1}, active: !f.Paused, reopens: item.Kind == "gate"}
			}
		}
	}
	return open, active, working
}

var kindWords = map[string]string{"prompt": "waits at a prompt", "silent": "not reporting", "asleep": "stopped", "stack": "review stack failed", "unreadable": "state unreadable", "design_return": "design question"}

func kindWord(kind string) string {
	if word, ok := kindWords[kind]; ok {
		return word
	}
	return kind
}

func alertBody(kind string) string {
	if kind == "handoff" {
		return workDone
	}
	return needsYou
}

func (s Store) Reconcile(node string, factories []host.LocalFactory, connected func(installation string) bool, now time.Time) (bool, error) {
	subs, err := s.Subscriptions()
	if err != nil {
		return false, err
	}
	queued := false
	err = update(s, pushFile, func(l *ledger) error {
		if l.Items == nil {
			l.Items = map[string]*tracked{}
		}
		open, active, working := alertsOf(factories)
		for key, a := range open {
			known, seen := l.Items[key]
			switch {
			case seen && !a.active:
				l.cancel(key)
				known.Owed = false
			case seen && known.Open && known.Owed:
				known.Alerted = l.queue(node, subs, key, known, nil, now)
				known.Owed = !known.Alerted
				queued = known.Alerted || queued
			case seen && !known.Open && a.reopens:
				known.Open, known.Body, known.Revision, known.Changed = true, revisedBody, known.Revision+1, now
				if l.Baseline {
					known.Alerted = l.queue(node, subs, key, known, nil, now)
					queued = known.Alerted || queued
				}
			case !seen:
				item := a.tracked
				item.Changed = now
				l.Items[key] = &item
				if l.Baseline {
					item.Alerted = a.active && l.queue(node, subs, key, &item, nil, now)
					item.Owed = a.active && !item.Alerted
					queued = item.Alerted || queued
				}
			}
		}
		for key, item := range l.Items {
			if _, still := open[key]; still || !item.Open {
				continue
			}
			item.Open, item.Body, item.Revision, item.Changed = false, resolvedBody, item.Revision+1, now
			if working[key] {
				item.Body = workingBody
			}
			unsent := l.cancel(key)
			if active[item.Incarnation] && item.Alerted {
				clearsItself := func(sub Subscription) bool { return unsent[sub.Endpoint] || connected(sub.Installation) }
				queued = l.queue(node, subs, key, item, clearsItself, now) || queued
			}
		}
		for key, item := range l.Items {
			if !item.Open && now.Sub(item.Changed) > resolvedKept {
				delete(l.Items, key)
			}
		}
		l.Baseline = true
		return nil
	})
	return queued, err
}

func notificationTag(node, key string) string { return node + "/" + key }

func linkPath(node, key string) string {
	return "/n/" + url.PathEscape(node) + "/" + url.PathEscape(key)
}

func (l *ledger) queue(node string, subs []Subscription, key string, item *tracked, skip func(Subscription) bool, now time.Time) bool {
	payload := Payload{Node: node, Tag: notificationTag(node, key), Title: item.Title, Body: cmp.Or(item.Body, needsYou), URL: linkPath(node, key), Revision: item.Revision, Resolved: !item.Open}
	data, _ := json.Marshal(payload)
	queued := false
	for _, sub := range subs {
		if skip != nil && skip(sub) {
			continue
		}
		l.Jobs = append(l.Jobs, job{ID: uuid.NewString(), Endpoint: sub.Endpoint, Revision: sub.Revision, Tag: key, Resolved: payload.Resolved, Payload: string(data), Due: now})
		queued = true
	}
	return queued
}

func (l *ledger) cancel(key string) map[string]bool {
	unsent := map[string]bool{}
	l.Jobs = slices.DeleteFunc(l.Jobs, func(j job) bool {
		if j.Tag != key {
			return false
		}
		if !j.Sending && !j.Resolved {
			unsent[j.Endpoint] = true
		}
		return true
	})
	return unsent
}

func (s Store) Link(key string) (Link, bool) {
	var l ledger
	if s.read(pushFile, &l) != nil || l.Items[key] == nil {
		return Link{}, false
	}
	return l.Items[key].Link, true
}

func deleteJobs(jobs []job, endpoints []string) []job {
	return slices.DeleteFunc(jobs, func(j job) bool { return slices.Contains(endpoints, j.Endpoint) })
}

func (s Store) dropJobsWhere(drop func(job) bool) error {
	return update(s, pushFile, func(l *ledger) error {
		l.Jobs = slices.DeleteFunc(l.Jobs, drop)
		return nil
	})
}
