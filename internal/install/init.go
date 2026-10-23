package install

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/goccy/go-yaml"
	"github.com/goccy/go-yaml/ast"
	"github.com/goccy/go-yaml/parser"

	"github.com/afonsodelgado/factory/bundle"
	"github.com/afonsodelgado/factory/internal/state"
)

var IgnoredPaths = []string{".claude/worktrees/", ".claude/factory/", ".claude/factory-archive/", ".claude/qa/", ".claude/factory.yml"}

type InitResult struct {
	Repo           string
	Profile        string
	ProfileWritten bool
	Excluded       []string
	ExcludeFile    string
	ConfigAdded    bool
	ConfigFile     string
}

func (r InitResult) Changed() bool {
	return r.ProfileWritten || len(r.Excluded) > 0 || r.ConfigAdded
}

func Init(path, configFile string) (InitResult, error) {
	r := InitResult{ConfigFile: configFile}
	repo, commonDir, err := MainCheckout(path)
	if err != nil {
		return r, err
	}
	r.Repo = repo
	r.Profile = state.ProfilePath(repo)
	if r.ProfileWritten, err = writeProfile(repo); err != nil {
		return r, err
	}
	r.ExcludeFile = filepath.Join(commonDir, "info", "exclude")
	if r.Excluded, err = excludeFactoryFiles(repo, r.ExcludeFile); err != nil {
		return r, err
	}
	r.ConfigAdded, err = addRepo(configFile, repo)
	return r, err
}

func MainCheckout(path string) (repo, commonDir string, err error) {
	out, err := exec.Command("git", "-C", path, "rev-parse", "--path-format=absolute", "--git-common-dir").Output()
	if err != nil {
		return "", "", fmt.Errorf("%s is not inside a git repository", path)
	}
	commonDir = strings.TrimSpace(string(out))
	return filepath.Dir(commonDir), commonDir, nil
}

func writeProfile(repo string) (bool, error) {
	target := state.ProfilePath(repo)
	if _, err := os.Stat(target); err == nil {
		return false, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return false, err
	}
	profile, err := detectProfile(repo)
	if err != nil {
		return false, err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return false, err
	}
	return true, os.WriteFile(target, profile, 0o644)
}

func detectProfile(repo string) ([]byte, error) {
	template, err := fs.ReadFile(bundle.Files, "skills/factory/templates/factory.yml")
	if err != nil {
		return nil, err
	}
	file, err := parser.ParseBytes(template, parser.ParseComments)
	if err != nil {
		return nil, err
	}
	commands, browser, detected := detectCommands(repo)
	edits := map[string]string{"$.coderabbit": "false"}
	if detected {
		edits["$.commands"] = mappingYAML(commands)
		if !browser {
			edits["$.qa"] = mappingYAML(yaml.MapSlice{{Key: "mode", Value: "terminal"}, {Key: "notes", Value: ""}})
		}
	}
	for at, value := range edits {
		path, err := yaml.PathString(at)
		if err != nil {
			return nil, err
		}
		if err := path.ReplaceWithReader(file, strings.NewReader(value)); err != nil {
			return nil, err
		}
	}
	return []byte(strings.TrimRight(file.String(), "\n") + "\n"), nil
}

func detectCommands(repo string) (commands yaml.MapSlice, browser, detected bool) {
	if exists(filepath.Join(repo, "go.mod")) {
		return yaml.MapSlice{{Key: "build", Value: "go build ./..."}, {Key: "lint", Value: "go vet ./..."}, {Key: "test", Value: "go test ./..."}}, false, true
	}
	data, err := os.ReadFile(filepath.Join(repo, "package.json"))
	if err != nil {
		return nil, false, false
	}
	var pkg struct {
		Scripts map[string]string `json:"scripts"`
	}
	json.Unmarshal(data, &pkg)
	for _, s := range []struct{ key, script string }{{"build", "build"}, {"lint", "lint"}, {"test", "test"}, {"migrate", "db:migrate"}} {
		if _, ok := pkg.Scripts[s.script]; ok {
			commands = append(commands, yaml.MapItem{Key: s.key, Value: "npm run " + s.script})
		}
	}
	_, hasDev := pkg.Scripts["dev"]
	return commands, hasDev, true
}

func mappingYAML(m yaml.MapSlice) string {
	if len(m) == 0 {
		return "{}"
	}
	out, _ := yaml.Marshal(m)
	return string(out)
}

func excludeFactoryFiles(repo, excludeFile string) ([]string, error) {
	existing, err := os.ReadFile(excludeFile)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	listed := map[string]bool{}
	for _, line := range strings.Split(string(existing), "\n") {
		listed[strings.TrimSpace(line)] = true
	}
	var added []string
	for _, p := range IgnoredPaths {
		if listed[p] {
			continue
		}
		ignored, err := gitIgnores(repo, p)
		if err != nil {
			return nil, err
		}
		if !ignored {
			added = append(added, p)
		}
	}
	if len(added) == 0 {
		return nil, nil
	}
	text := string(existing)
	if text != "" && !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	text += strings.Join(added, "\n") + "\n"
	if err := os.MkdirAll(filepath.Dir(excludeFile), 0o755); err != nil {
		return nil, err
	}
	return added, os.WriteFile(excludeFile, []byte(text), 0o644)
}

func gitIgnores(repo, path string) (bool, error) {
	probe := path
	if strings.HasSuffix(path, "/") {
		probe += "probe"
	}
	err := exec.Command("git", "-C", repo, "check-ignore", "--no-index", "-q", probe).Run()
	var exit *exec.ExitError
	switch {
	case err == nil:
		return true, nil
	case errors.As(err, &exit) && exit.ExitCode() == 1:
		return false, nil
	}
	return false, fmt.Errorf("git check-ignore %s: %w", path, err)
}

func addRepo(configFile, repo string) (bool, error) {
	data, err := os.ReadFile(configFile)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return false, err
	}
	listed, err := configuredRepos(data)
	if err != nil {
		return false, fmt.Errorf("%s: %w", configFile, err)
	}
	for _, r := range listed {
		if samePath(state.ExpandHome(r), repo) {
			return false, nil
		}
	}
	entry := HomeRelative(repo)
	updated, err := withRepo(data, entry)
	if err != nil {
		return false, fmt.Errorf("%s: %w; add %s to repos by hand", configFile, err, entry)
	}
	if after, err := configuredRepos(updated); err != nil || len(after) != len(listed)+1 || after[len(after)-1] != entry {
		return false, fmt.Errorf("%s: could not add %s to repos without changing the rest of the file; add it by hand", configFile, entry)
	}
	if err := os.MkdirAll(filepath.Dir(configFile), 0o755); err != nil {
		return false, err
	}
	return true, os.WriteFile(configFile, updated, 0o644)
}

func configuredRepos(data []byte) ([]string, error) {
	var cfg struct {
		Repos []string `yaml:"repos"`
	}
	err := yaml.Unmarshal(data, &cfg)
	return cfg.Repos, err
}

func withRepo(data []byte, entry string) ([]byte, error) {
	quoted, err := yaml.Marshal(entry)
	if err != nil {
		return nil, err
	}
	scalar := strings.TrimSpace(string(quoted))
	if len(strings.TrimSpace(string(data))) == 0 {
		return []byte(string(data) + "repos:\n  - " + scalar + "\n"), nil
	}
	file, err := parser.ParseBytes(data, parser.ParseComments)
	if err != nil {
		return nil, err
	}
	path, err := yaml.PathString("$.repos")
	if err != nil {
		return nil, err
	}
	node, err := path.FilterFile(file)
	if err != nil {
		text := string(data)
		if !strings.HasSuffix(text, "\n") {
			text += "\n"
		}
		return []byte(text + "repos:\n  - " + scalar + "\n"), nil
	}
	switch seq, isSequence := node.(*ast.SequenceNode); {
	case isSequence && seq.IsFlowStyle:
		err = path.MergeFromReader(file, strings.NewReader("["+scalar+"]"))
	case isSequence:
		err = path.MergeFromReader(file, strings.NewReader("- "+scalar))
	default:
		err = path.ReplaceWithReader(file, strings.NewReader("["+scalar+"]"))
	}
	if err != nil {
		return nil, err
	}
	return []byte(strings.TrimRight(file.String(), "\n") + "\n"), nil
}

func samePath(a, b string) bool {
	ra, errA := filepath.EvalSymlinks(a)
	rb, errB := filepath.EvalSymlinks(b)
	if errA != nil || errB != nil {
		return filepath.Clean(a) == filepath.Clean(b)
	}
	return ra == rb
}

func HomeRelative(path string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return path
	}
	homes := []string{home}
	if resolved, err := filepath.EvalSymlinks(home); err == nil {
		homes = append(homes, resolved)
	}
	for _, h := range homes {
		if rel, ok := strings.CutPrefix(path, h+string(filepath.Separator)); ok {
			return "~/" + rel
		}
	}
	return path
}
