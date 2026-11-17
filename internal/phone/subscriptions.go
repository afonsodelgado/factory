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

func (s Store) Adopt() error {
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

func (s Store) Subscriptions() ([]Subscription, error) {
	var subs subscriptions
	err := s.read(subscriptionsFile, &subs)
	return subs.Subscriptions, err
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
		if known, ok := first(subs.Subscriptions, at(sub.Endpoint)); ok {
			sub.Since, sub.LastSent, sub.Problem = known.Since, known.LastSent, known.Problem
		}
		renewed := func(other Subscription) bool {
			return other.Endpoint != sub.Endpoint && sub.Installation != "" && other.Installation == sub.Installation
		}
		for _, other := range subs.Subscriptions {
			if renewed(other) {
				replaced = append(replaced, other.Endpoint)
			}
		}
		subs.Subscriptions = append(slices.DeleteFunc(withoutEndpoint(subs.Subscriptions, sub.Endpoint), renewed), sub)
		subs.Version = storeVersion
		return nil
	})
	if err != nil {
		return Subscription{}, err
	}
	return sub, s.dropJobsWhere(func(j job) bool { return slices.Contains(replaced, j.Endpoint) })
}

func (s Store) Unsubscribe(endpoint string) error {
	if err := update(s, subscriptionsFile, func(subs *subscriptions) error {
		subs.Subscriptions = withoutEndpoint(subs.Subscriptions, endpoint)
		return nil
	}); err != nil {
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
