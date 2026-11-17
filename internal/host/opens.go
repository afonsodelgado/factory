package host

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"

	"github.com/afonsodelgado/factory/internal/factory"
)

var openers = []string{"open", "xdg-open"}

func (m *Runtime) checkOpenable(raw string) (ReviewReply, error) {
	if !WebLink(raw) && !m.factoryPage(raw) {
		return ReviewReply{}, fmt.Errorf("only web links and factory pages open in a browser, and %q is neither", raw)
	}
	return ReviewReply{}, nil
}

func WebLink(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Hostname() != ""
}

func (m *Runtime) factoryPage(raw string) bool {
	if m.Pages == nil {
		return false
	}
	_, page := m.Pages.PagePath(raw)
	return page
}

func WriteOpeners(dir, bin string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	script := "#!/bin/sh\nexec " + factory.ShellQuote(bin) + " review link \"$@\"\n"
	for _, name := range openers {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o755); err != nil {
			return err
		}
	}
	return os.WriteFile(openersFirst(dir), []byte("export PATH="+factory.ShellQuote(dir)+":\"$PATH\"\n"), 0o644)
}

func openersFirst(dir string) string { return filepath.Join(dir, "path.sh") }
