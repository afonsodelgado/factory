package state

import (
	"bytes"
	"cmp"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"

	"github.com/goccy/go-yaml"
	"github.com/goccy/go-yaml/ast"
	"github.com/goccy/go-yaml/parser"
)

const DefaultPhonePort = 7418

type Machine struct {
	Phone Phone `yaml:"phone,omitempty"`
	Node  Node  `yaml:"node,omitempty"`
}

type Node struct {
	Owner     string    `yaml:"owner,omitempty"`
	Port      int       `yaml:"port,omitempty"`
	Hostname  string    `yaml:"hostname,omitempty"`
	Target    string    `yaml:"target,omitempty"`
	KeepAwake KeepAwake `yaml:"keep_awake,omitempty"`
}

type KeepAwake string

const sleeps KeepAwake = "off"

func (k *KeepAwake) UnmarshalYAML(data []byte) error {
	var on bool
	if err := yaml.Unmarshal(data, &on); err != nil {
		return fmt.Errorf("node.keep_awake must be true or false: %w", err)
	}
	*k = ""
	if !on {
		*k = sleeps
	}
	return nil
}

func (k KeepAwake) MarshalYAML() (any, error) { return k != sleeps, nil }

func (m Machine) KeepsAwake() bool { return m.Node.KeepAwake != sleeps }

func (m Machine) NodePort() int {
	if m.Node.Port != 0 {
		return m.Node.Port
	}
	return m.Phone.ListenPort()
}

func (m Machine) NodeHostname() string { return cmp.Or(m.Node.Hostname, m.Phone.Hostname) }

func (m Machine) NodeName() string {
	name, _, _ := strings.Cut(m.NodeHostname(), ".")
	if name == "" {
		host, _ := os.Hostname()
		name, _, _ = strings.Cut(host, ".")
	}
	return name
}

func (m Machine) NodeOrigin() string {
	if m.NodeHostname() == "" {
		return ""
	}
	return "https://" + m.NodeHostname()
}

type Phone struct {
	Enabled        bool   `yaml:"enabled,omitempty"`
	Port           int    `yaml:"port,omitempty"`
	Hostname       string `yaml:"hostname,omitempty"`
	VAPIDPublicKey string `yaml:"vapid_public_key,omitempty"`
}

func (p Phone) ListenPort() int {
	if p.Port == 0 {
		return DefaultPhonePort
	}
	return p.Port
}

var tailnetDNS = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?\.[a-z0-9]([a-z0-9-]*[a-z0-9])?\.ts\.net$`)

func LoadMachine(configDir string) (Machine, error) {
	var m Machine
	data, err := os.ReadFile(filepath.Join(configDir, "config.yml"))
	if errors.Is(err, fs.ErrNotExist) {
		return m, nil
	}
	if err != nil {
		return m, err
	}
	if err := yaml.Unmarshal(data, &m); err != nil {
		return m, err
	}
	return m, m.Validate()
}

func (m Machine) Equal(other Machine) bool { return m == other }

func (m Machine) Validate() error {
	if m.Node.Port < 0 || m.Node.Port > 65535 {
		return fmt.Errorf("node.port %d must be between 1 and 65535", m.Node.Port)
	}
	if m.Node.Hostname != "" && !tailnetDNS.MatchString(m.Node.Hostname) {
		return fmt.Errorf("node.hostname %q must be this machine's full Tailscale name, such as devbox.tail1234.ts.net", m.Node.Hostname)
	}
	if m.Node.Target != "" && !tailnetDNS.MatchString(m.Node.Target) {
		return fmt.Errorf("node.target %q must be a node's full Tailscale name, such as homelab.tail1234.ts.net; the hub's d key sets it", m.Node.Target)
	}
	return m.Phone.validate()
}

func (p Phone) validate() error {
	switch {
	case p.Port < 0 || p.Port > 65535:
		return fmt.Errorf("phone.port %d must be between 1 and 65535", p.Port)
	case p.Hostname != "" && !tailnetDNS.MatchString(p.Hostname):
		return fmt.Errorf("phone.hostname %q must be this machine's full Tailscale name, such as devbox.tail1234.ts.net", p.Hostname)
	}
	return nil
}

func UpdateMachine(configDir string, change func(*Machine) error) error {
	return rewriteConfig(configDir, func(path string, data []byte) ([]byte, error) {
		var m Machine
		if err := yaml.Unmarshal(data, &m); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		if err := change(&m); err != nil {
			return nil, err
		}
		if err := m.Validate(); err != nil {
			return nil, err
		}
		updated, err := withMachine(data, m)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		return updated, nil
	})
}

var legacyRemoteKeys = []string{"remotes", "default_remote", "push"}

func DropLegacyRemotes(configDir string) ([]string, error) {
	if _, err := os.Stat(filepath.Join(configDir, "config.yml")); err != nil {
		return nil, nil
	}
	var listed []string
	err := rewriteConfig(configDir, func(path string, data []byte) ([]byte, error) {
		file, err := parser.ParseBytes(data, parser.ParseComments)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		if !slices.ContainsFunc(legacyRemoteKeys, func(key string) bool { return topLevelEntry(file, key) != nil }) {
			return nil, nil
		}
		var old struct {
			Remotes map[string]struct {
				Host string `yaml:"host"`
			} `yaml:"remotes"`
		}
		yaml.Unmarshal(data, &old)
		for _, name := range slices.Sorted(maps.Keys(old.Remotes)) {
			listed = append(listed, name+" ("+old.Remotes[name].Host+")")
		}
		for _, key := range legacyRemoteKeys {
			removeKey(file, key)
		}
		if !slices.ContainsFunc(file.Docs, func(doc *ast.DocumentNode) bool { return len(entriesOf(doc.Body)) > 0 }) {
			return []byte{}, nil
		}
		return []byte(file.String()), nil
	})
	return listed, err
}

func rewriteConfig(configDir string, rewrite func(path string, data []byte) ([]byte, error)) error {
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(configDir, "config.yml")
	lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	updated, err := rewrite(path, data)
	if err != nil || updated == nil {
		return err
	}
	return WriteFileAtomic(path, updated)
}

func withMachine(data []byte, m Machine) ([]byte, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return yaml.Marshal(m)
	}
	file, err := parser.ParseBytes(data, parser.ParseComments)
	if err != nil {
		return nil, err
	}
	if holdsOnlyComments(file) {
		return afterComments(data, m)
	}
	keys := []struct {
		name  string
		value any
		zero  bool
	}{
		{"phone", m.Phone, m.Phone == Phone{}},
		{"node", m.Node, m.Node == Node{}},
	}
	for _, key := range keys {
		if err := setKey(file, key.name, key.value, key.zero); err != nil {
			return nil, err
		}
	}
	return []byte(file.String()), nil
}

func holdsOnlyComments(file *ast.File) bool {
	for _, doc := range file.Docs {
		switch doc.Body.(type) {
		case nil, *ast.CommentGroupNode, *ast.CommentNode:
		default:
			return false
		}
	}
	return true
}

func afterComments(comments []byte, m Machine) ([]byte, error) {
	if m.Equal(Machine{}) {
		return comments, nil
	}
	settings, err := yaml.Marshal(m)
	if err != nil {
		return nil, err
	}
	return slices.Concat(bytes.TrimRight(comments, "\n"), []byte("\n"), settings), nil
}

func setKey(file *ast.File, name string, value any, zero bool) error {
	if entry := topLevelEntry(file, name); entry != nil {
		fresh, err := entryOf(name, value)
		if err != nil {
			return err
		}
		entry.Value = fresh.Value
		return nil
	}
	if zero {
		return nil
	}
	node, err := yaml.ValueToNode(map[string]any{name: value})
	if err != nil {
		return err
	}
	root, err := yaml.PathString("$")
	if err != nil {
		return err
	}
	return root.MergeFromNode(file, node)
}

func removeKey(file *ast.File, name string) {
	for _, doc := range file.Docs {
		switch body := doc.Body.(type) {
		case *ast.MappingNode:
			body.Values = slices.DeleteFunc(body.Values, func(entry *ast.MappingValueNode) bool { return entry.Key.GetToken().Value == name })
		case *ast.MappingValueNode:
			if body.Key.GetToken().Value == name {
				doc.Body = nil
			}
		}
	}
}

func topLevelEntry(file *ast.File, name string) *ast.MappingValueNode {
	for _, doc := range file.Docs {
		for _, entry := range entriesOf(doc.Body) {
			if entry.Key.GetToken().Value == name {
				return entry
			}
		}
	}
	return nil
}

func entriesOf(body ast.Node) []*ast.MappingValueNode {
	switch n := body.(type) {
	case *ast.MappingNode:
		return n.Values
	case *ast.MappingValueNode:
		return []*ast.MappingValueNode{n}
	}
	return nil
}

func entryOf(name string, value any) (*ast.MappingValueNode, error) {
	data, err := yaml.Marshal(map[string]any{name: value})
	if err != nil {
		return nil, err
	}
	file, err := parser.ParseBytes(data, 0)
	if err != nil {
		return nil, err
	}
	entry := topLevelEntry(file, name)
	if entry == nil {
		return nil, fmt.Errorf("could not write %s", name)
	}
	return entry, nil
}
