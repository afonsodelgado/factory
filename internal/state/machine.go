package state

import (
	"bytes"
	"cmp"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"net/url"
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

const (
	DefaultPushServer = "https://ntfy.sh"
	DefaultPhonePort  = 7418
)

type Machine struct {
	Remotes       map[string]Remote `yaml:"remotes,omitempty"`
	DefaultRemote string            `yaml:"default_remote,omitempty"`
	Push          Push              `yaml:"push,omitempty"`
	Phone         Phone             `yaml:"phone,omitempty"`
	Node          Node              `yaml:"node,omitempty"`
}

type Node struct {
	Owner    string `yaml:"owner,omitempty"`
	Port     int    `yaml:"port,omitempty"`
	Hostname string `yaml:"hostname,omitempty"`
}

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

type Remote struct {
	Host     string `yaml:"host"`
	User     string `yaml:"user,omitempty"`
	Port     int    `yaml:"port,omitempty"`
	Identity string `yaml:"identity,omitempty"`
}

type Push struct {
	Server string `yaml:"server,omitempty"`
	Topic  string `yaml:"topic,omitempty"`
}

func (p Push) On() bool { return p.Topic != "" }

func (p Push) ServerURL() string {
	if p.Server == "" {
		return DefaultPushServer
	}
	return strings.TrimRight(p.Server, "/")
}

var (
	remoteName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)
	sshHost    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]*$`)
	sshUser    = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]*$`)
	pushTopic  = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	tailnetDNS = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?\.[a-z0-9]([a-z0-9-]*[a-z0-9])?\.ts\.net$`)
)

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

func (m Machine) Equal(other Machine) bool {
	return maps.Equal(m.Remotes, other.Remotes) && m.DefaultRemote == other.DefaultRemote && m.Push == other.Push && m.Phone == other.Phone && m.Node == other.Node
}

func (m Machine) Validate() error {
	for name, r := range m.Remotes {
		if err := r.validate(name); err != nil {
			return err
		}
	}
	if _, known := m.Remotes[m.DefaultRemote]; m.DefaultRemote != "" && !known {
		return fmt.Errorf("default_remote %q names no remote; add it under remotes or remove default_remote to create factories here", m.DefaultRemote)
	}
	if m.Push.Server != "" {
		if u, err := url.Parse(m.Push.Server); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			return fmt.Errorf("push.server %q is not an http(s) URL such as %s", m.Push.Server, DefaultPushServer)
		}
	}
	if m.Push.Topic != "" && !pushTopic.MatchString(m.Push.Topic) {
		return errors.New("push.topic may hold only letters, digits, - and _; run factory remote push to generate one")
	}
	if m.Node.Port < 0 || m.Node.Port > 65535 {
		return fmt.Errorf("node.port %d must be between 1 and 65535", m.Node.Port)
	}
	if m.Node.Hostname != "" && !tailnetDNS.MatchString(m.Node.Hostname) {
		return fmt.Errorf("node.hostname %q must be this machine's full Tailscale name, such as devbox.tail1234.ts.net", m.Node.Hostname)
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

func (r Remote) validate(name string) error {
	switch {
	case !remoteName.MatchString(name) || name == "local":
		return fmt.Errorf("remote name %q must be lowercase letters, digits and -, and not local", name)
	case !sshHost.MatchString(r.Host):
		return fmt.Errorf("remotes.%s.host %q must be a host name or address", name, r.Host)
	case r.User != "" && !sshUser.MatchString(r.User):
		return fmt.Errorf("remotes.%s.user %q is not a user name", name, r.User)
	case r.Port < 0 || r.Port > 65535:
		return fmt.Errorf("remotes.%s.port %d must be between 1 and 65535", name, r.Port)
	case strings.HasPrefix(r.Identity, "-"):
		return fmt.Errorf("remotes.%s.identity %q must be a key file", name, r.Identity)
	}
	return nil
}

func UpdateMachine(configDir string, change func(*Machine) error) error {
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
	var m Machine
	if err := yaml.Unmarshal(data, &m); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	if err := change(&m); err != nil {
		return err
	}
	if err := m.Validate(); err != nil {
		return err
	}
	updated, err := withMachine(data, m)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
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
	remotes := m.Remotes
	if remotes == nil {
		remotes = map[string]Remote{}
	}
	keys := []struct {
		name  string
		value any
		zero  bool
	}{
		{"remotes", remotes, len(m.Remotes) == 0},
		{"default_remote", m.DefaultRemote, m.DefaultRemote == ""},
		{"push", m.Push, m.Push == Push{}},
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
