package remote

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
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

func (c Client) Resolve(ctx context.Context, tunnels *Tunnels, review ReviewHome, raw string) (string, error) {
	if isFile(raw) {
		return c.reviewPage(ctx, tunnels, review, raw)
	}
	port, loopback := LoopbackPort(raw)
	if !loopback {
		return openable(raw)
	}
	hello, err := c.Hello(ctx)
	if err != nil {
		return "", err
	}
	local, err := tunnels.Open(c.Name, Tunnel{Server: hello.Server, Port: port}, c.Forward)
	if err != nil {
		return "", err
	}
	return ThroughTunnel(raw, local), nil
}

func (c Client) reviewPage(ctx context.Context, tunnels *Tunnels, review ReviewHome, raw string) (string, error) {
	if review.Socket == "" {
		return "", fmt.Errorf("%s's review server is not running, so its pages cannot open here", c.Name)
	}
	hello, err := c.Hello(ctx)
	if err != nil {
		return "", err
	}
	local, err := tunnels.Open(c.Name, Tunnel{Server: hello.Server, Socket: review.Socket}, c.Forward)
	if err != nil {
		return "", err
	}
	var page string
	if err := c.Read(ctx, "link", LinkRequest{URL: raw, Port: local}, &page); err != nil {
		return "", err
	}
	if !strings.HasPrefix(page, "/r/"+review.Token+"/") {
		return "", fmt.Errorf("%s is a file on %s outside every factory's review pages, so this laptop cannot show it", strings.TrimPrefix(raw, "file://"), c.Name)
	}
	return "http://" + net.JoinHostPort("127.0.0.1", strconv.Itoa(local)) + page, nil
}

func (c Client) Restore(ctx context.Context, tunnels *Tunnels, review ReviewHome) error {
	hello, err := c.Hello(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, t := range LoadTunnels(c.StateDir, c.Name) {
		if t.Server != hello.Server {
			continue
		}
		local, err := tunnels.Open(c.Name, t, c.Forward)
		if err == nil && t.Socket != "" && t.Socket == review.Socket {
			err = c.Read(ctx, "link", LinkRequest{Port: local}, nil)
		}
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}
