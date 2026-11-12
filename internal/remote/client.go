package remote

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/afonsodelgado/factory/internal/state"
)

var ErrUncertain = errors.New("outcome unknown; run the same command again once the remote answers, to see what happened")

type Client struct {
	Transport
}

func For(cfg *state.Config, name string) (Client, error) {
	r, ok := cfg.Remotes[name]
	if !ok {
		return Client{}, fmt.Errorf("no remote is named %s; factory remote list shows them", name)
	}
	return Client{Transport{Name: name, Remote: r, StateDir: cfg.StateDir}}, nil
}

type Cache struct {
	Server       string        `json:"server,omitempty"`
	Release      string        `json:"release,omitempty"`
	Inventory    []Listing     `json:"inventory,omitempty"`
	Reservations []Reservation `json:"reservations,omitempty"`
	Forgotten    []string      `json:"forgotten,omitempty"`
	CheckedAt    time.Time     `json:"checked_at,omitzero"`
}

type Reservation struct {
	ID     string    `json:"id"`
	GitURL string    `json:"git_url"`
	Op     string    `json:"op"`
	At     time.Time `json:"at"`
}

func (c Cache) Shown() []Listing {
	return slices.DeleteFunc(slices.Clone(c.Inventory), func(l Listing) bool { return slices.Contains(c.Forgotten, l.Incarnation) })
}

func (c Cache) Holds(id string) bool {
	return slices.ContainsFunc(c.Inventory, func(l Listing) bool { return l.ID == id }) ||
		slices.ContainsFunc(c.Reservations, func(r Reservation) bool { return r.ID == id })
}

func CachePath(stateDir, name string) string {
	return filepath.Join(stateDir, "remotes", name+".json")
}

func LoadCache(stateDir, name string) (Cache, error) {
	var c Cache
	data, err := os.ReadFile(CachePath(stateDir, name))
	if errors.Is(err, fs.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	return c, json.Unmarshal(data, &c)
}

func UpdateCache(stateDir, name string, change func(*Cache) error) error {
	path := CachePath(stateDir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	c, err := LoadCache(stateDir, name)
	if err != nil {
		return err
	}
	if err := change(&c); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return state.WriteFileAtomic(path, data)
}

func (c Client) Hello(ctx context.Context) (Hello, error) {
	var hello Hello
	data, err := c.call(ctx, "hello", "", nil)
	if err == nil {
		err = json.Unmarshal(data, &hello)
	}
	if err != nil {
		return hello, err
	}
	return hello, UpdateCache(c.StateDir, c.Name, func(cache *Cache) error {
		cache.Server, cache.Release = hello.Server, hello.Release
		return nil
	})
}

func (c Client) Inventory(ctx context.Context) ([]Listing, error) {
	var listings []Listing
	data, err := c.call(ctx, "inventory", "", nil)
	if err == nil {
		err = json.Unmarshal(data, &listings)
	}
	if err != nil {
		return nil, err
	}
	return listings, UpdateCache(c.StateDir, c.Name, func(cache *Cache) error {
		cache.Inventory, cache.CheckedAt = listings, time.Now()
		return nil
	})
}

func (c Client) Read(ctx context.Context, action string, payload, reply any) error {
	data, err := c.call(ctx, action, "", payload)
	if err != nil || reply == nil {
		return err
	}
	return json.Unmarshal(data, reply)
}

func (c Client) Mutate(ctx context.Context, action, op string, payload, reply any) error {
	if _, err := c.Hello(ctx); err != nil {
		return fmt.Errorf("%w. Nothing was sent", err)
	}
	data, err := c.call(ctx, action, op, payload)
	var unreachable *TransportError
	if errors.As(err, &unreachable) {
		return fmt.Errorf("%w (%v)", ErrUncertain, unreachable)
	}
	if err != nil || reply == nil {
		return err
	}
	return json.Unmarshal(data, reply)
}

func (c Client) call(ctx context.Context, action, op string, payload any) (json.RawMessage, error) {
	cache, err := LoadCache(c.StateDir, c.Name)
	if err != nil {
		return nil, err
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	request, err := json.Marshal(Request{Protocol: Protocol, Op: op, Server: cache.Server, Action: action, Data: data})
	if err != nil {
		return nil, err
	}
	out, err := c.Run(ctx, request, RequestCommand)
	if err != nil {
		return nil, err
	}
	var reply Reply
	if err := json.Unmarshal(out, &reply); err != nil {
		return nil, fmt.Errorf("%s answered with something other than factory: %s", c.Name, firstLine(out))
	}
	if reply.Protocol != Protocol {
		return nil, fmt.Errorf("%s speaks factory protocol %d and this machine %d; run factory update on both", c.Name, reply.Protocol, Protocol)
	}
	switch reply.Status {
	case Completed:
		return reply.Data, nil
	case Uncertain:
		return nil, ErrUncertain
	}
	return nil, errors.New(reply.Error)
}

func firstLine(out []byte) string {
	line, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	return line
}
