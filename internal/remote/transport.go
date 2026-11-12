package remote

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/afonsodelgado/factory/internal/state"
)

var RequestCommand = []string{"factory", "host", "request"}

type Transport struct {
	Name     string
	Remote   state.Remote
	StateDir string
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

func (t Transport) controlDir() string { return filepath.Join(t.StateDir, "ssh") }

func (t Transport) Args(tty bool, command []string) []string {
	args := []string{
		"-o", "BatchMode=yes",
		"-o", "ConnectTimeout=10",
		"-o", "ServerAliveInterval=15",
		"-o", "ServerAliveCountMax=3",
		"-o", "ControlMaster=auto",
		"-o", "ControlPath=" + filepath.Join(t.controlDir(), "%C"),
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
	if err := os.MkdirAll(t.controlDir(), 0o700); err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, "ssh", t.Args(false, command)...)
	cmd.Stdin = bytes.NewReader(stdin)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if ctx.Err() != nil {
		return nil, &TransportError{Remote: t.Name, Detail: "no answer in time"}
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return nil, &TransportError{Remote: t.Name, Detail: lastLine(stderr.String(), exit)}
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
