package phone

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/afonsodelgado/factory/internal/host"
	"github.com/afonsodelgado/factory/internal/state"
)

const (
	ModeHub    = "hub"
	ModeLocal  = "local"
	ModeRemote = "remote"

	transitionWait = 20 * time.Second
	handoffWait    = 15 * time.Second
	lockPoll       = 50 * time.Millisecond
)

var (
	ErrLocked   = errors.New("locked")
	ErrHubOpen  = errors.New("another factory hub is already running")
	ErrServing  = errors.New("factory serve is already running for this state directory")
	errOldOwner = "an older factory, from before the phone gateway, holds this machine's runtime lock (%s). Quit it, or restart it with this version, then try again"
)

type OwnerInfo struct {
	Mode    string    `json:"mode"`
	Version string    `json:"version"`
	PID     int       `json:"pid"`
	Since   time.Time `json:"since"`
}

func ownerPath(stateDir string) string      { return filepath.Join(stateDir, "owner.json") }
func transitionPath(stateDir string) string { return filepath.Join(stateDir, "owner-transition.lock") }
func ServeLockPath(stateDir string) string  { return filepath.Join(stateDir, "phone", "serve.lock") }
func ControlPath(stateDir string) string    { return filepath.Join(stateDir, "phone", "control.sock") }

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

func lockWithin(path string, wait time.Duration) (func(), error) {
	deadline := time.Now().Add(wait)
	for {
		unlock, err := Lock(path)
		if !errors.Is(err, ErrLocked) || time.Now().After(deadline) {
			return unlock, err
		}
		time.Sleep(lockPoll)
	}
}

func ReadOwner(stateDir string) (OwnerInfo, bool) {
	var info OwnerInfo
	data, err := os.ReadFile(ownerPath(stateDir))
	if err != nil || json.Unmarshal(data, &info) != nil || !alive(info.PID) {
		return OwnerInfo{}, false
	}
	return info, true
}

func alive(pid int) bool {
	return pid > 0 && syscall.Kill(pid, 0) == nil
}

func CurrentOwner(cfg *state.Config) (OwnerInfo, bool) {
	if !Held(cfg.LockPath()) {
		return OwnerInfo{}, false
	}
	info, known := ReadOwner(cfg.StateDir)
	if !known {
		return OwnerInfo{Mode: "unknown"}, true
	}
	return info, true
}

func FallbackOwns(cfg *state.Config) bool {
	info, running := CurrentOwner(cfg)
	return running && info.Mode == ModeLocal
}

func Claim(cfg *state.Config, mode, version string) (func(), error) {
	if mode != ModeHub {
		return own(cfg, mode, version)
	}
	unlockTransition, err := lockWithin(transitionPath(cfg.StateDir), transitionWait)
	if err != nil {
		return nil, fmt.Errorf("the factory runtime is changing hands and did not settle within %s; try again", transitionWait)
	}
	defer unlockTransition()
	release, err := own(cfg, mode, version)
	if !errors.Is(err, ErrLocked) {
		return release, err
	}
	if err := handOff(cfg, version); err != nil {
		return nil, err
	}
	release, err = ownWithin(cfg, mode, version, handoffWait)
	if errors.Is(err, ErrLocked) {
		return nil, errors.New("factory serve's runtime did not hand over within " + handoffWait.String() + "; run factory phone stop, then open the hub again")
	}
	return release, err
}

func handOff(cfg *state.Config, version string) error {
	info, known := ReadOwner(cfg.StateDir)
	switch {
	case !known:
		return fmt.Errorf(errOldOwner, cfg.LockPath())
	case info.Mode == ModeHub:
		return ErrHubOpen
	case info.Mode == ModeRemote:
		return errors.New("factory host run holds this machine's runtime for a laptop. Close its host window in the factory tmux session, then open the hub")
	case info.Version != version:
		return fmt.Errorf("factory serve runs version %s and this is %s; run factory phone stop, then open the hub and run factory phone start", info.Version, version)
	}
	reply, err := Call(context.Background(), cfg.SocketPath(), host.Message{T: "handoff", Pane: "phone"})
	if err != nil || reply.T != "done" {
		return fmt.Errorf("factory serve's runtime would not hand over: %v %s", err, reply.Error)
	}
	return nil
}

func ownWithin(cfg *state.Config, mode, version string, wait time.Duration) (func(), error) {
	deadline := time.Now().Add(wait)
	for {
		release, err := own(cfg, mode, version)
		if !errors.Is(err, ErrLocked) || time.Now().After(deadline) {
			return release, err
		}
		time.Sleep(lockPoll)
	}
}

func own(cfg *state.Config, mode, version string) (func(), error) {
	unlock, err := Lock(cfg.LockPath())
	if err != nil {
		return nil, err
	}
	info := OwnerInfo{Mode: mode, Version: version, PID: os.Getpid(), Since: time.Now().UTC()}
	data, _ := json.Marshal(info)
	if err := writePrivate(ownerPath(cfg.StateDir), data); err != nil {
		unlock()
		return nil, err
	}
	return func() {
		if current, ok := ReadOwner(cfg.StateDir); ok && current.PID == info.PID {
			os.Remove(ownerPath(cfg.StateDir))
		}
		unlock()
	}, nil
}

func Release(cfg *state.Config) error {
	if !FallbackOwns(cfg) {
		return nil
	}
	reply, err := Call(context.Background(), cfg.SocketPath(), host.Message{T: "handoff", Pane: "phone"})
	if err != nil {
		return err
	}
	if reply.T != "done" {
		return errors.New(reply.Error)
	}
	return nil
}

type Fallback func() error

func KeepOwner(ctx context.Context, cfg *state.Config, start Fallback, report func(error)) {
	ticker := time.NewTicker(pollEvery)
	defer ticker.Stop()
	for {
		if err := ensureOwner(cfg, start); err != nil {
			report(err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func ensureOwner(cfg *state.Config, start Fallback) error {
	if Held(cfg.LockPath()) {
		return nil
	}
	unlock, err := Lock(transitionPath(cfg.StateDir))
	if err != nil {
		return nil
	}
	defer unlock()
	if Held(cfg.LockPath()) {
		return nil
	}
	return start()
}
