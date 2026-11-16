package phone

import (
	"crypto/ecdh"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

const (
	CookieName     = "factory_device"
	CodeTTL        = 5 * time.Minute
	CredentialLife = 365 * 24 * time.Hour
	MaxDeviceName  = 64
	failureWindow  = 5 * time.Minute
	failuresKept   = 10
	devicesFile    = "devices.json"
	codesFile      = "codes.json"
	vapidFile      = "vapid.json"
)

var (
	ErrCodeSpent    = errors.New("This pairing code was used or has expired. Show a new one in the hub: Phone, then Pair.")
	ErrTooManyCodes = errors.New("Too many wrong pairing codes. Wait a few minutes, then try again.")
	ErrNoDevice     = errors.New("no paired device with that id")
)

type Device struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	PairedAt time.Time `json:"paired_at"`
	Hash     string    `json:"hash,omitempty"`
}

func (d Device) Public() Device {
	d.Hash = ""
	return d
}

type devices struct {
	Devices []Device `json:"devices"`
}

type code struct {
	Hash    string    `json:"hash"`
	Expires time.Time `json:"expires"`
}

type codes struct {
	Codes    []code      `json:"codes"`
	Failures []time.Time `json:"failures,omitempty"`
}

func randomBytes(n int) []byte {
	b := make([]byte, n)
	rand.Read(b)
	return b
}

func hashOf(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

func newCode() string {
	return strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(randomBytes(16)))
}

func (c *codes) live(now time.Time) {
	c.Codes = slices.DeleteFunc(c.Codes, func(k code) bool { return !now.Before(k.Expires) })
	c.Failures = slices.DeleteFunc(c.Failures, func(at time.Time) bool { return now.Sub(at) > failureWindow })
}

func (c *codes) find(given string) int {
	h := hashOf(given)
	return slices.IndexFunc(c.Codes, func(k code) bool { return subtle.ConstantTimeCompare([]byte(k.Hash), []byte(h)) == 1 })
}

func (s Store) NewCode(now time.Time) (string, time.Time, error) {
	given, expires := newCode(), now.Add(CodeTTL)
	err := update(s, codesFile, func(c *codes) error {
		c.live(now)
		c.Codes = append(c.Codes, code{Hash: hashOf(given), Expires: expires})
		return nil
	})
	return given, expires, err
}

func (s Store) CodeExpiry(given string, now time.Time) (time.Time, error) {
	var expires time.Time
	err := s.locked(func() error {
		var err error
		expires, err = s.takeCode(given, now, false)
		return err
	})
	return expires, err
}

func (s Store) Exchange(given, name string, now time.Time) (Device, string, error) {
	name = strings.TrimSpace(name)
	switch {
	case name == "":
		name = "Phone"
	case utf8.RuneCountInString(name) > MaxDeviceName:
		return Device{}, "", fmt.Errorf("a device name holds at most %d characters", MaxDeviceName)
	}
	var device Device
	var secret string
	err := s.locked(func() error {
		if _, err := s.takeCode(given, now, true); err != nil {
			return err
		}
		secret = base64.RawURLEncoding.EncodeToString(randomBytes(32))
		device = Device{ID: uuid.NewString(), Name: name, PairedAt: now.UTC(), Hash: hashOf(secret)}
		var d devices
		if err := s.read(devicesFile, &d); err != nil {
			return err
		}
		d.Devices = append(d.Devices, device)
		return s.write(devicesFile, d)
	})
	return device.Public(), secret, err
}

func (s Store) takeCode(given string, now time.Time, consume bool) (time.Time, error) {
	var c codes
	if err := s.read(codesFile, &c); err != nil {
		return time.Time{}, err
	}
	c.live(now)
	if len(c.Failures) >= failuresKept {
		return time.Time{}, ErrTooManyCodes
	}
	i := c.find(given)
	if i < 0 {
		c.Failures = append(c.Failures, now)
		if err := s.write(codesFile, c); err != nil {
			return time.Time{}, err
		}
		return time.Time{}, ErrCodeSpent
	}
	expires := c.Codes[i].Expires
	if consume {
		c.Codes = slices.Delete(c.Codes, i, i+1)
	}
	return expires, s.write(codesFile, c)
}

func (s Store) Devices() ([]Device, error) {
	var d devices
	if err := s.read(devicesFile, &d); err != nil {
		return nil, err
	}
	public := make([]Device, len(d.Devices))
	for i, device := range d.Devices {
		public[i] = device.Public()
	}
	return public, nil
}

func (s Store) Authenticate(secret string) (Device, bool) {
	var d devices
	if secret == "" || s.read(devicesFile, &d) != nil {
		return Device{}, false
	}
	h := hashOf(secret)
	for _, device := range d.Devices {
		if subtle.ConstantTimeCompare([]byte(device.Hash), []byte(h)) == 1 {
			return device.Public(), true
		}
	}
	return Device{}, false
}

func (s Store) Paired(id string) bool {
	devices, err := s.Devices()
	return err == nil && slices.ContainsFunc(devices, func(d Device) bool { return d.ID == id })
}

func (s Store) Revoke(id string) error {
	return update(s, devicesFile, func(d *devices) error {
		before := len(d.Devices)
		d.Devices = slices.DeleteFunc(d.Devices, func(device Device) bool { return device.ID == id })
		if len(d.Devices) == before {
			return ErrNoDevice
		}
		return nil
	})
}

type vapid struct {
	Private string `json:"private"`
}

func (s Store) VAPIDKey(configured string) (string, error) {
	var public string
	err := update(s, vapidFile, func(v *vapid) error {
		switch {
		case v.Private == "" && configured != "":
			return fmt.Errorf("config.yml names a VAPID public key, but its private key is missing from %s; phones subscribed with it cannot be reached. Remove phone.vapid_public_key to make a new pair, then pair the phones again", s.Dir)
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
