package state

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
)

type knownRepos struct {
	Repos []string `json:"repos"`
}

func KnownReposPath(stateDir string) string {
	return filepath.Join(stateDir, "repos.json")
}

func LoadKnownRepos(stateDir string) ([]string, error) {
	data, err := os.ReadFile(KnownReposPath(stateDir))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var known knownRepos
	if err := json.Unmarshal(data, &known); err != nil {
		return nil, err
	}
	return known.Repos, nil
}

func RememberRepo(stateDir, repo string) (bool, error) {
	return updateKnownRepos(stateDir, func(repos []string) []string {
		if slices.Contains(repos, repo) {
			return repos
		}
		return append(repos, repo)
	})
}

func ForgetRepo(stateDir, repo string) (bool, error) {
	return updateKnownRepos(stateDir, func(repos []string) []string {
		return slices.DeleteFunc(repos, func(r string) bool { return r == repo })
	})
}

func updateKnownRepos(stateDir string, change func([]string) []string) (bool, error) {
	before, err := LoadKnownRepos(stateDir)
	if err != nil {
		return false, err
	}
	after := change(slices.Clone(before))
	if slices.Equal(before, after) {
		return false, nil
	}
	data, err := json.MarshalIndent(knownRepos{Repos: after}, "", "  ")
	if err != nil {
		return false, err
	}
	return true, WriteFileAtomic(KnownReposPath(stateDir), data)
}

func (c *Config) addKnownRepos() error {
	known, err := LoadKnownRepos(c.StateDir)
	if err != nil {
		return err
	}
	for _, repo := range known {
		if info, err := os.Stat(repo); err == nil && info.IsDir() && !listsDir(c.Repos, info) {
			c.Repos = append(c.Repos, repo)
		}
	}
	return nil
}

func listsDir(repos []string, dir os.FileInfo) bool {
	return slices.ContainsFunc(repos, func(repo string) bool {
		info, err := os.Stat(repo)
		return err == nil && os.SameFile(info, dir)
	})
}
