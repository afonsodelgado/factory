package state

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

type StageRun struct {
	ID        string       `json:"id"`
	Attempt   int          `json:"attempt"`
	Role      string       `json:"role"`
	Round     int          `json:"round"`
	Setting   AgentSetting `json:"setting"`
	Token     string       `json:"token"`
	Prompt    string       `json:"prompt"`
	Result    string       `json:"result"`
	StartedAt time.Time    `json:"started_at"`
}

func (r StageRun) Finished() bool {
	data, err := os.ReadFile(r.Result)
	if err != nil {
		return false
	}
	var result struct {
		SchemaVersion int    `json:"schema_version"`
		RunID         string `json:"run_id"`
		Status        string `json:"status"`
	}
	if json.Unmarshal(data, &result) != nil {
		return false
	}
	return r.ID != "" && result.SchemaVersion == 2 && result.RunID == r.ID && (result.Status == "done" || result.Status == "blocked" || result.Status == "failed")
}

type StageRuns struct {
	path string
	Runs []StageRun `json:"runs"`
}

func StageRunsPath(stateDir, repo, slug string) string {
	return filepath.Join(stateDir, "stages", FileStem(repo, slug)+".json")
}

const SupervisorLaunch = "supervisor"

func LaunchScriptPath(stateDir, repo, slug, role string) string {
	return filepath.Join(stateDir, "launch", FileStem(repo, slug)+"-"+role+".sh")
}

func LaunchScriptPaths(stateDir, repo, slug string) []string {
	var paths []string
	for _, role := range append([]string{SupervisorLaunch}, AgentRoles...) {
		paths = append(paths, LaunchScriptPath(stateDir, repo, slug, role))
	}
	return paths
}

func LoadStageRuns(path string) (*StageRuns, error) {
	runs := &StageRuns{path: path}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return runs, nil
	}
	if err != nil {
		return nil, err
	}
	return runs, json.Unmarshal(data, runs)
}

func (s *StageRuns) Save() error {
	data, err := json.Marshal(s)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	return WriteFileAtomic(s.path, data)
}

func (s *StageRuns) Latest(role string) (StageRun, bool) {
	for i := len(s.Runs) - 1; i >= 0; i-- {
		if s.Runs[i].Role == role {
			return s.Runs[i], true
		}
	}
	return StageRun{}, false
}

func (s *StageRuns) NextRound(role string) int {
	latest, _ := s.Latest(role)
	return latest.Round + 1
}

func (s *StageRuns) Running() (StageRun, bool) {
	superseded := map[string]bool{}
	for i := len(s.Runs) - 1; i >= 0; i-- {
		run := s.Runs[i]
		if superseded[run.Role] {
			continue
		}
		superseded[run.Role] = true
		if !run.Finished() {
			return run, true
		}
	}
	return StageRun{}, false
}

func (s *StageRuns) Tokens() []string {
	var tokens []string
	for _, run := range s.Runs {
		tokens = append(tokens, run.Token)
	}
	return tokens
}
