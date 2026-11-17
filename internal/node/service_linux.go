package node

import (
	"context"
	"os"
	"os/user"
	"path/filepath"
	"strings"
)

const ServiceLabel = "factory-node.service"

func (s Service) UnitPath() string {
	config := os.Getenv("XDG_CONFIG_HOME")
	if config == "" {
		config = filepath.Join(s.Home, ".config")
	}
	return filepath.Join(config, "systemd", "user", ServiceLabel)
}

func (s Service) Kind() string { return "systemd user unit " + ServiceLabel }

var (
	specifiers = strings.NewReplacer("%", "%%")
	quoted     = strings.NewReplacer(`\`, `\\`, `"`, `\"`, "%", "%%")
	expansions = strings.NewReplacer(`\`, `\\`, `"`, `\"`, "%", "%%", "$", "$$")
)

func (s Service) Unit() []byte {
	var b strings.Builder
	b.WriteString("[Unit]\nDescription=factory node\n\n[Service]\nType=simple\n")
	b.WriteString(`ExecStart="` + expansions.Replace(s.Binary) + "\" node run\n")
	b.WriteString("WorkingDirectory=" + specifiers.Replace(s.Home) + "\n")
	for _, kv := range s.Env {
		b.WriteString(`Environment="` + quoted.Replace(kv) + "\"\n")
	}
	log := specifiers.Replace(s.Log)
	b.WriteString("StandardOutput=append:" + log + "\nStandardError=append:" + log + "\n")
	b.WriteString("Restart=always\nRestartSec=5\nKillMode=process\n\n[Install]\nWantedBy=default.target\n")
	return []byte(b.String())
}

func (s Service) systemctl(ctx context.Context, args ...string) ([]byte, error) {
	return s.Run(ctx, "systemctl", append([]string{"--user"}, args...)...)
}

func (s Service) State(ctx context.Context) ServiceState {
	st := ServiceState{Installed: s.installed()}
	out, err := s.systemctl(ctx, "show", ServiceLabel, "--property=LoadState,ActiveState,MainPID")
	if err != nil {
		return st
	}
	for _, line := range strings.Split(string(out), "\n") {
		key, value, _ := strings.Cut(strings.TrimSpace(line), "=")
		switch key {
		case "LoadState":
			st.Loaded = value == "loaded"
		case "ActiveState":
			st.Running = value == "active"
		case "MainPID":
			if value != "0" {
				st.PID = value
			}
		}
	}
	return st
}

func (s Service) Load(ctx context.Context) error {
	for _, args := range [][]string{{"daemon-reload"}, {"enable", ServiceLabel}, {"restart", ServiceLabel}} {
		if _, err := s.systemctl(ctx, args...); err != nil {
			return err
		}
	}
	return nil
}

func (s Service) Unload(ctx context.Context) error {
	if !s.State(ctx).Loaded {
		return nil
	}
	_, err := s.systemctl(ctx, "disable", "--now", ServiceLabel)
	return err
}

func (s Service) Uninstall(ctx context.Context) error {
	if err := s.Unload(ctx); err != nil {
		return err
	}
	if err := s.remove(); err != nil {
		return err
	}
	_, err := s.systemctl(ctx, "daemon-reload")
	return err
}

func (s Service) RestartCommand() string { return "systemctl --user restart " + ServiceLabel }

func (s Service) Boot(ctx context.Context, enable bool) (string, string, bool) {
	login := os.Getenv("USER")
	if u, err := user.Current(); err == nil {
		login = u.Username
	}
	fix := "sudo loginctl enable-linger " + login
	lingering := func() bool {
		out, err := s.Run(ctx, "loginctl", "show-user", login, "--property=Linger", "--value")
		return err == nil && strings.TrimSpace(string(out)) == "yes"
	}
	if lingering() {
		return "lingering on: the node starts at boot without a login", "", true
	}
	if enable {
		if _, err := s.Run(ctx, "loginctl", "enable-linger", login); err == nil && lingering() {
			return "lingering on: the node starts at boot without a login", "", true
		}
	}
	return "lingering is off, so the node stops when you log out and waits for a login after a reboot; turning it on needs root here", fix, false
}

func (s Service) Loaded(persists bool) string {
	if persists {
		return "systemd user unit factory-node enabled, lingering on"
	}
	return "systemd user unit factory-node enabled"
}
