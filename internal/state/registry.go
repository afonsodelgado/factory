package state

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"time"
)

type Registry struct {
	path      string
	Factories []Entry `json:"factories"`
}

type Entry struct {
	Repo        string       `json:"repo"`
	Slug        string       `json:"slug"`
	Title       string       `json:"title,omitempty"`
	SessionID   string       `json:"session_id,omitempty"`
	CreatedAt   time.Time    `json:"created_at"`
	Created     bool         `json:"created,omitempty"`
	Archived    bool         `json:"archived,omitempty"`
	Dir         string       `json:"dir,omitempty"`
	Supervisor  AgentSetting `json:"supervisor,omitzero"`
	Branches    []string     `json:"branches,omitempty"`
	Incarnation string       `json:"incarnation,omitempty"`

	Agents map[string]AgentSetting `json:"agents,omitempty"`
}

func (e Entry) Key() string {
	return Key(e.Repo, e.Slug)
}

func Key(repo, slug string) string {
	return repo + ":" + slug
}

func LoadRegistry(path string) (*Registry, error) {
	r := &Registry{path: path}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return r, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, r); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *Registry) Find(repo, slug string) *Entry {
	for i := range r.Factories {
		if r.Factories[i].Repo == repo && r.Factories[i].Slug == slug {
			return &r.Factories[i]
		}
	}
	return nil
}

func (r *Registry) FindDir(repo, dir string) *Entry {
	for i := range r.Factories {
		if r.Factories[i].Repo == repo && r.Factories[i].Dir == dir {
			return &r.Factories[i]
		}
	}
	return nil
}

func (r *Registry) Upsert(e Entry) *Entry {
	if existing := r.Find(e.Repo, e.Slug); existing != nil {
		*existing = e
		return existing
	}
	r.Factories = append(r.Factories, e)
	return &r.Factories[len(r.Factories)-1]
}

func (r *Registry) Remove(match func(Entry) bool) int {
	before := len(r.Factories)
	r.Factories = slices.DeleteFunc(r.Factories, match)
	return before - len(r.Factories)
}

func (r *Registry) Observe(repo, slug string, seen time.Time) (*Entry, bool) {
	if existing := r.Find(repo, slug); existing != nil {
		return existing, false
	}
	return r.Upsert(Entry{Repo: repo, Slug: slug, CreatedAt: seen}), true
}

func (r *Registry) Save() error {
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	return WriteFileAtomic(r.path, data)
}

func WriteFileAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), path)
}
