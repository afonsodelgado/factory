package phone

import (
	"crypto/ecdh"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"time"
)

const (
	vapidFile  = "vapid.json"
	stagedFile = "staged-key.json"
)

type vapid struct {
	Private  string    `json:"private"`
	Conflict *Conflict `json:"conflict,omitempty"`
}

type Conflict struct {
	Key   string    `json:"key"`
	Since time.Time `json:"since"`
}

type KeyState struct {
	Public      string `json:"public,omitempty"`
	Fingerprint string `json:"fingerprint,omitempty"`
	Phones      int    `json:"phones"`
	Conflict    string `json:"conflict,omitempty"`
}

type Shared struct {
	Private       string         `json:"private"`
	Subscriptions []Subscription `json:"subscriptions"`
}

type staged struct {
	Shared
	Replace bool      `json:"replace,omitempty"`
	At      time.Time `json:"at"`
}

func Fingerprint(public string) string {
	raw, _ := base64.RawURLEncoding.DecodeString(public)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:6])
}

func (s Store) VAPIDKey(configured string) (string, error) {
	var public string
	err := update(s, vapidFile, func(v *vapid) error {
		switch {
		case v.Private == "" && configured != "":
			return fmt.Errorf("config.yml names a VAPID public key, but its private key is missing from %s; phones subscribed with it cannot be reached. Remove phone.vapid_public_key to make a new pair, then turn notifications on again on each phone", s.Dir)
		case v.Private == "":
			key, err := ecdh.P256().GenerateKey(rand.Reader)
			if err != nil {
				return err
			}
			v.Private = base64.RawURLEncoding.EncodeToString(key.Bytes())
		}
		derived, err := publicOf(v.Private)
		if err != nil {
			return err
		}
		if configured != "" && configured != derived {
			return fmt.Errorf("phone.vapid_public_key in config.yml does not match the private key in %s; restore the original key rather than make a new one, so subscribed phones keep working", s.Dir)
		}
		public = derived
		return nil
	})
	return public, err
}

func (s Store) KeyState() (KeyState, error) {
	var v vapid
	if err := s.read(vapidFile, &v); err != nil || v.Private == "" {
		return KeyState{}, err
	}
	public, err := publicOf(v.Private)
	if err != nil {
		return KeyState{}, err
	}
	subs, err := s.Subscriptions()
	state := KeyState{Public: public, Fingerprint: Fingerprint(public), Phones: len(subs)}
	if v.Conflict != nil {
		state.Conflict = v.Conflict.Key
	}
	return state, err
}

var ErrNoSharedKey = errors.New("this node kept its own push key when it joined, so it holds no shared key to give; run factory node push-key import --replace here with a key exported from a node that notifies your phone")

func (s Store) Share() (Shared, error) {
	var v vapid
	if err := s.read(vapidFile, &v); err != nil {
		return Shared{}, err
	}
	if v.Conflict != nil {
		return Shared{}, ErrNoSharedKey
	}
	return s.Export()
}

func (s Store) Export() (Shared, error) {
	var v vapid
	if err := s.read(vapidFile, &v); err != nil {
		return Shared{}, err
	}
	if v.Private == "" {
		return Shared{}, errors.New("this node has no push key yet; start it with factory node run")
	}
	records, err := s.records()
	return Shared{Private: v.Private, Subscriptions: records}, err
}

func (sh Shared) Fingerprint() string {
	public, _ := publicOf(sh.Private)
	return Fingerprint(public)
}

func (s Store) Stage(shared Shared, replace bool, at time.Time) error {
	if _, err := publicOf(shared.Private); err != nil {
		return fmt.Errorf("the push key is not a P-256 private key: %w", err)
	}
	return s.write(stagedFile, staged{Shared: shared, Replace: replace, At: at})
}

func (s Store) Unstage() error {
	if err := os.Remove(s.path(stagedFile)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

func (s Store) Promote() (KeyState, error) {
	err := s.locked(func() error {
		var st staged
		if err := s.read(stagedFile, &st); err != nil || st.Private == "" {
			return err
		}
		if err := s.take(st); err != nil {
			return err
		}
		return s.Unstage()
	})
	if err != nil {
		return KeyState{}, err
	}
	return s.KeyState()
}

func (s Store) take(st staged) error {
	var v vapid
	var subs subscriptions
	var l ledger
	for name, into := range map[string]any{vapidFile: &v, subscriptionsFile: &subs, pushFile: &l} {
		if err := s.read(name, into); err != nil {
			return err
		}
	}
	incoming, _ := publicOf(st.Private)
	current, _ := publicOf(v.Private)
	sameKey := v.Private != "" && current == incoming
	active := len(subs.Subscriptions) - countRemoved(subs.Subscriptions)
	if !sameKey && active == 0 && !st.Replace {
		st.Replace = true
		if err := s.write(stagedFile, st); err != nil {
			return err
		}
	}
	switch {
	case st.Replace:
		l.Jobs = nil
		subs.Subscriptions, _ = merged([]Subscription{}, st.Subscriptions)
		v = vapid{Private: st.Private}
	case sameKey:
		var removed []string
		subs.Subscriptions, removed = merged(subs.Subscriptions, st.Subscriptions)
		l.Jobs = deleteJobs(l.Jobs, removed)
		v.Conflict = nil
	default:
		v.Conflict = &Conflict{Key: Fingerprint(incoming), Since: st.At}
		return s.write(vapidFile, v)
	}
	subs.Version = storeVersion
	if err := s.write(pushFile, l); err != nil {
		return err
	}
	if err := s.write(subscriptionsFile, subs); err != nil {
		return err
	}
	return s.write(vapidFile, v)
}

func (s Store) Refuse(key string, at time.Time) error {
	return update(s, vapidFile, func(v *vapid) error {
		public, _ := publicOf(v.Private)
		if v.Private != "" && Fingerprint(public) != key {
			v.Conflict = &Conflict{Key: key, Since: at}
		}
		return nil
	})
}

func countRemoved(subs []Subscription) int {
	n := 0
	for _, sub := range subs {
		if sub.Removed {
			n++
		}
	}
	return n
}

type vapidKeys struct{ public, private string }

func (s Store) vapidPair() (vapidKeys, error) {
	var v vapid
	if err := s.read(vapidFile, &v); err != nil || v.Private == "" {
		return vapidKeys{}, err
	}
	public, err := publicOf(v.Private)
	return vapidKeys{public: public, private: v.Private}, err
}

func publicOf(private string) (string, error) {
	raw, err := base64.RawURLEncoding.DecodeString(private)
	if err != nil {
		return "", err
	}
	key, err := ecdh.P256().NewPrivateKey(raw)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes()), nil
}

func randomBytes(n int) []byte {
	b := make([]byte, n)
	rand.Read(b)
	return b
}
