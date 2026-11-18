package node

import (
	"fmt"
	"net"
	"slices"
	"strconv"
	"strings"

	"github.com/afonsodelgado/factory/internal/host"
)

const (
	FirstAppHTTPS = 8443
	LastAppHTTPS  = 8499
)

func FreePort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port, nil
}

func (sc ServeConfig) serves(https int) bool {
	port := strconv.Itoa(https)
	if _, tcp := sc.TCP[port]; tcp {
		return true
	}
	for hostPort := range sc.Web {
		if strings.HasSuffix(hostPort, ":"+port) {
			return true
		}
	}
	return false
}

func pickHTTPS(sc ServeConfig, reserved []int) (int, error) {
	for port := FirstAppHTTPS; port <= LastAppHTTPS; port++ {
		if !slices.Contains(reserved, port) && !sc.serves(port) {
			return port, nil
		}
	}
	return 0, failed(host.FailPorts, fmt.Errorf("every tailnet HTTPS port from %d to %d already serves a review stack or another Serve entry; stop a review stack, or free a port with tailscale serve --https=<port> off", FirstAppHTTPS, LastAppHTTPS))
}
