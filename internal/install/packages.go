package install

import (
	"cmp"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

type Manager struct {
	Name    string
	Install []string
	Refresh []string
	names   map[string]string
}

var managers = []Manager{
	{Name: "pacman", Install: []string{"sudo", "pacman", "-S", "--needed", "--noconfirm"}, names: map[string]string{"gh": "github-cli", "node": "nodejs npm", "python3": "python"}},
	{Name: "apt-get", Install: []string{"sudo", "apt-get", "install", "-y"}, Refresh: []string{"sudo", "apt-get", "update"}, names: map[string]string{"node": "nodejs npm", "docker": "docker-compose-v2"}},
	{Name: "dnf", Install: []string{"sudo", "dnf", "install", "-y"}, names: map[string]string{"node": "nodejs npm"}},
	{Name: "zypper", Install: []string{"sudo", "zypper", "install", "-y"}, names: map[string]string{"node": "nodejs npm"}},
}

var brew = Manager{Name: "brew", Install: []string{"brew", "install"}, names: map[string]string{"python3": "python"}}

func PackageManager() (Manager, bool) {
	if runtime.GOOS == "darwin" {
		return brew, hasCommand("brew")
	}
	for _, m := range managers {
		if hasCommand(m.Name) {
			return m, true
		}
	}
	return Manager{}, false
}

func (m Manager) Packages(tools ...string) []string {
	var pkgs []string
	for _, tool := range tools {
		pkgs = append(pkgs, strings.Fields(cmp.Or(m.names[tool], tool))...)
	}
	return pkgs
}

func (m Manager) Command(tools ...string) []string {
	return append(append([]string{}, m.Install...), m.Packages(tools...)...)
}

func installCommand(tool string) string {
	m, ok := PackageManager()
	if !ok {
		return "install " + tool + " with your package manager"
	}
	return strings.Join(m.Command(tool), " ")
}

type Plugin struct {
	Name        string
	ID          string
	Marketplace string
	Repo        string
	Purpose     string
}

var Plugins = []Plugin{
	{Name: "revmux", ID: "revmux@revmux", Marketplace: "revmux", Repo: "umputun/revmux", Purpose: "review stage"},
	{Name: "impeccable", ID: "impeccable@impeccable", Marketplace: "impeccable", Repo: "pbakaus/impeccable", Purpose: "design stage"},
}

func PluginNamed(name string) Plugin {
	for _, p := range Plugins {
		if p.Name == name {
			return p
		}
	}
	panic("no plugin " + name)
}

func (p Plugin) Fix(claudeDir string) string {
	if KnownMarketplaces(claudeDir)[p.Marketplace] {
		return "claude plugin install " + p.ID
	}
	return "claude plugin marketplace add " + p.Repo + "  &&  claude plugin install " + p.ID
}

func KnownMarketplaces(claudeDir string) map[string]bool {
	data, err := os.ReadFile(filepath.Join(claudeDir, "plugins", "known_marketplaces.json"))
	if err != nil {
		return nil
	}
	var known map[string]json.RawMessage
	if json.Unmarshal(data, &known) != nil {
		return nil
	}
	names := map[string]bool{}
	for name := range known {
		names[name] = true
	}
	return names
}

func RevmuxAsset() string {
	return fmt.Sprintf("revmux_*_%s_%s.tar.gz", runtime.GOOS, runtime.GOARCH)
}

func RevmuxInstall() string {
	return fmt.Sprintf("mkdir -p ~/.local/bin && gh release download --repo umputun/revmux --pattern '%s' -O - | tar -xz -C ~/.local/bin revmux", RevmuxAsset())
}
