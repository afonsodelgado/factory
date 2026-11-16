package phone

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io/fs"
	"net/url"
	"os"
	"slices"
	"strings"
	"time"
)

const (
	subscriptionsFile = "subscriptions.json"
	storeVersion      = 2
)

var pairingFiles = []string{"devices.json", "codes.json"}

type Subscription struct {
	ID           string    `json:"id"`
	Endpoint     string    `json:"endpoint"`
	P256dh       string    `json:"p256dh"`
	Auth         string    `json:"auth"`
	Installation string    `json:"installation,omitempty"`
	Since        time.Time `json:"since"`
	Revision     int64     `json:"revision,omitempty"`
	Removed      bool      `json:"removed,omitempty"`
	LastSent     time.Time `json:"last_sent,omitzero"`
	Problem      string    `json:"problem,omitempty"`
}

type subscriptions struct {
	Version       int            `json:"version"`
	Subscriptions []Subscription `json:"subscriptions"`
}

func subscriptionID(endpoint string) string {
	sum := sha256.Sum256([]byte(endpoint))
	return hex.EncodeToString(sum[:8])
}

func nextRevision(at time.Time, current int64) int64 {
	return max(at.UnixNano(), current+1)
}

func (s Store) Adopt() error {
	if _, err := s.Promote(); err != nil {
		return err
	}
	return s.locked(func() error {
		var current subscriptions
		if err := s.read(subscriptionsFile, &current); err != nil {
			return err
		}
		if current.Version < storeVersion {
			adopted := subscriptions{Version: storeVersion, Subscriptions: []Subscription{}}
			for _, sub := range current.Subscriptions {
				sub.ID = subscriptionID(sub.Endpoint)
				adopted.Subscriptions = append(withoutEndpoint(adopted.Subscriptions, sub.Endpoint), sub)
			}
			if err := s.write(subscriptionsFile, adopted); err != nil {
				return err
			}
		}
		for _, name := range pairingFiles {
			if err := os.Remove(s.path(name)); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return err
			}
		}
		return nil
	})
}

func (s Store) records() ([]Subscription, error) {
	var subs subscriptions
	err := s.read(subscriptionsFile, &subs)
	return subs.Subscriptions, err
}

func (s Store) Subscriptions() ([]Subscription, error) {
	subs, err := s.records()
	return slices.DeleteFunc(subs, func(sub Subscription) bool { return sub.Removed }), err
}

func first(subs []Subscription, match func(Subscription) bool) (Subscription, bool) {
	i := slices.IndexFunc(subs, match)
	if i < 0 {
		return Subscription{}, false
	}
	return subs[i], true
}

func at(endpoint string) func(Subscription) bool {
	return func(sub Subscription) bool { return sub.Endpoint == endpoint }
}

func (s Store) SubscriptionAt(endpoint string) (Subscription, bool) {
	subs, _ := s.Subscriptions()
	return first(subs, at(endpoint))
}

func (s Store) SubscriptionOf(installation string) (Subscription, bool) {
	subs, _ := s.Subscriptions()
	return first(subs, func(sub Subscription) bool { return installation != "" && sub.Installation == installation })
}

func (s Store) Subscribe(sub Subscription) (Subscription, error) {
	sub.P256dh, sub.Auth = strings.TrimRight(sub.P256dh, "="), strings.TrimRight(sub.Auth, "=")
	if err := validSubscription(sub); err != nil {
		return Subscription{}, err
	}
	sub.ID = subscriptionID(sub.Endpoint)
	var replaced []string
	err := update(s, subscriptionsFile, func(subs *subscriptions) error {
		known, seen := first(subs.Subscriptions, at(sub.Endpoint))
		sub.Revision = nextRevision(sub.Since, known.Revision)
		if seen && !known.Removed {
			sub.Since, sub.LastSent, sub.Problem = known.Since, known.LastSent, known.Problem
		}
		for i, other := range subs.Subscriptions {
			renewed := !other.Removed && other.Endpoint != sub.Endpoint && sub.Installation != "" && other.Installation == sub.Installation
			if renewed {
				replaced = append(replaced, other.Endpoint)
				subs.Subscriptions[i] = tombstone(other, nextRevision(sub.Since, other.Revision))
			}
		}
		subs.Subscriptions = slices.DeleteFunc(append(withoutEndpoint(subs.Subscriptions, sub.Endpoint), sub), forgotten(sub.Since))
		subs.Version = storeVersion
		return nil
	})
	if err != nil {
		return Subscription{}, err
	}
	return sub, s.dropJobsWhere(func(j job) bool { return slices.Contains(replaced, j.Endpoint) })
}

func tombstone(sub Subscription, revision int64) Subscription {
	return Subscription{ID: sub.ID, Endpoint: sub.Endpoint, Installation: sub.Installation, Since: sub.Since, Revision: revision, Removed: true}
}

func (s Store) Unsubscribe(endpoint string, at time.Time) error {
	return s.retire(endpoint, func(sub Subscription) (int64, bool) { return nextRevision(at, sub.Revision), true })
}

func (s Store) expire(endpoint string, revision int64) error {
	return s.retire(endpoint, func(sub Subscription) (int64, bool) { return revision + 1, sub.Revision == revision })
}

func (s Store) retire(endpoint string, retirement func(Subscription) (int64, bool)) error {
	retired := false
	if err := update(s, subscriptionsFile, func(subs *subscriptions) error {
		for i, sub := range subs.Subscriptions {
			if revision, applies := retirement(sub); sub.Endpoint == endpoint && !sub.Removed && applies {
				subs.Subscriptions[i], retired = tombstone(sub, revision), true
			}
		}
		return nil
	}); err != nil || !retired {
		return err
	}
	return s.dropJobsWhere(func(j job) bool { return j.Endpoint == endpoint })
}

func withoutEndpoint(subs []Subscription, endpoint string) []Subscription {
	return slices.DeleteFunc(subs, at(endpoint))
}

func (s Store) recordDelivery(endpoint string, at time.Time, problem string) error {
	return update(s, subscriptionsFile, func(subs *subscriptions) error {
		for i := range subs.Subscriptions {
			if sub := &subs.Subscriptions[i]; sub.Endpoint == endpoint {
				sub.Problem = problem
				if problem == "" {
					sub.LastSent = at
				}
			}
		}
		return nil
	})
}

var ErrOtherKey = errors.New("this node sends with another push key, so it takes no subscriptions made for that key")

type Replica struct {
	Key     string         `json:"key"`
	Records []Subscription `json:"records"`
}

func (s Store) Replica() (Replica, error) {
	keys, err := s.vapidPair()
	if err != nil || keys.private == "" {
		return Replica{}, err
	}
	records, err := s.records()
	return Replica{Key: Fingerprint(keys.public), Records: records}, err
}

func (s Store) Merge(r Replica, now time.Time) error {
	keys, err := s.vapidPair()
	switch {
	case err != nil:
		return err
	case keys.private == "" || Fingerprint(keys.public) != r.Key:
		return ErrOtherKey
	}
	var removed []string
	if err := update(s, subscriptionsFile, func(subs *subscriptions) error {
		subs.Subscriptions, removed = merged(subs.Subscriptions, r.Records)
		subs.Subscriptions = slices.DeleteFunc(subs.Subscriptions, forgotten(now))
		subs.Version = storeVersion
		return nil
	}); err != nil {
		return err
	}
	return s.dropJobsWhere(func(j job) bool { return slices.Contains(removed, j.Endpoint) })
}

func merged(mine, theirs []Subscription) ([]Subscription, []string) {
	var removed []string
	for _, record := range theirs {
		known, seen := first(mine, at(record.Endpoint))
		revivesATie := known.Revision == record.Revision && known.Removed && !record.Removed
		if seen && known.Revision >= record.Revision && !revivesATie {
			continue
		}
		if record.Removed {
			removed = append(removed, record.Endpoint)
		} else if validSubscription(record) != nil {
			continue
		}
		record.ID, record.LastSent, record.Problem = subscriptionID(record.Endpoint), known.LastSent, known.Problem
		mine = append(withoutEndpoint(mine, record.Endpoint), record)
	}
	return mine, removed
}

func forgotten(now time.Time) func(Subscription) bool {
	return func(sub Subscription) bool {
		return sub.Removed && now.Sub(time.Unix(0, sub.Revision)) > resolvedKept
	}
}

func (s Store) Disable() error {
	records, err := s.records()
	var l ledger
	if err == nil {
		err = s.read(pushFile, &l)
	}
	if err != nil || len(records)+len(l.Jobs) == 0 {
		return err
	}
	if err := update(s, subscriptionsFile, func(subs *subscriptions) error {
		subs.Subscriptions = []Subscription{}
		subs.Version = storeVersion
		return nil
	}); err != nil {
		return err
	}
	return s.dropJobsWhere(func(job) bool { return true })
}

func validSubscription(sub Subscription) error {
	endpoint, err := url.Parse(sub.Endpoint)
	key, keyErr := base64.RawURLEncoding.DecodeString(sub.P256dh)
	auth, authErr := base64.RawURLEncoding.DecodeString(sub.Auth)
	switch {
	case err != nil || endpoint.Scheme != "https" || endpoint.Hostname() == "" || endpoint.User != nil:
		return errors.New("the push endpoint must be an https URL")
	case keyErr != nil || len(key) != 65 || key[0] != 4:
		return errors.New("the push subscription's p256dh key is not an uncompressed P-256 key")
	case authErr != nil || len(auth) != 16:
		return errors.New("the push subscription's auth secret must be 16 bytes")
	}
	return nil
}
