package host

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"github.com/google/uuid"

	"github.com/afonsodelgado/factory/internal/factory"
	"github.com/afonsodelgado/factory/internal/remote"
)

var openers = []string{"open", "xdg-open"}

const rememberedTunnels = 32

func (m *Runtime) opensPath() string { return filepath.Join(m.Config.StateDir, "opens.json") }

func (m *Runtime) tunnelsPath() string { return filepath.Join(m.Config.StateDir, "tunnels.json") }

func (m *Runtime) forwardedPorts() []int {
	var ports []int
	if data, err := os.ReadFile(m.tunnelsPath()); err == nil {
		json.Unmarshal(data, &ports)
	}
	return ports
}

func (m *Runtime) allowForwarded(port int) error {
	m.Pages.Allow(port)
	ports := m.forwardedPorts()
	if slices.Contains(ports, port) {
		return nil
	}
	ports = append(ports, port)
	return SavePrivate(m.tunnelsPath(), ports[max(0, len(ports)-rememberedTunnels):])
}

func (m *Runtime) allowRememberedTunnels() {
	if m.Pages == nil {
		return
	}
	for _, port := range m.forwardedPorts() {
		m.Pages.Allow(port)
	}
}

func (m *Runtime) Opens() []remote.Open {
	var opens []remote.Open
	if data, err := os.ReadFile(m.opensPath()); err == nil {
		json.Unmarshal(data, &opens)
	}
	return opens
}

func (m *Runtime) QueueOpen(it Item, o remote.Open) (ReviewReply, error) {
	if !m.opensOnTheLaptop(o.URL) {
		return ReviewReply{}, fmt.Errorf("only web links and factory pages open on the laptop, and %q is neither", o.URL)
	}
	o.ID, o.Factory, o.At = uuid.NewString(), it.Factory.Label(), m.Now()
	if err := SavePrivate(m.opensPath(), append(m.Opens(), o)); err != nil {
		return ReviewReply{}, err
	}
	return ReviewReply{Target: "the laptop", Queued: true}, nil
}

func (m *Runtime) opensOnTheLaptop(raw string) bool {
	if remote.WebLink(raw) {
		return true
	}
	if m.Pages == nil {
		return false
	}
	_, page := m.Pages.PagePath(raw)
	return page
}

func (m *Runtime) Opened(ids []string) error {
	opens := m.Opens()
	kept := slices.DeleteFunc(slices.Clone(opens), func(o remote.Open) bool { return slices.Contains(ids, o.ID) })
	if len(kept) == len(opens) {
		return nil
	}
	return SavePrivate(m.opensPath(), kept)
}

func (m *Runtime) Link(r remote.LinkRequest) (string, error) {
	if m.Pages == nil {
		return "", errors.New("the review server is not running")
	}
	if r.Port != 0 {
		if err := m.allowForwarded(r.Port); err != nil {
			return "", err
		}
	}
	if page, ok := m.Pages.PagePath(r.URL); ok {
		return page, nil
	}
	if r.URL != "" {
		return "", fmt.Errorf("%s is not a page in any factory's folder", r.URL)
	}
	return "", nil
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
