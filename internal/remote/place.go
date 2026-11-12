package remote

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/google/uuid"

	"github.com/afonsodelgado/factory/internal/state"
)

const (
	RefreshWait = 3 * time.Second
	CreateWait  = 15 * time.Minute
)

func Place(cfg *state.Config, id, remoteFlag string, local bool) (string, error) {
	if remoteFlag != "" && local {
		return "", errors.New("give --remote or --local, not both")
	}
	if remoteFlag != "" {
		if _, ok := cfg.Remotes[remoteFlag]; !ok {
			return "", fmt.Errorf("no remote is named %s; factory remote list shows them", remoteFlag)
		}
	}
	if id == "" {
		if remoteFlag != "" {
			return "", fmt.Errorf("name the factory to create on %s", remoteFlag)
		}
		return "", nil
	}
	here, elsewhere := Local(cfg, id), Holders(cfg, id)
	heldThere := slices.Contains(elsewhere, remoteFlag)
	switch {
	case local && len(elsewhere) > 0 && !here:
		return "", fmt.Errorf("%s runs on %s; open it with --remote %s, or pick another id", id, elsewhere[0], elsewhere[0])
	case local:
		return "", nil
	case remoteFlag != "" && heldThere:
		return remoteFlag, nil
	case remoteFlag != "" && here:
		return "", fmt.Errorf("%s runs on this machine; open it without --remote", id)
	case remoteFlag != "" && len(elsewhere) > 0:
		return "", fmt.Errorf("%s runs on %s; pick another id to create one on %s", id, elsewhere[0], remoteFlag)
	case remoteFlag != "":
		return remoteFlag, nil
	case here && len(elsewhere) > 0:
		return "", fmt.Errorf("%s exists on this machine and on %s; pick one with --local or --remote %s", id, strings.Join(elsewhere, " and "), elsewhere[0])
	case here:
		return "", nil
	case len(elsewhere) > 1:
		return "", fmt.Errorf("%s exists on %s; pick one with --remote", id, strings.Join(elsewhere, " and "))
	case len(elsewhere) == 1:
		return elsewhere[0], nil
	}
	return cfg.DefaultRemote, nil
}

func Local(cfg *state.Config, id string) bool {
	registry, err := state.LoadRegistry(cfg.RegistryPath())
	return err == nil && slices.ContainsFunc(registry.Factories, func(e state.Entry) bool { return e.Slug == id })
}

func Holders(cfg *state.Config, id string) []string {
	var names []string
	for _, name := range slices.Sorted(maps.Keys(cfg.Remotes)) {
		if cache, err := LoadCache(cfg.StateDir, name); err == nil && cache.Holds(id) {
			names = append(names, name)
		}
	}
	return names
}

func RefreshInventories(cfg *state.Config) {
	refreshInventories(cfg, slices.Collect(maps.Keys(cfg.Remotes)))
}

func RefreshWhereListed(cfg *state.Config, id string) {
	var listing []string
	for name := range cfg.Remotes {
		if cache, err := LoadCache(cfg.StateDir, name); err == nil && cache.Lists(id) {
			listing = append(listing, name)
		}
	}
	refreshInventories(cfg, listing)
}

func refreshInventories(cfg *state.Config, names []string) {
	ctx, cancel := context.WithTimeout(context.Background(), RefreshWait)
	defer cancel()
	var wg sync.WaitGroup
	for _, name := range names {
		client, err := For(cfg, name)
		if err != nil {
			continue
		}
		wg.Go(func() { client.Inventory(ctx) })
	}
	wg.Wait()
}

func WithCreationLock(cfg *state.Config, fn func() error) error {
	if err := os.MkdirAll(cfg.StateDir, 0o700); err != nil {
		return err
	}
	lock, err := os.OpenFile(filepath.Join(cfg.StateDir, "create.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	return fn()
}

func RefuseKnownID(cfg *state.Config, id string) error {
	RefreshInventories(cfg)
	if elsewhere := Holders(cfg, id); len(elsewhere) > 0 {
		return fmt.Errorf("%s already runs on %s; pick another id, or open that one with --remote %s", id, elsewhere[0], elsewhere[0])
	}
	return nil
}

func CreateOn(cfg *state.Config, name, repo, gitRemote string, create Create, say func(string)) error {
	client, err := For(cfg, name)
	if err != nil {
		return err
	}
	if create.GitURL, err = GitURL(repo, gitRemote); err != nil {
		return err
	}
	if create.Profile, err = readOptional(state.ProfilePath(repo)); err != nil {
		return err
	}
	if _, err := state.ReadProfile(repo); err != nil {
		return fmt.Errorf("%s: %w", state.ProfilePath(repo), err)
	}
	if create.Rules, err = readOptional(cfg.RulesPath()); err != nil {
		return err
	}
	reservation, err := reserve(cfg, name, create)
	if err != nil {
		return err
	}
	lostDelete, err := operationKey("delete", Target{ID: create.ID})
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), CreateWait)
	defer cancel()
	say(fmt.Sprintf("Creating %s on %s: the server clones or fetches %s first, which can take a while.", create.ID, name, create.GitURL))
	var listing Listing
	err = client.Mutate(ctx, "create", reservation.Op, create, &listing)
	if errors.Is(err, ErrUncertain) {
		return fmt.Errorf("%s on %s: %w", create.ID, name, err)
	}
	settle := UpdateCache(cfg.StateDir, name, func(c *Cache) error {
		c.Reservations = slices.DeleteFunc(c.Reservations, func(res Reservation) bool { return res.ID == create.ID })
		if err == nil {
			c.Inventory = append(slices.DeleteFunc(c.Inventory, func(l Listing) bool { return l.ID == create.ID }), listing)
			c.Pending = slices.DeleteFunc(c.Pending, func(p PendingOp) bool { return p.Key == lostDelete })
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("%s did not start on %s: %w", create.ID, name, err)
	}
	if settle != nil {
		return settle
	}
	say(fmt.Sprintf("%s runs on %s, cloned from %s. It keeps going when this machine sleeps.", create.ID, name, create.GitURL))
	return nil
}

func earlierCreation(cfg *state.Config, name, id string) error {
	cache, _ := LoadCache(cfg.StateDir, name)
	if !slices.ContainsFunc(cache.Inventory, func(l Listing) bool { return l.ID == id }) {
		return fmt.Errorf("an earlier creation of %s on %s, with another prompt or settings, has no answer yet; run that command again to see what happened", id, name)
	}
	err := UpdateCache(cfg.StateDir, name, func(c *Cache) error {
		c.Reservations = slices.DeleteFunc(c.Reservations, func(res Reservation) bool { return res.ID == id })
		return nil
	})
	return cmp.Or(err, fmt.Errorf("%s already runs on %s: the earlier attempt whose answer was lost created it, with that attempt's prompt and settings, not these. Open it with factory open %s", id, name, id))
}

func reserve(cfg *state.Config, name string, create Create) (Reservation, error) {
	digest, err := operationKey("create", create)
	if err != nil {
		return Reservation{}, err
	}
	var reservation Reservation
	err = WithCreationLock(cfg, func() error {
		RefreshInventories(cfg)
		if Local(cfg, create.ID) {
			return fmt.Errorf("%s runs on this machine; pick another id", create.ID)
		}
		if others := slices.DeleteFunc(Holders(cfg, create.ID), func(n string) bool { return n == name }); len(others) > 0 {
			return fmt.Errorf("%s already runs on %s; pick another id", create.ID, others[0])
		}
		return UpdateCache(cfg.StateDir, name, func(c *Cache) error {
			if i := slices.IndexFunc(c.Reservations, func(res Reservation) bool { return res.ID == create.ID }); i >= 0 {
				reservation = c.Reservations[i]
				return nil
			}
			reservation = Reservation{ID: create.ID, GitURL: create.GitURL, Digest: digest, Op: uuid.NewString(), At: time.Now()}
			c.Reservations = append(c.Reservations, reservation)
			return nil
		})
	})
	switch {
	case err != nil:
	case reservation.GitURL != create.GitURL:
		err = fmt.Errorf("a creation of %s from %s on %s has not finished; run it again from that repo to see what happened", create.ID, reservation.GitURL, name)
	case reservation.Digest != digest:
		err = earlierCreation(cfg, name, create.ID)
	}
	return reservation, err
}

func readOptional(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	return data, err
}
