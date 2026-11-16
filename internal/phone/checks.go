package phone

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os/exec"
	"slices"
	"strings"
	"time"

	"github.com/afonsodelgado/factory/internal/state"
)

const (
	Passed  = "ok"
	Failed  = "fail"
	Skipped = "skip"
)

var pushServices = []string{"fcm.googleapis.com:443", "web.push.apple.com:443", "updates.push.services.mozilla.com:443"}

type Check struct {
	Name   string `json:"name"`
	State  string `json:"state"`
	Detail string `json:"detail"`
	Fix    string `json:"fix,omitempty"`
}

type Probe struct {
	Tailscale func(ctx context.Context, args ...string) ([]byte, error)
	DialTLS   func(ctx context.Context, address string) error
}

func SystemProbe() Probe {
	return Probe{
		Tailscale: func(ctx context.Context, args ...string) ([]byte, error) {
			return exec.CommandContext(ctx, "tailscale", args...).Output()
		},
		DialTLS: func(ctx context.Context, address string) error {
			dialer := tls.Dialer{NetDialer: &net.Dialer{Timeout: 5 * time.Second}}
			conn, err := dialer.DialContext(ctx, "tcp", address)
			if err == nil {
				conn.Close()
			}
			return err
		},
	}
}

type tailscaleStatus struct {
	BackendState string
	CertDomains  []string
	Self         struct{ DNSName string }
}

type serveStatus struct {
	Web map[string]struct {
		Handlers map[string]struct{ Proxy string }
	}
	AllowFunnel map[string]bool
}

func Checks(ctx context.Context, m state.Machine, probe Probe) []Check {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	return append(tailscaleChecks(ctx, m, probe), pushReachable(ctx, probe))
}

func tailscaleChecks(ctx context.Context, m state.Machine, probe Probe) []Check {
	host, port := m.NodeHostname(), m.NodePort()
	var status tailscaleStatus
	out, err := probe.Tailscale(ctx, "status", "--json")
	switch {
	case errors.Is(err, exec.ErrNotFound):
		return append([]Check{{Name: "tailscale", State: Failed, Detail: "not installed", Fix: "install Tailscale from https://tailscale.com/download and log in"}}, skipped("login", "ts.net name", "https cert", "serve", "funnel")...)
	case err == nil:
		err = json.Unmarshal(out, &status)
	}
	if err != nil || status.BackendState != "Running" {
		return append([]Check{{Name: "tailscale", State: Failed, Detail: "installed but not logged in or not running", Fix: "tailscale up"}}, skipped("ts.net name", "https cert", "serve", "funnel")...)
	}
	name := strings.TrimSuffix(status.Self.DNSName, ".")
	checks := []Check{{Name: "tailscale", State: Passed, Detail: "logged in as " + name}, machineName(name, host)}
	checks = append(checks, certificate(status.CertDomains, host))
	var serve serveStatus
	out, err = probe.Tailscale(ctx, "serve", "status", "--json")
	if err == nil {
		err = json.Unmarshal(out, &serve)
	}
	return append(checks, mapping(serve, err, host, port), funnel(serve, err, host, port))
}

func skipped(names ...string) []Check {
	checks := make([]Check, len(names))
	for i, name := range names {
		checks[i] = Check{Name: name, State: Skipped, Detail: "needs Tailscale first"}
	}
	return checks
}

func machineName(name, host string) Check {
	switch {
	case host == "":
		return Check{Name: "ts.net name", State: Failed, Detail: "node.hostname is not set", Fix: "put node.hostname: " + name + " in config.yml, then restart the node"}
	case name != host:
		return Check{Name: "ts.net name", State: Failed, Detail: fmt.Sprintf("node.hostname is %s, but this machine is %s", host, name), Fix: "put node.hostname: " + name + " in config.yml, then restart the node"}
	}
	return Check{Name: "ts.net name", State: Passed, Detail: host}
}

func certificate(domains []string, host string) Check {
	if !slices.Contains(domains, host) {
		return Check{Name: "https cert", State: Failed, Detail: "HTTPS is off for this tailnet", Fix: "turn on HTTPS Certificates at https://login.tailscale.com/admin/dns"}
	}
	return Check{Name: "https cert", State: Passed, Detail: "Tailscale can issue a certificate for " + host}
}

func mapping(serve serveStatus, err error, host string, port int) Check {
	want := fmt.Sprintf("http://127.0.0.1:%d", port)
	fix := "tailscale serve --bg --https=443 " + want
	if err != nil {
		return Check{Name: "serve", State: Failed, Detail: "tailscale serve status failed: " + err.Error(), Fix: fix}
	}
	got := serve.Web[host+":443"].Handlers["/"].Proxy
	if strings.TrimSuffix(got, "/") != want {
		return Check{Name: "serve", State: Failed, Detail: fmt.Sprintf("https://%s/ does not forward to %s", host, want), Fix: fix}
	}
	return Check{Name: "serve", State: Passed, Detail: fmt.Sprintf("https://%s/ → %s", host, want)}
}

func funnel(serve serveStatus, err error, host string, port int) Check {
	if err != nil {
		return Check{Name: "funnel", State: Failed, Detail: "could not tell whether Funnel is on: tailscale serve status failed: " + err.Error(), Fix: "tailscale serve status --json"}
	}
	if serve.AllowFunnel[host+":443"] {
		return Check{Name: "funnel", State: Failed, Detail: "Funnel puts this server on the public internet", Fix: "tailscale funnel --https=443 off"}
	}
	return Check{Name: "funnel", State: Passed, Detail: fmt.Sprintf("off; port %d stays on the tailnet", port)}
}

func pushReachable(ctx context.Context, probe Probe) Check {
	var unreachable []string
	for _, address := range pushServices {
		if probe.DialTLS(ctx, address) != nil {
			unreachable = append(unreachable, strings.TrimSuffix(address, ":443"))
		}
	}
	if len(unreachable) > 0 {
		return Check{Name: "push reach", State: Failed, Detail: "cannot open TLS to " + strings.Join(unreachable, ", "), Fix: "allow outbound HTTPS from this machine to the browser push services"}
	}
	return Check{Name: "push reach", State: Passed, Detail: "reachable; this does not prove a phone gets notifications, so send a test push"}
}
