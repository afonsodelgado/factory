package node

import (
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"syscall"
)

var ErrRunning = errors.New("a factory node already runs for this state directory; stop it before starting another")

type OldOwnerError struct{ Lock string }

func (e OldOwnerError) Error() string {
	return "an older factory holds " + e.Lock + ": an open hub, factory host run or factory serve from before the node. Quit that hub, close the host window, or stop factory serve (factory phone stop with the old version); factory never stops them for you. Then run factory node run again"
}

func Lock(nodeLock string, oldLocks ...string) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(nodeLock), 0o700); err != nil {
		return nil, err
	}
	release, err := flock(nodeLock)
	if err != nil {
		return nil, err
	}
	if release == nil {
		return nil, ErrRunning
	}
	held := []func(){release}
	releaseAll := func() {
		for _, r := range held {
			r()
		}
	}
	for _, path := range oldLocks {
		r, err := flock(path)
		if err != nil || r == nil {
			releaseAll()
			if err != nil {
				return nil, err
			}
			return nil, OldOwnerError{Lock: path}
		}
		held = append(held, r)
	}
	return releaseAll, nil
}

func flock(path string) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, nil
	}
	return func() { f.Close() }, nil
}

func ListenUnix(path string) (net.Listener, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, err
	}
	if err := clearStaleSocket(path); err != nil {
		return nil, err
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		ln.Close()
		return nil, err
	}
	return ln, nil
}

func clearStaleSocket(path string) error {
	info, err := os.Lstat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil
	case err != nil:
		return err
	case info.Mode()&fs.ModeSocket == 0:
		return fmt.Errorf("refusing to listen at %s: it exists and is not a socket (a symlink or file put there by something else); remove it if it is yours", path)
	}
	return os.Remove(path)
}
