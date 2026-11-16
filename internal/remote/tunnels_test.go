package remote

import (
	"context"
	"errors"
	"net"
	"strconv"
	"sync"
	"testing"
)

type listening struct {
	ln   net.Listener
	once sync.Once
	done chan struct{}
}

func (l *listening) Done() <-chan struct{} { return l.done }

func (l *listening) Close() {
	l.once.Do(func() {
		l.ln.Close()
		close(l.done)
	})
}

func bindLocally(local int, to Tunnel) (Forwarding, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(local))
	if err != nil {
		return nil, &PortTakenError{Local: local, To: to.Destination()}
	}
	return &listening{ln: ln, done: make(chan struct{})}, nil
}

func TestTunnelsKeepTheirPortAcrossReconnectsAndNeverMoveIt(t *testing.T) {
	dir := t.TempDir()
	tunnels := &Tunnels{StateDir: dir}
	t.Cleanup(tunnels.Close)
	open := func(ts *Tunnels, name, server string) (int, error) {
		t.Helper()
		return ts.Open(name, Tunnel{Server: server, Socket: "/srv/state/review.sock"}, bindLocally)
	}

	first, err := open(tunnels, "homelab", "s-1")
	if err != nil {
		t.Fatal(err)
	}
	if other, _ := open(tunnels, "build", "s-2"); other == first {
		t.Fatalf("two servers whose review sockets share a path share 127.0.0.1:%d", first)
	}
	for _, f := range tunnels.live {
		f.Close()
	}
	if again, err := open(tunnels, "homelab", "s-1"); err != nil || again != first {
		t.Fatalf("after the link dropped the tunnel came back on %d (%v); want %d so open tabs keep working", again, err, first)
	}
	tunnels.Close()
	restarted := &Tunnels{StateDir: dir}
	t.Cleanup(restarted.Close)
	if again, err := open(restarted, "homelab", "s-1"); err != nil || again != first {
		t.Fatalf("after a hub restart the tunnel came back on %d (%v); want %d", again, err, first)
	}

	restarted.Close()
	squatter, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(first))
	if err != nil {
		t.Fatal(err)
	}
	defer squatter.Close()
	var taken *PortTakenError
	if moved, err := open(restarted, "homelab", "s-1"); !errors.As(err, &taken) || taken.Local != first {
		t.Fatalf("with another program on %d the tunnel opened on %d (%v); want a collision report and no new port", first, moved, err)
	}
}

func TestOnlyLoopbackLinksGoThroughTheTunnel(t *testing.T) {
	for raw, want := range map[string]string{
		"http://localhost:5173/app?x=1#top":  "http://127.0.0.1:52817/app?x=1#top",
		"http://[::1]:3000/":                 "http://127.0.0.1:52817/",
		"https://127.0.0.1/health":           "https://127.0.0.1:52817/health",
		"HTTP://LOCALHOST:47960/index.html":  "http://127.0.0.1:52817/index.html",
		"http://0.0.0.0:47960/":              "http://127.0.0.1:52817/",
		"https://github.com/acme/app/pull/7": "",
		"http://homelab.tailnet:3000/":       "",
	} {
		_, loopback := LoopbackPort(raw)
		if loopback != (want != "") {
			t.Errorf("%s: loopback %v", raw, loopback)
			continue
		}
		if loopback && ThroughTunnel(raw, 52817) != want {
			t.Errorf("%s became %s; want %s", raw, ThroughTunnel(raw, 52817), want)
		}
	}
}

func TestAServerCannotMakeTheLaptopOpenAnythingButAWebLink(t *testing.T) {
	var client Client
	for _, raw := range []string{"-a Terminal", "/System/Applications/Utilities/Terminal.app", "vscode://file/etc/passwd", "ftp://example.com/x", "https://"} {
		if got, err := client.Resolve(context.Background(), nil, ReviewHome{}, raw); err == nil {
			t.Errorf("%q resolved to %q; want a refusal, since the laptop's opener would run it", raw, got)
		}
	}
	if got, err := client.Resolve(context.Background(), nil, ReviewHome{}, "https://github.com/acme/app/pull/7"); err != nil || got != "https://github.com/acme/app/pull/7" {
		t.Errorf("a public link resolved to %q, %v; want it unchanged", got, err)
	}
}
