package remote

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"

	"github.com/afonsodelgado/factory/internal/review"
)

func LoopbackPort(raw string) (int, bool) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return 0, false
	}
	if !onTheServerItself(u.Hostname()) {
		return 0, false
	}
	port := u.Port()
	if port == "" {
		port = map[string]string{"http": "80", "https": "443"}[u.Scheme]
	}
	n, err := strconv.Atoi(port)
	return n, err == nil
}

func onTheServerItself(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && (ip.IsLoopback() || ip.IsUnspecified())
}

func ThroughTunnel(raw string, local int) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	u.Host = net.JoinHostPort("127.0.0.1", strconv.Itoa(local))
	return u.String()
}

func WebLink(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Hostname() != ""
}

func openable(raw string) (string, error) {
	if !WebLink(raw) {
		return "", fmt.Errorf("only web links open on this laptop, and %q is not one", raw)
	}
	return raw, nil
}

func isFile(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "file"
}

func (c Client) Resolve(ctx context.Context, tunnels *Tunnels, home, raw string) (string, error) {
	if _, loopback := LoopbackPort(raw); !loopback && !isFile(raw) {
		return openable(raw)
	}
	hello, err := c.Hello(ctx)
	if err != nil {
		return "", err
	}
	reviewPort, served := LoopbackPort(home)
	target, registered := raw, false
	if isFile(raw) && !served {
		return "", fmt.Errorf("%s's review server is not running, so its pages cannot open here", c.Name)
	}
	if isFile(raw) {
		local, err := tunnels.Open(c.Name, hello.Server, reviewPort, c.Forward)
		if err != nil {
			return "", err
		}
		if err := c.Read(ctx, "link", LinkRequest{URL: raw, Port: local}, &target); err != nil {
			return "", err
		}
		if isFile(target) {
			return "", fmt.Errorf("%s is a file on %s outside every factory's review pages, so this laptop cannot show it", strings.TrimPrefix(raw, "file://"), c.Name)
		}
		registered = true
	}
	port, ok := LoopbackPort(target)
	if !ok {
		return openable(target)
	}
	local, err := tunnels.Open(c.Name, hello.Server, port, c.Forward)
	if err != nil {
		return "", err
	}
	if port == reviewPort && !registered {
		if err := c.Read(ctx, "link", LinkRequest{URL: target, Port: local}, &target); err != nil {
			return "", err
		}
	}
	return ThroughTunnel(target, local), nil
}

func EndpointOf(home string) review.Endpoint {
	u, err := url.Parse(home)
	if err != nil {
		return review.Endpoint{}
	}
	token, _, _ := strings.Cut(strings.TrimPrefix(u.Path, "/r/"), "/")
	return review.Endpoint{Origin: u.Scheme + "://" + u.Host, Token: token}
}

func (c Client) Restore(ctx context.Context, tunnels *Tunnels, home string) error {
	hello, err := c.Hello(ctx)
	if err != nil {
		return err
	}
	reviewPort, _ := LoopbackPort(home)
	var errs []error
	for _, t := range LoadTunnels(c.StateDir, c.Name) {
		if t.Server != hello.Server {
			continue
		}
		local, err := tunnels.Open(c.Name, t.Server, t.Port, c.Forward)
		if err == nil && t.Port == reviewPort {
			err = c.Read(ctx, "link", LinkRequest{Port: local}, nil)
		}
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}
