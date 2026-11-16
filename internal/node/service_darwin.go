package node

import (
	"context"
	"encoding/xml"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	ServiceLabel = "dev.factory.node"
	unloadWait   = 10 * time.Second
)

func (s Service) UnitPath() string {
	return filepath.Join(s.Home, "Library", "LaunchAgents", ServiceLabel+".plist")
}

func (s Service) Kind() string { return "launchd agent " + ServiceLabel }

func (s Service) Unit() []byte {
	var b strings.Builder
	str := func(v string) string {
		var e strings.Builder
		xml.EscapeText(&e, []byte(v))
		return "<string>" + e.String() + "</string>"
	}
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	` + str(ServiceLabel) + `
	<key>ProgramArguments</key>
	<array>
		` + str(s.Binary) + `
		<string>node</string>
		<string>run</string>
	</array>
	<key>WorkingDirectory</key>
	` + str(s.Home) + `
	<key>EnvironmentVariables</key>
	<dict>
`)
	for _, kv := range s.Env {
		name, value, _ := strings.Cut(kv, "=")
		b.WriteString("\t\t<key>" + name + "</key>\n\t\t" + str(value) + "\n")
	}
	b.WriteString(`	</dict>
	<key>StandardOutPath</key>
	` + str(s.Log) + `
	<key>StandardErrorPath</key>
	` + str(s.Log) + `
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<true/>
	<key>AbandonProcessGroup</key>
	<true/>
</dict>
</plist>
`)
	return []byte(b.String())
}

func launchdDomain() string { return "gui/" + strconv.Itoa(os.Getuid()) }

func launchdTarget() string { return launchdDomain() + "/" + ServiceLabel }

func (s Service) launchctl(ctx context.Context, args ...string) ([]byte, error) {
	return s.Run(ctx, "launchctl", args...)
}

func (s Service) State(ctx context.Context) ServiceState {
	st := ServiceState{Installed: s.installed()}
	out, err := s.launchctl(ctx, "print", launchdTarget())
	if err != nil {
		return st
	}
	st.Loaded = true
	for _, line := range strings.Split(string(out), "\n") {
		key, value, _ := strings.Cut(strings.TrimSpace(line), " = ")
		switch key {
		case "state":
			st.Running = value == "running"
		case "pid":
			st.PID = value
		}
	}
	return st
}

func (s Service) Load(ctx context.Context) error {
	if s.State(ctx).Loaded {
		if _, err := s.launchctl(ctx, "bootout", launchdTarget()); err != nil {
			return err
		}
		s.awaitUnloaded(ctx)
	}
	if _, err := s.launchctl(ctx, "enable", launchdTarget()); err != nil {
		return err
	}
	_, err := s.launchctl(ctx, "bootstrap", launchdDomain(), s.UnitPath())
	return err
}

func (s Service) awaitUnloaded(ctx context.Context) {
	deadline := time.Now().Add(unloadWait)
	for s.State(ctx).Loaded && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
	}
}

func (s Service) Unload(ctx context.Context) error {
	if !s.State(ctx).Loaded {
		return nil
	}
	_, err := s.launchctl(ctx, "bootout", launchdTarget())
	return err
}

func (s Service) Uninstall(ctx context.Context) error {
	if err := s.Unload(ctx); err != nil {
		return err
	}
	return s.remove()
}

func (s Service) RestartCommand() string { return "launchctl kickstart -k " + launchdTarget() }

func (s Service) Loaded(bool) string {
	return "launchd agent loaded: runs factory node run, starts at login"
}

func (s Service) Boot(context.Context, bool) (string, string, bool) {
	return "starts when you log in: after a reboot the node waits for your login, and FileVault's login screen is not enough", "", true
}
