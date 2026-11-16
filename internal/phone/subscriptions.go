package phone

import (
	"encoding/base64"
	"errors"
	"net/url"
	"slices"
	"strings"
	"time"
)

const subscriptionsFile = "subscriptions.json"

type Subscription struct {
	Device   string    `json:"device"`
	Endpoint string    `json:"endpoint"`
	P256dh   string    `json:"p256dh"`
	Auth     string    `json:"auth"`
	Since    time.Time `json:"since"`
	LastSent time.Time `json:"last_sent,omitzero"`
	Problem  string    `json:"problem,omitempty"`
}

type subscriptions struct {
	Subscriptions []Subscription `json:"subscriptions"`
}

func (s Store) Subscriptions() ([]Subscription, error) {
	var subs subscriptions
	err := s.read(subscriptionsFile, &subs)
	return subs.Subscriptions, err
}

func (s Store) SubscriptionOf(device string) (Subscription, bool) {
	subs, _ := s.Subscriptions()
	i := slices.IndexFunc(subs, func(sub Subscription) bool { return sub.Device == device })
	if i < 0 {
		return Subscription{}, false
	}
	return subs[i], true
}

func (s Store) Subscribe(sub Subscription) error {
	sub.P256dh, sub.Auth = strings.TrimRight(sub.P256dh, "="), strings.TrimRight(sub.Auth, "=")
	if err := validSubscription(sub); err != nil {
		return err
	}
	return update(s, subscriptionsFile, func(subs *subscriptions) error {
		subs.Subscriptions = append(withoutDevice(subs.Subscriptions, sub.Device), sub)
		return nil
	})
}

func (s Store) Unsubscribe(device string) error {
	if err := update(s, subscriptionsFile, func(subs *subscriptions) error {
		subs.Subscriptions = withoutDevice(subs.Subscriptions, device)
		return nil
	}); err != nil {
		return err
	}
	return s.dropJobs(device)
}

func withoutDevice(subs []Subscription, device string) []Subscription {
	return slices.DeleteFunc(subs, func(sub Subscription) bool { return sub.Device == device })
}

func (s Store) recordDelivery(device, endpoint string, at time.Time, problem string) error {
	return update(s, subscriptionsFile, func(subs *subscriptions) error {
		for i := range subs.Subscriptions {
			if sub := &subs.Subscriptions[i]; sub.Device == device && sub.Endpoint == endpoint {
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
