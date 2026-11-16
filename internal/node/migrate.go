package node

import (
	"cmp"

	"github.com/afonsodelgado/factory/internal/phone"
	"github.com/afonsodelgado/factory/internal/state"
)

func Migrate(cfg *state.Config) error {
	store := phone.Open(cfg.StateDir)
	if _, err := store.VAPIDKey(cfg.Phone.VAPIDPublicKey); err != nil {
		return err
	}
	if err := store.Adopt(); err != nil {
		return err
	}
	if cfg.Phone == (state.Phone{}) {
		return nil
	}
	return state.UpdateMachine(cfg.ConfigDir, func(m *state.Machine) error {
		m.Node.Hostname = m.NodeHostname()
		m.Node.Port = cmp.Or(m.Node.Port, m.Phone.Port)
		m.Phone = state.Phone{}
		return nil
	})
}
