package remote

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/afonsodelgado/factory/internal/state"
)

const (
	tunnelStart    = 15 * time.Second
	tunnelAttempts = 5
)

type Tunnel struct {
	Server string `json:"server"`
	Port   int    `json:"port"`
	Local  int    `json:"local"`
}

type Forwarding interface {
	Done() <-chan struct{}
	Close()
}

type Forwarder func(local, port int) (Forwarding, error)

type PortTakenError struct {
	Local  int
	Remote string
	Port   int
}

func (e *PortTakenError) Error() string {
	return fmt.Sprintf("127.0.0.1:%d, the tunnel to %s:%d, is taken by another program. Pages left open use that port, so it was not moved; close that program, then try again", e.Local, e.Remote, e.Port)
}

type Tunnels struct {
	StateDir string

	mu   sync.Mutex
	live map[string]Forwarding
}

func TunnelsPath(stateDir, name string) string {
	return CachePath(stateDir, name+".tunnels")
}

func LoadTunnels(stateDir, name string) []Tunnel {
	var tunnels []Tunnel
	data, err := os.ReadFile(TunnelsPath(stateDir, name))
	if err == nil {
		json.Unmarshal(data, &tunnels)
	}
	return tunnels
}

func LocalPort(stateDir, name, server string, port int) (int, bool) {
	tunnels := LoadTunnels(stateDir, name)
	i := slices.IndexFunc(tunnels, func(t Tunnel) bool { return t.Server == server && t.Port == port })
	if i < 0 {
		return 0, false
	}
	return tunnels[i].Local, true
}

func (ts *Tunnels) Open(name, server string, port int, forward Forwarder) (int, error) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	key := name + "\x00" + server + "\x00" + strconv.Itoa(port)
	local, known := LocalPort(ts.StateDir, name, server, port)
	if f, ok := ts.live[key]; ok && known && running(f) {
		return local, nil
	}
	if known {
		if !free(local) {
			return 0, &PortTakenError{Local: local, Remote: name, Port: port}
		}
		return local, ts.start(key, local, port, forward)
	}
	for range tunnelAttempts {
		local, err := freePort()
		if err != nil {
			return 0, err
		}
		err = ts.start(key, local, port, forward)
		if taken := (*PortTakenError)(nil); errors.As(err, &taken) {
			continue
		}
		if err != nil {
			return 0, err
		}
		return local, ts.save(name, Tunnel{Server: server, Port: port, Local: local})
	}
	return 0, errors.New("every free local port was taken before the tunnel could bind it; try again")
}

func (ts *Tunnels) start(key string, local, port int, forward Forwarder) error {
	f, err := forward(local, port)
	if err != nil {
		return err
	}
	if ts.live == nil {
		ts.live = map[string]Forwarding{}
	}
	ts.live[key] = f
	return nil
}

func (ts *Tunnels) save(name string, t Tunnel) error {
	path := TunnelsPath(ts.StateDir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(append(LoadTunnels(ts.StateDir, name), t), "", "  ")
	if err != nil {
		return err
	}
	return state.WriteFileAtomic(path, data)
}

func (ts *Tunnels) Close() {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	for key, f := range ts.live {
		f.Close()
		delete(ts.live, key)
	}
}

func running(f Forwarding) bool {
	select {
	case <-f.Done():
		return false
	default:
		return true
	}
}

func free(port int) bool {
	ln, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(port))
	if err != nil {
		return false
	}
	ln.Close()
	return true
}

func freePort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port, nil
}

type sshForwarding struct {
	cmd  *exec.Cmd
	done chan struct{}
}

func (f *sshForwarding) Done() <-chan struct{} { return f.done }

func (f *sshForwarding) Close() {
	f.cmd.Process.Kill()
	<-f.done
}

func (t Transport) Forward(local, port int) (Forwarding, error) {
	cmd := exec.Command("ssh", t.tunnelArgs(local, port)...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	f := &sshForwarding{cmd: cmd, done: make(chan struct{})}
	go func() {
		cmd.Wait()
		close(f.done)
	}()
	deadline := time.Now().Add(tunnelStart)
	for time.Now().Before(deadline) {
		select {
		case <-f.done:
			if !free(local) {
				return nil, &PortTakenError{Local: local, Remote: t.Name, Port: port}
			}
			return nil, &TransportError{Remote: t.Name, Detail: lastLine(stderr.String(), cmd.ProcessState.ExitCode())}
		default:
		}
		if conn, err := net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(local), time.Second); err == nil {
			conn.Close()
			return f, nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	f.Close()
	return nil, &TransportError{Remote: t.Name, Detail: "the tunnel did not open in time"}
}
