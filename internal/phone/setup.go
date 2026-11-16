package phone

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"time"

	"github.com/afonsodelgado/factory/internal/host"
	"github.com/afonsodelgado/factory/internal/state"
)

const (
	stopWait = 10 * time.Second
	testWait = 8 * time.Second
)

type Setup struct {
	Config *state.Config
	Bundle fs.FS
	Start  func() error
	Now    func() time.Time
}

type Status struct {
	Settings state.Phone `json:"settings"`
	Running  bool        `json:"running"`
	Serving  *Serving    `json:"serving,omitempty"`
	Devices  []Device    `json:"devices"`
}

type Serving struct {
	Version string     `json:"version"`
	PID     int        `json:"pid"`
	Owner   *OwnerInfo `json:"owner,omitempty"`
}

type Pairing struct {
	Code    string    `json:"code"`
	URL     string    `json:"url"`
	Expires time.Time `json:"expires"`
}

func (s Setup) store() Store { return Open(s.Config.StateDir) }

func (s Setup) now() time.Time {
	if s.Now == nil {
		return time.Now()
	}
	return s.Now()
}

func (s Setup) Settings() (state.Phone, error) {
	m, err := state.LoadMachine(s.Config.ConfigDir)
	return m.Phone, err
}

func Running(stateDir string) bool { return Held(ServeLockPath(stateDir)) }

func (s Setup) control(m host.Message, into any) error {
	reply, err := Call(context.Background(), ControlPath(s.Config.StateDir), m)
	switch {
	case err != nil:
		return err
	case reply.T != "done":
		return errors.New(reply.Error)
	case into != nil:
		return json.Unmarshal(reply.Data, into)
	}
	return nil
}

func (s Setup) Status() (Status, error) {
	settings, err := s.Settings()
	if err != nil {
		return Status{}, err
	}
	devices, err := s.store().Devices()
	if err != nil {
		return Status{}, err
	}
	status := Status{Settings: settings, Running: Running(s.Config.StateDir), Devices: devices}
	if status.Running {
		var serving Serving
		if s.control(host.Message{T: "status"}, &serving) == nil {
			status.Serving = &serving
		}
	}
	return status, nil
}

func (s Setup) Configure(port int, hostname string) error {
	return state.UpdateMachine(s.Config.ConfigDir, func(m *state.Machine) error {
		if port != 0 {
			m.Phone.Port = port
		}
		if hostname != "" {
			m.Phone.Hostname = hostname
		}
		return nil
	})
}

func (s Setup) Enable() error {
	settings, err := s.Settings()
	if err != nil {
		return err
	}
	if settings.Hostname == "" {
		return errors.New("set this machine's Tailscale name first: factory phone configure --hostname devbox.tail1234.ts.net")
	}
	if _, err := App(s.Bundle); err != nil {
		return err
	}
	public, err := s.store().VAPIDKey(settings.VAPIDPublicKey)
	if err != nil {
		return err
	}
	if err := state.UpdateMachine(s.Config.ConfigDir, func(m *state.Machine) error {
		m.Phone.Enabled, m.Phone.VAPIDPublicKey = true, public
		return nil
	}); err != nil {
		return err
	}
	if Running(s.Config.StateDir) {
		return nil
	}
	return s.Start()
}

func (s Setup) Disable() error {
	if err := state.UpdateMachine(s.Config.ConfigDir, func(m *state.Machine) error {
		m.Phone.Enabled = false
		return nil
	}); err != nil {
		return err
	}
	return s.Stop()
}

func (s Setup) Stop() error {
	if !Running(s.Config.StateDir) {
		return Release(s.Config)
	}
	if err := s.control(host.Message{T: "stop"}, nil); err != nil {
		return err
	}
	deadline := time.Now().Add(stopWait)
	for Running(s.Config.StateDir) {
		if time.Now().After(deadline) {
			return errors.New("factory serve did not stop within " + stopWait.String())
		}
		time.Sleep(lockPoll)
	}
	return nil
}

func (s Setup) Pair() (Pairing, error) {
	settings, err := s.Settings()
	if err != nil {
		return Pairing{}, err
	}
	if !settings.Enabled {
		return Pairing{}, errors.New("Phone is off; turn it on first with factory phone enable")
	}
	code, expires, err := s.store().NewCode(s.now())
	if err != nil {
		return Pairing{}, err
	}
	return Pairing{Code: code, URL: settings.Origin() + "/#pair=" + code, Expires: expires}, nil
}

func (s Setup) Devices() ([]Device, error) { return s.store().Devices() }

func (s Setup) Test() ([]Delivery, error) {
	if !Running(s.Config.StateDir) {
		return nil, errors.New("factory serve is not running, so it cannot send a test notification; start it with factory phone start")
	}
	var results []Delivery
	err := s.control(host.Message{T: "test"}, &results)
	return results, err
}

func (s Setup) Revoke(id string) error {
	if Running(s.Config.StateDir) {
		return s.control(host.Message{T: "revoke", Data: mustJSON(id)}, nil)
	}
	return s.store().Revoke(id)
}

func mustJSON(v any) json.RawMessage {
	data, _ := json.Marshal(v)
	return data
}

type Control struct {
	Config  *state.Config
	Server  *Server
	Version string
	Stop    func()
}

func (c Control) Handle(e host.Event) {
	e.Respond(c.handle(e.Message))
	if e.T == "stop" {
		c.Stop()
	}
}

func (c Control) handle(m host.Message) host.Message {
	switch m.T {
	case "status":
		serving := Serving{Version: c.Version, PID: os.Getpid()}
		if info, running := CurrentOwner(c.Config); running {
			serving.Owner = &info
		}
		return done(serving)
	case "stop":
		return done(nil)
	case "test":
		if c.Server.Push == nil {
			return host.Message{T: "error", Error: "this factory serve sends no notifications"}
		}
		ctx, cancel := context.WithTimeout(context.Background(), testWait)
		defer cancel()
		results, err := c.Server.Push.Test(ctx)
		if err != nil {
			return host.Message{T: "error", Error: err.Error()}
		}
		return done(results)
	case "revoke":
		var id string
		if err := json.Unmarshal(m.Data, &id); err != nil {
			return host.Message{T: "error", Error: err.Error()}
		}
		if err := c.Server.Store.Revoke(id); err != nil {
			return host.Message{T: "error", Error: err.Error()}
		}
		c.Server.Revoke(id)
		return done(nil)
	}
	return host.Message{T: "error", Error: "unknown phone control message: " + m.T}
}

func done(value any) host.Message {
	return host.Message{T: "done", Data: mustJSON(value)}
}
