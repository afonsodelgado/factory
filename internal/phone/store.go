package phone

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"

	"github.com/afonsodelgado/factory/internal/state"
)

type Store struct{ Dir string }

func Open(stateDir string) Store { return Store{Dir: filepath.Join(stateDir, "phone")} }

func (s Store) path(name string) string { return filepath.Join(s.Dir, name) }

func (s Store) ensureDir() error {
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return err
	}
	return os.Chmod(s.Dir, 0o700)
}

func (s Store) read(name string, value any) error {
	data, err := os.ReadFile(s.path(name))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return json.Unmarshal(data, value)
}

func (s Store) write(name string, value any) error {
	if err := s.ensureDir(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return writePrivate(s.path(name), data)
}

func writePrivate(path string, data []byte) error {
	if err := state.WriteFileAtomic(path, data); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}

func (s Store) locked(change func() error) error {
	if err := s.ensureDir(); err != nil {
		return err
	}
	lock, err := os.OpenFile(s.path("store.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	return change()
}

func update[T any](s Store, name string, change func(*T) error) error {
	return s.locked(func() error {
		var value T
		if err := s.read(name, &value); err != nil {
			return err
		}
		if err := change(&value); err != nil {
			return err
		}
		return s.write(name, value)
	})
}
