package node

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/afonsodelgado/factory/internal/state"
)

const (
	settingsBody = 1 << 20
	retryWrite   = 10 * time.Minute
)

type Setting struct {
	Key      string    `json:"key"`
	Content  string    `json:"content"`
	Revision int64     `json:"revision"`
	Author   string    `json:"author"`
	At       time.Time `json:"at"`
}

func (s Setting) newer(than Setting) bool {
	if s.Revision != than.Revision {
		return s.Revision > than.Revision
	}
	return s.Author > than.Author
}

type SettingsPush struct {
	From     string    `json:"from"`
	Settings []Setting `json:"settings"`
}

type Sources struct {
	Read  func() map[string]string
	Write func(ctx context.Context, key, content string) (string, error)
}

type Settings struct {
	Path    string
	Self    string
	Sources Sources
	Now     func() time.Time

	mu          sync.Mutex
	book        settingsBook
	load        sync.Once
	reconciling sync.Mutex
	failed      map[string]time.Time
}

type SettingsStatus struct {
	Records int      `json:"records"`
	Pending []string `json:"pending,omitempty"`
}

type settingsBook struct {
	Records map[string]Setting `json:"records"`
	Seen    map[string]string  `json:"seen"`
}

func SettingsPath(stateDir string) string { return filepath.Join(stateDir, "settings.json") }

func digest(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

func (s *Settings) opened() {
	s.load.Do(func() {
		data, err := os.ReadFile(s.Path)
		if err == nil {
			json.Unmarshal(data, &s.book)
		}
		if s.book.Records == nil {
			s.book.Records = map[string]Setting{}
		}
		if s.book.Seen == nil {
			s.book.Seen = map[string]string{}
		}
	})
}

func (s *Settings) save() error {
	data, err := json.Marshal(s.book)
	if err != nil {
		return err
	}
	return state.WriteFileAtomic(s.Path, data)
}

func (s *Settings) now() time.Time {
	if s.Now == nil {
		return time.Now()
	}
	return s.Now()
}

func (s *Settings) Records() []Setting {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.opened()
	return slices.Collect(maps.Values(s.book.Records))
}

func (s *Settings) Merge(records []Setting) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.opened()
	for _, r := range records {
		if current, ok := s.book.Records[r.Key]; !ok || r.newer(current) {
			s.book.Records[r.Key] = r
		}
	}
	s.save()
}

func (s *Settings) Join() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.opened()
	s.book = settingsBook{Records: map[string]Setting{}, Seen: map[string]string{}}
	s.save()
}

func (s *Settings) Settle(key, content string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.opened()
	if _, ok := s.book.Records[key]; !ok {
		s.book.Seen[key] = digest(content)
		s.save()
	}
}

type apply struct {
	key     string
	content string
}

func (s *Settings) Status() SettingsStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.opened()
	status := SettingsStatus{Records: len(s.book.Records)}
	for key, record := range s.book.Records {
		if digest(record.Content) != s.book.Seen[key] {
			status.Pending = append(status.Pending, key)
		}
	}
	slices.Sort(status.Pending)
	return status
}

func (s *Settings) Reconcile(ctx context.Context) bool {
	s.reconciling.Lock()
	defer s.reconciling.Unlock()
	authored, applies := s.plan(s.Sources.Read())
	for _, a := range applies {
		if at, ok := s.failed[a.key]; ok && s.now().Sub(at) < retryWrite {
			continue
		}
		written, err := s.Sources.Write(ctx, a.key, a.content)
		s.mu.Lock()
		if err != nil {
			if s.failed == nil {
				s.failed = map[string]time.Time{}
			}
			s.failed[a.key] = s.now()
		} else {
			delete(s.failed, a.key)
			s.book.Seen[a.key] = digest(written)
			s.save()
		}
		s.mu.Unlock()
	}
	return authored
}

func (s *Settings) plan(local map[string]string) (bool, []apply) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.opened()
	authored := false
	var applies []apply
	keys := slices.Sorted(maps.Keys(local))
	for key := range s.book.Records {
		if _, ok := local[key]; !ok {
			keys = append(keys, key)
		}
	}
	for _, key := range keys {
		content, present := local[key]
		record, known := s.book.Records[key]
		seen := s.book.Seen[key]
		h := digest(content)
		editedHere := present && h != seen && (seen != "" || !known)
		switch {
		case editedHere && known && digest(record.Content) == h:
			s.book.Seen[key] = h
		case editedHere:
			s.book.Records[key] = Setting{Key: key, Content: content, Revision: record.Revision + 1, Author: s.Self, At: s.now()}
			s.book.Seen[key] = h
			authored = true
		case known && digest(record.Content) != seen:
			applies = append(applies, apply{key: key, content: record.Content})
		}
	}
	s.save()
	return authored, applies
}

func (n *Nodes) sendSettings(ctx context.Context, m Member) {
	if n.Settings == nil {
		return
	}
	var theirs SettingsPush
	if err := Remote(m.Name, n.HTTP).Do(ctx, http.MethodPost, "/api/node/settings", SettingsPush{From: n.Self.ID, Settings: n.Settings.Records()}, &theirs); err != nil {
		return
	}
	n.Settings.Merge(theirs.Settings)
	n.Settings.Reconcile(ctx)
}

func (n *Nodes) settingsRoute(w http.ResponseWriter, r *http.Request, _ string) {
	var body SettingsPush
	if err := decode(r, &body); err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	if _, err := n.verifiedMember(r.Context(), body.From); err != nil {
		fail(w, http.StatusForbidden, err.Error())
		return
	}
	if n.Settings == nil {
		fail(w, http.StatusNotImplemented, "this node keeps no shared settings")
		return
	}
	n.Settings.Merge(body.Settings)
	n.Settings.Reconcile(r.Context())
	reply(w, http.StatusOK, SettingsPush{From: n.Self.ID, Settings: n.Settings.Records()})
}

func (n *Nodes) settingsStatusRoute(w http.ResponseWriter, r *http.Request, _ string) {
	if n.Settings == nil {
		reply(w, http.StatusOK, SettingsStatus{})
		return
	}
	reply(w, http.StatusOK, n.Settings.Status())
}
