package phone

import (
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
	resolvedBody = "Resolved elsewhere."
)

var notifiable = []string{"question", "gate", "comment", "cap", "failure", "permission", "handoff"}

type Link struct {
	Route       string `json:"route"`
	Item        string `json:"item"`
	Incarnation string `json:"incarnation"`
	Open        bool   `json:"open"`
}

type tracked struct {
	Link
	Title    string    `json:"title"`
	Alerted  bool      `json:"alerted,omitempty"`
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
	active bool
}

func alertsOf(factories []host.LocalFactory) (map[string]alert, map[string]bool) {
	open, active := map[string]alert{}, map[string]bool{}
	for _, f := range factories {
		active[f.Incarnation] = !f.Paused
		for _, item := range f.Items {
			if slices.Contains(notifiable, item.Kind) {
				link := Link{Route: f.Route, Item: item.ID, Incarnation: f.Incarnation, Open: true}
				open[item.Key] = alert{tracked: tracked{Link: link, Title: f.ID + " · " + item.Kind, Revision: 1}, active: !f.Paused}
			}
		}
	}
	return open, active
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
		open, active := alertsOf(factories)
		for key, a := range open {
			_, seen := l.Items[key]
			switch {
			case seen && !a.active:
				l.cancel(key)
			case !seen:
				item := a.tracked
				item.Changed = now
				l.Items[key] = &item
				if l.Baseline && a.active {
					item.Alerted = l.queue(node, subs, key, &item, nil, now)
					queued = item.Alerted || queued
				}
			}
		}
		for key, item := range l.Items {
			if _, still := open[key]; still || !item.Open {
				continue
			}
			item.Open, item.Revision, item.Changed = false, item.Revision+1, now
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
	payload := Payload{Node: node, Tag: notificationTag(node, key), Title: item.Title, Body: needsYou, URL: linkPath(node, key), Revision: item.Revision, Resolved: !item.Open}
	if payload.Resolved {
		payload.Body = resolvedBody
	}
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
