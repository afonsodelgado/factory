package node

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func TestSystemctlFindsTheUserBusWhenSudoLeftNoSession(t *testing.T) {
	root := t.TempDir()
	defer func(was string) { userRuntimeRoot = was }(userRuntimeRoot)
	userRuntimeRoot = root
	os.MkdirAll(filepath.Join(root, strconv.Itoa(os.Getuid())), 0o700)
	t.Setenv("XDG_RUNTIME_DIR", "")
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "")
	var seen string
	s := Service{Run: func(context.Context, string, ...string) ([]byte, error) {
		seen = os.Getenv("XDG_RUNTIME_DIR")
		return nil, nil
	}}
	if _, err := s.systemctl(context.Background(), "daemon-reload"); err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(root, strconv.Itoa(os.Getuid())); seen != want {
		t.Errorf("systemctl --user ran with XDG_RUNTIME_DIR=%q; want %q, or it fails with no user bus after sudo -iu", seen, want)
	}
}
