package install

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/afonsodelgado/factory/bundle"
	"github.com/afonsodelgado/factory/internal/state"
)

type InitResult struct {
	Repo           string
	Profile        string
	ProfileWritten bool
	CopiedFrom     string
	Excluded       []string
	ExcludeFile    string
	Remembered     bool
}

func (r InitResult) Changed() bool {
	return r.ProfileWritten || len(r.Excluded) > 0 || r.Remembered
}

func Init(path, stateDir string) (InitResult, error) {
	var r InitResult
	repo, commonDir, err := MainCheckout(path)
	if err != nil {
		return r, err
	}
	r.Repo = repo
	r.Profile = state.RepoConfigPath(repo)
	if r.CopiedFrom, r.ProfileWritten, err = writeProfile(repo); err != nil {
		return r, err
	}
	r.ExcludeFile = filepath.Join(commonDir, "info", "exclude")
	if r.Excluded, err = excludeFactoryFiles(repo, r.ExcludeFile); err != nil {
		return r, err
	}
	r.Remembered, err = state.RememberRepo(stateDir, repo)
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

func writeProfile(repo string) (copiedFrom string, written bool, err error) {
	target := state.RepoConfigPath(repo)
	if _, err := os.Stat(target); err == nil {
		return "", false, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", false, err
	}
	legacy := state.LegacyProfilePath(repo)
	profile, err := os.ReadFile(legacy)
	switch {
	case err == nil:
		copiedFrom = legacy
	case errors.Is(err, fs.ErrNotExist):
		if profile, err = detectProfile(repo); err != nil {
			return "", false, err
		}
	default:
		return "", false, err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return "", false, err
	}
	return copiedFrom, true, os.WriteFile(target, profile, 0o644)
}

func detectProfile(repo string) ([]byte, error) {
	template, err := fs.ReadFile(bundle.Files, "skills/factory/templates/factory.yml")
	if err != nil || !servesWeb(repo) {
		return template, err
	}
	template = bytes.Replace(template, []byte("  source: terminal\n"), []byte("  source: web\n"), 1)
	return bytes.Replace(template, []byte("  mode: terminal\n"), []byte("  mode: browser\n"), 1), nil
}

func servesWeb(repo string) bool {
	data, err := os.ReadFile(filepath.Join(repo, "package.json"))
	if err != nil {
		return false
	}
	var pkg struct {
		Scripts map[string]string `json:"scripts"`
	}
	return json.Unmarshal(data, &pkg) == nil && pkg.Scripts["dev"] != ""
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
	for _, p := range state.FactoryPaths {
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
