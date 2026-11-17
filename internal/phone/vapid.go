package phone

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"fmt"
)

const vapidFile = "vapid.json"

type vapid struct {
	Private string `json:"private"`
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

type vapidKeys struct{ public, private string }

func (s Store) vapidPair() (vapidKeys, error) {
	var v vapid
	if err := s.read(vapidFile, &v); err != nil {
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
