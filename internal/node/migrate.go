package node

import (
	"cmp"
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/google/uuid"

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

func ID(stateDir string) (string, error) {
	path := filepath.Join(stateDir, "server-id")
	data, err := os.ReadFile(path)
	if err == nil {
		return string(data), nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(stateDir, ".server-id.*")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	id := uuid.NewString()
	_, err = tmp.WriteString(id)
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return "", err
	}
	if err := os.Link(tmp.Name(), path); errors.Is(err, fs.ErrExist) {
		return ID(stateDir)
	} else if err != nil {
		return "", err
	}
	return id, nil
}
