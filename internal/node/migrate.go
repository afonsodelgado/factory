package node

import (
	"cmp"

	"github.com/afonsodelgado/factory/internal/phone"
	"github.com/afonsodelgado/factory/internal/state"
)

func Migrate(cfg *state.Config) (string, error) {
	store := phone.Open(cfg.StateDir)
	public, err := store.VAPIDKey(cfg.Phone.VAPIDPublicKey)
	if err != nil {
		return "", err
	}
	if err := store.Adopt(); err != nil {
		return "", err
	}
	if cfg.Phone == (state.Phone{}) {
		return public, nil
	}
	return public, state.UpdateMachine(cfg.ConfigDir, func(m *state.Machine) error {
		m.Node.Hostname = m.NodeHostname()
		m.Node.Port = cmp.Or(m.Node.Port, m.Phone.Port)
		m.Phone = state.Phone{}
		return nil
	})
}
