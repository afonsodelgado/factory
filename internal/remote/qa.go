package remote

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/afonsodelgado/factory/internal/state"
)

const (
	QAHeld      = "held"
	QAClaimed   = "claimed"
	QAAccepted  = "accepted"
	QACancelled = "cancelled"

	QAReturnable = ".claude/"
	qaFileLimit  = 10 << 20
	qaTotalLimit = 50 << 20
)

type QAJob struct {
	ID          string             `json:"id"`
	Factory     string             `json:"factory"`
	Incarnation string             `json:"incarnation"`
	Kind        string             `json:"kind"`
	Round       int                `json:"round"`
	GitURL      string             `json:"git_url"`
	Branch      string             `json:"branch"`
	SHA         string             `json:"sha"`
	Setting     state.AgentSetting `json:"setting"`
	Root        string             `json:"root"`
	Prompt      string             `json:"prompt"`
	Result      string             `json:"result"`
	Inputs      []QAFile           `json:"inputs,omitempty"`
	Phase       string             `json:"phase"`
	Runner      string             `json:"runner,omitempty"`
	Token       string             `json:"token,omitempty"`
	CreatedAt   time.Time          `json:"created_at"`
}

type QAFile struct {
	Path   string `json:"path"`
	Data   []byte `json:"data"`
	SHA256 string `json:"sha256"`
}

type QAClaim struct {
	Job    string `json:"job"`
	Runner string `json:"runner"`
}

type QAReturn struct {
	Job    string          `json:"job"`
	Token  string          `json:"token"`
	SHA    string          `json:"sha"`
	Result json.RawMessage `json:"result"`
	Files  []QAFile        `json:"files,omitempty"`
}

func (j QAJob) Start() string {
	switch j.Kind {
	case "recheck":
		return "Start re-check"
	case "walkthrough":
		return "Start walkthrough"
	}
	return "Start QA on this laptop"
}

func (j QAJob) Public() QAJob {
	j.Inputs, j.Token = nil, ""
	return j
}

func NewQAFile(path string, data []byte) QAFile {
	sum := sha256.Sum256(data)
	return QAFile{Path: path, Data: data, SHA256: hex.EncodeToString(sum[:])}
}

func CheckQAFiles(files []QAFile) error {
	total := 0
	for _, f := range files {
		total += len(f.Data)
		switch {
		case !returnable(f.Path):
			return fmt.Errorf("%q is not a report or evidence file under %s", f.Path, QAReturnable)
		case len(f.Data) > qaFileLimit || total > qaTotalLimit:
			return fmt.Errorf("%s is over the size limit for returned QA evidence", f.Path)
		case NewQAFile(f.Path, f.Data).SHA256 != f.SHA256:
			return fmt.Errorf("%s arrived damaged: its hash does not match", f.Path)
		}
	}
	return nil
}

func returnable(rel string) bool {
	return !filepath.IsAbs(rel) && filepath.Clean(rel) == rel && strings.HasPrefix(rel, QAReturnable) && filepath.Base(rel) != "state.json"
}

func LocalCheckout(repos []string, gitURL string) (string, bool) {
	for _, repo := range repos {
		if url, err := GitURL(repo, ""); err == nil && url == gitURL {
			return repo, true
		}
	}
	return "", false
}

func qaRef(job QAJob) string { return "refs/factory-qa/" + job.ID }

func CheckOutQA(ctx context.Context, repo, dir string, job QAJob) error {
	if _, err := git(ctx, repo, "fetch", "--no-tags", "--", job.GitURL, "+refs/heads/"+job.Branch+":"+qaRef(job)); err != nil {
		return err
	}
	if _, err := git(ctx, repo, "cat-file", "-e", job.SHA+"^{commit}"); err != nil {
		return fmt.Errorf("%s no longer has %s on %s; the server publishes the next one", job.GitURL, job.SHA[:7], job.Branch)
	}
	if _, err := git(ctx, repo, "worktree", "add", "--detach", dir, job.SHA); err != nil {
		return err
	}
	head, err := git(ctx, dir, "rev-parse", "HEAD")
	if err == nil && head != job.SHA {
		err = fmt.Errorf("the QA worktree is at %s, not %s", head, job.SHA)
	}
	return err
}

func WriteQAInputs(dir string, job QAJob) error {
	for _, f := range job.Inputs {
		if filepath.IsAbs(f.Path) || !filepath.IsLocal(f.Path) {
			return fmt.Errorf("the server sent the input %q outside the worktree", f.Path)
		}
		path := filepath.Join(dir, f.Path)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(path, []byte(strings.ReplaceAll(string(f.Data), job.Root, dir)), 0o644); err != nil {
			return err
		}
	}
	return nil
}

func RemoveQA(ctx context.Context, repo, dir string, job QAJob) error {
	var errs []error
	if _, err := os.Stat(dir); err == nil {
		_, err := git(ctx, repo, "worktree", "remove", "--force", dir)
		errs = append(errs, err)
	}
	_, err := git(ctx, repo, "update-ref", "-d", qaRef(job))
	return errors.Join(append(errs, err)...)
}

func QAReport(dir string, job QAJob) (QAReturn, bool, error) {
	raw, err := os.ReadFile(filepath.Join(dir, job.Result))
	if errors.Is(err, fs.ErrNotExist) || (err == nil && !json.Valid(raw)) {
		return QAReturn{}, false, nil
	}
	if err != nil {
		return QAReturn{}, false, err
	}
	var result struct {
		Outputs []string `json:"outputs"`
	}
	json.Unmarshal(raw, &result)
	r := QAReturn{Job: job.ID, Token: job.Token, SHA: job.SHA, Result: json.RawMessage(strings.ReplaceAll(string(raw), dir, job.Root))}
	for _, out := range result.Outputs {
		rel, err := filepath.Rel(dir, out)
		if err != nil || !returnable(rel) || slices.ContainsFunc(r.Files, func(f QAFile) bool { return f.Path == rel }) {
			continue
		}
		file, err := regularFile(filepath.Join(dir, rel))
		if err != nil {
			return QAReturn{}, false, err
		}
		r.Files = append(r.Files, NewQAFile(rel, file))
	}
	return r, true, nil
}

func regularFile(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file, so it is not returned", path)
	}
	return os.ReadFile(path)
}

func FailedQA(job QAJob, why string) QAReturn {
	result, _ := json.Marshal(map[string]any{"status": "failed", "summary": why, "outputs": []string{}, "questions": []string{}, "data": map[string]any{}})
	return QAReturn{Job: job.ID, Token: job.Token, SHA: job.SHA, Result: result}
}

type QARun struct {
	Remote   string    `json:"remote"`
	Key      string    `json:"key"`
	Slug     string    `json:"slug"`
	Repo     string    `json:"repo"`
	Dir      string    `json:"dir"`
	Job      QAJob     `json:"job"`
	Launched time.Time `json:"launched,omitzero"`
}

func qaRunsPath(stateDir string) string { return filepath.Join(stateDir, "remotes", "qa.json") }

func QADir(stateDir, jobID string) string { return filepath.Join(stateDir, "qa", jobID) }

func LoadQARuns(stateDir string) []QARun {
	runs := []QARun{}
	if data, err := os.ReadFile(qaRunsPath(stateDir)); err == nil {
		json.Unmarshal(data, &runs)
	}
	return runs
}

func SaveQARuns(stateDir string, runs []QARun) error {
	path := qaRunsPath(stateDir)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(runs)
	if err != nil {
		return err
	}
	return state.WriteFileAtomic(path, data)
}
