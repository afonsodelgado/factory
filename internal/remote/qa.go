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
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/afonsodelgado/factory/internal/state"
)

const (
	QAHeld      = "held"
	QAClaimed   = "claimed"
	QAAccepted  = "accepted"
	QACancelled = "cancelled"

	QAReturnable = ".claude/qa/"
	qaFileLimit  = 10 << 20
	QATotalLimit = 50 << 20
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
	Stack       []string           `json:"stack,omitempty"`
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

var (
	settingValue = regexp.MustCompile(`^[A-Za-z0-9._:/@+-]*$`)
	commitHash   = regexp.MustCompile(`^[0-9a-f]{40,64}$`)
)

func (j QAJob) Validate() error {
	if _, err := uuid.Parse(j.ID); err != nil {
		return errors.New("the server sent a QA job whose id is not a UUID")
	}
	if !commitHash.MatchString(j.SHA) {
		return errors.New("the server sent a QA job whose commit is not a full hash")
	}
	if !filepath.IsLocal(j.Prompt) || !filepath.IsLocal(j.Result) {
		return errors.New("the server sent a QA job with a prompt or result path outside the worktree")
	}
	for _, value := range []string{j.Setting.Tool, j.Setting.Model, j.Setting.Effort} {
		if !settingValue.MatchString(value) {
			return fmt.Errorf("the server sent a QA job whose agent setting %q is not a plain model or effort name", value)
		}
	}
	return nil
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

func CheckQAFiles(job QAJob, files []QAFile) error {
	total := 0
	for _, f := range files {
		total += len(f.Data)
		switch {
		case !returnable(job, f.Path):
			return fmt.Errorf("%q is not a report or evidence file under %s or a stack file this job asked for", f.Path, QAReturnable)
		case len(f.Data) > qaFileLimit || total > QATotalLimit:
			return fmt.Errorf("%s is over the size limit for returned QA evidence", f.Path)
		case NewQAFile(f.Path, f.Data).SHA256 != f.SHA256:
			return fmt.Errorf("%s arrived damaged: its hash does not match", f.Path)
		}
	}
	return nil
}

func returnable(job QAJob, rel string) bool {
	inside := strings.HasPrefix(rel, QAReturnable) && filepath.Base(rel) != "state.json"
	return !filepath.IsAbs(rel) && filepath.Clean(rel) == rel && (inside || slices.Contains(job.Stack, rel))
}

func LocalCheckout(repos []string, gitURL string) (string, bool) {
	for _, repo := range repos {
		if hasRemoteURL(repo, gitURL) {
			return repo, true
		}
	}
	return "", false
}

func hasRemoteURL(repo, gitURL string) bool {
	names, err := git(context.Background(), repo, "remote")
	if err != nil {
		return false
	}
	for _, name := range strings.Fields(names) {
		if url, err := git(context.Background(), repo, "config", "--get", "remote."+name+".url"); err == nil && url == gitURL {
			return true
		}
	}
	return false
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
	serverPaths := strings.NewReplacer(resolved(dir), job.Root, dir, job.Root)
	r := QAReturn{Job: job.ID, Token: job.Token, SHA: job.SHA, Result: json.RawMessage(serverPaths.Replace(string(raw)))}
	for _, out := range result.Outputs {
		if !filepath.IsAbs(out) {
			out = filepath.Join(dir, out)
		}
		rel, err := filepath.Rel(resolved(dir), resolved(out))
		if err != nil || !returnable(job, rel) || slices.ContainsFunc(r.Files, func(f QAFile) bool { return f.Path == rel }) {
			continue
		}
		file, err := regularFile(filepath.Join(dir, rel))
		if err != nil {
			return QAReturn{}, false, err
		}
		if slices.Contains(job.Stack, rel) {
			file = []byte(serverPaths.Replace(string(file)))
		}
		r.Files = append(r.Files, NewQAFile(rel, file))
	}
	if err := CheckQAFiles(job, r.Files); err != nil {
		return QAReturn{}, false, err
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

func Failure(r QAReturn) string {
	var result struct {
		Status  string `json:"status"`
		Summary string `json:"summary"`
	}
	json.Unmarshal(r.Result, &result)
	if result.Status != "failed" {
		return ""
	}
	return result.Summary
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
