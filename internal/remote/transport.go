package remote

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/afonsodelgado/factory/internal/state"
)

var RequestCommand = []string{"factory", "host", "request"}

type Transport struct {
	Name     string
	Remote   state.Remote
	StateDir string
}

type CommandError struct {
	Remote string
	Detail string
}

func (e *CommandError) Error() string {
	return e.Remote + " could not run factory over ssh: " + e.Detail
}

type TransportError struct {
	Remote string
	Detail string
}

func (e *TransportError) Error() string {
	if e.Detail == "" {
		return "can't reach " + e.Remote
	}
	return "can't reach " + e.Remote + ": " + e.Detail
}

func (t Transport) Endpoint() string {
	endpoint := t.Remote.Host
	if t.Remote.User != "" {
		endpoint = t.Remote.User + "@" + endpoint
	}
	if t.Remote.Port != 0 {
		endpoint += ":" + strconv.Itoa(t.Remote.Port)
	}
	return endpoint
}

const unreachable = 255

const (
	socketPathLimit = 100
	sshBindSuffix   = len(".0123456789abcdef")
	socketName      = 16
)

func (t Transport) controlDir() string {
	dir := filepath.Join(t.StateDir, "ssh")
	if len(dir)+1+socketName+sshBindSuffix <= socketPathLimit {
		return dir
	}
	return filepath.Join("/tmp", "factory-ssh-"+strconv.Itoa(os.Getuid()))
}

func (t Transport) controlPath() string {
	sum := sha256.Sum256([]byte(t.StateDir + "\x00" + t.Endpoint() + "\x00" + t.Remote.Identity))
	return filepath.Join(t.controlDir(), hex.EncodeToString(sum[:socketName/2]))
}

func (t Transport) privateControlDir() error {
	dir := t.controlDir()
	if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
		return err
	}
	if err := os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
		return err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !info.IsDir() || info.Mode().Perm() != 0o700 || !ok || int(stat.Uid) != os.Getuid() {
		return fmt.Errorf("%s holds ssh connections, so it must be a directory only you can open; remove it and try again", dir)
	}
	return nil
}

func (t Transport) Args(tty bool, command []string) []string {
	args := []string{
		"-o", "BatchMode=yes",
		"-o", "ConnectTimeout=10",
		"-o", "ServerAliveInterval=15",
		"-o", "ServerAliveCountMax=3",
		"-o", "ControlMaster=auto",
		"-o", "ControlPath=" + t.controlPath(),
		"-o", "ControlPersist=10m",
		"-o", "ForwardAgent=no",
		"-o", "ClearAllForwardings=yes",
	}
	if tty {
		args = append(args, "-t")
	} else {
		args = append(args, "-T")
	}
	if t.Remote.User != "" {
		args = append(args, "-l", t.Remote.User)
	}
	if t.Remote.Port != 0 {
		args = append(args, "-p", strconv.Itoa(t.Remote.Port))
	}
	if t.Remote.Identity != "" {
		args = append(args, "-i", state.ExpandHome(t.Remote.Identity), "-o", "IdentitiesOnly=yes")
	}
	return append(append(args, "--", t.Remote.Host), command...)
}

func (t Transport) Run(ctx context.Context, stdin []byte, command []string) ([]byte, error) {
	if err := t.privateControlDir(); err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, "ssh", t.Args(false, command)...)
	cmd.Stdin = bytes.NewReader(stdin)
	cmd.WaitDelay = time.Second
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if errors.Is(err, exec.ErrWaitDelay) {
		err = nil
	}
	if ctx.Err() != nil {
		return nil, &TransportError{Remote: t.Name, Detail: "no answer in time"}
	}
	var exit *exec.ExitError
	switch {
	case errors.As(err, &exit) && exit.ExitCode() == unreachable:
		return nil, &TransportError{Remote: t.Name, Detail: lastLine(stderr.String(), exit)}
	case errors.As(err, &exit):
		return nil, &CommandError{Remote: t.Name, Detail: lastLine(stderr.String(), exit)}
	}
	return out, err
}

func lastLine(stderr string, exit *exec.ExitError) string {
	lines := strings.Split(strings.TrimSpace(stderr), "\n")
	if line := strings.TrimSpace(lines[len(lines)-1]); line != "" {
		return line
	}
	return fmt.Sprintf("ssh exited with %d", exit.ExitCode())
}
