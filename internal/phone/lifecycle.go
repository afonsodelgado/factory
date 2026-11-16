package phone

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
)

var ErrLocked = errors.New("locked")

func ServeLockPath(stateDir string) string { return filepath.Join(stateDir, "phone", "serve.lock") }
func ControlPath(stateDir string) string   { return filepath.Join(stateDir, "phone", "control.sock") }

func Lock(path string) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, ErrLocked
	}
	return func() { f.Close() }, nil
}

func Held(path string) bool {
	unlock, err := Lock(path)
	if err != nil {
		return errors.Is(err, ErrLocked)
	}
	unlock()
	return false
}
