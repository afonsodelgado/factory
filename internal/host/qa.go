package host

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/afonsodelgado/factory/internal/factory"
	"github.com/afonsodelgado/factory/internal/remote"
	"github.com/afonsodelgado/factory/internal/state"
)

const qaInputLimit = 1 << 20

var publishCheckWait = 20 * time.Second

func (m *Runtime) qaPath() string { return filepath.Join(m.Config.StateDir, "qa.json") }

func (m *Runtime) QAJobs() []remote.QAJob {
	var jobs []remote.QAJob
	if data, err := os.ReadFile(m.qaPath()); err == nil {
		json.Unmarshal(data, &jobs)
	}
	return jobs
}

func (m *Runtime) saveQA(job remote.QAJob) error {
	jobs := slices.DeleteFunc(m.QAJobs(), func(j remote.QAJob) bool { return j.ID == job.ID })
	return savePrivate(m.qaPath(), append(jobs, job))
}

func (m *Runtime) qaJob(id string) (remote.QAJob, error) {
	jobs := m.QAJobs()
	if i := slices.IndexFunc(jobs, func(j remote.QAJob) bool { return j.ID == id }); i >= 0 {
		return jobs[i], nil
	}
	return remote.QAJob{}, fmt.Errorf("this server has no QA job %s", id)
}

func openJob(job remote.QAJob) bool {
	return job.Phase == remote.QAHeld || job.Phase == remote.QAClaimed
}

func (m *Runtime) withQA(s Snapshot) Snapshot {
	for _, job := range m.QAJobs() {
		if !openJob(job) {
			continue
		}
		for i, it := range s.Items {
			if Incarnation(it.Factory) != job.Incarnation {
				continue
			}
			s.Items[i].QA = &job
			if job.Phase == remote.QAHeld {
				s.Items[i].Status = factory.NeedsYou
			}
		}
	}
	return s
}

func (m *Runtime) heldForLaptop(it Item, role string) bool {
	_, profile := m.repoConfig(it.Factory.Repo)
	designing := it.Factory.State != nil && it.Factory.State.Stage == "design"
	return role == "qa" && profile.BrowserQA() && m.clonedForLaptop(it.Factory.Repo) && !designing
}

func laptopKind(f factory.Factory) string {
	if f.State == nil {
		return "qa"
	}
	switch f.State.Stage {
	case "qa_fixes":
		return "recheck"
	case "walkthrough":
		return "walkthrough"
	}
	return "qa"
}

func (m *Runtime) HoldQA(it Item, runs *state.StageRuns, run state.StageRun, prompt string) (StageReply, error) {
	f := it.Factory
	gitURL, branch, sha, err := publishedHead(f.Worktree)
	if err != nil {
		return StageReply{}, err
	}
	promptPath, err := filepath.Rel(f.Worktree, run.Prompt)
	if err != nil {
		return StageReply{}, err
	}
	resultPath, err := filepath.Rel(f.Worktree, run.Result)
	if err != nil {
		return StageReply{}, err
	}
	m.cancelQA(Incarnation(f))
	job := remote.QAJob{
		ID: uuid.NewString(), Factory: f.Label(), Incarnation: Incarnation(f), Kind: laptopKind(f), Round: run.Round,
		GitURL: gitURL, Branch: branch, SHA: sha, Setting: run.Setting, Root: f.Worktree, Prompt: promptPath, Result: resultPath,
		Inputs: qaInputs(f.Worktree, f.Dir, promptPath, prompt), Phase: remote.QAHeld, CreatedAt: m.Now(),
	}
	if job.Kind == "walkthrough" {
		job.Stack = stackFiles(f)
	}
	if err := m.saveQA(job); err != nil {
		return StageReply{}, err
	}
	runs.Runs = append(runs.Runs, run)
	if err := runs.Save(); err != nil {
		return StageReply{}, err
	}
	m.note(f, fmt.Sprintf("QA waits for the laptop: %s of %s at %s", job.Kind, branch, sha[:7]))
	return StageReply{Result: run.Result}, nil
}

func publishedHead(worktree string) (string, string, string, error) {
	sha, err := Git(worktree, "rev-parse", "HEAD")
	if err != nil {
		return "", "", "", fmt.Errorf("git rev-parse: %s", sha)
	}
	branch, err := Git(worktree, "symbolic-ref", "--short", "HEAD")
	if err != nil {
		return "", "", "", errors.New("the worktree is on no branch; check out the top of the stack so the laptop can fetch it")
	}
	gitURL, err := Git(worktree, "config", "--get", "remote.origin.url")
	if err != nil {
		return "", "", "", errors.New("the worktree has no origin to publish the commit to")
	}
	published, timedOut := remoteHead(worktree, branch)
	if timedOut {
		return "", "", "", errors.New("origin did not answer in time, so the commit could not be checked; try again once it does")
	}
	if !strings.HasPrefix(published, sha) {
		return "", "", "", fmt.Errorf("push %s first: the laptop fetches %s from origin, which does not have it on that branch yet", branch, sha[:7])
	}
	return gitURL, branch, sha, nil
}

func stackFiles(f factory.Factory) []string {
	dir, err := filepath.Rel(f.Worktree, filepath.Join(f.Dir, "review"))
	if err != nil {
		return nil
	}
	return []string{filepath.Join(dir, "stack.sh"), filepath.Join(dir, "stack.json")}
}

func remoteHead(worktree, branch string) (string, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), publishCheckWait)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-C", worktree, "ls-remote", "origin", "refs/heads/"+branch)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	cmd.WaitDelay = time.Second
	out, err := cmd.Output()
	if ctx.Err() != nil {
		return "", true
	}
	if err != nil {
		return "", false
	}
	return strings.TrimRight(string(out), "\n"), false
}

func qaInputs(root, dir, promptPath, prompt string) []remote.QAFile {
	inputs := []remote.QAFile{remote.NewQAFile(promptPath, []byte(prompt))}
	mentioned := regexp.MustCompile(regexp.QuoteMeta(root+"/") + "[^\\s\"'`<>()\\[\\],;]+")
	spec := []string{filepath.Join(dir, "brief.md"), filepath.Join(dir, "requirements.md"), filepath.Join(dir, "plan.md")}
	for _, match := range slices.Concat(spec, mentioned.FindAllString(prompt, -1)) {
		path := strings.TrimRight(match, ".:")
		rel, _ := filepath.Rel(root, path)
		info, err := os.Stat(path)
		known := slices.ContainsFunc(inputs, func(f remote.QAFile) bool { return f.Path == rel })
		if known || err != nil || !info.Mode().IsRegular() || info.Size() > qaInputLimit || filepath.Base(rel) == "state.json" {
			continue
		}
		if data, err := os.ReadFile(path); err == nil {
			inputs = append(inputs, remote.NewQAFile(rel, data))
		}
	}
	return inputs
}

func (m *Runtime) cancelQA(incarnation string) {
	for _, job := range m.QAJobs() {
		if job.Incarnation == incarnation && openJob(job) {
			job.Phase, job.Token, job.Inputs = remote.QACancelled, "", nil
			m.saveQA(job)
		}
	}
}

func (m *Runtime) ClaimQA(c remote.QAClaim) (remote.QAJob, error) {
	job, err := m.qaJob(c.Job)
	switch {
	case err != nil:
		return remote.QAJob{}, err
	case c.Runner == "":
		return remote.QAJob{}, errors.New("a claim names the laptop that runs it")
	case job.Phase == remote.QAClaimed && job.Runner == c.Runner:
		return job, nil
	case job.Phase == remote.QAClaimed:
		return remote.QAJob{}, errors.New("another laptop already started this QA job; nothing was started here")
	case job.Phase != remote.QAHeld:
		return remote.QAJob{}, fmt.Errorf("this QA job is %s; nothing was started", job.Phase)
	}
	job.Phase, job.Runner, job.Token = remote.QAClaimed, c.Runner, uuid.NewString()
	if err := m.saveQA(job); err != nil {
		return remote.QAJob{}, err
	}
	m.noteQA(job, "QA started on the laptop at "+job.SHA[:7])
	return job, nil
}

func (m *Runtime) ReturnQA(r remote.QAReturn) error {
	job, err := m.qaJob(r.Job)
	switch {
	case err != nil:
		return err
	case r.Token == "" || r.Token != job.Token || r.SHA != job.SHA:
		return errors.New("this report belongs to a QA job that was replaced or cancelled, so the server ignored it")
	case job.Phase == remote.QAAccepted:
		return nil
	case !json.Valid(r.Result):
		return errors.New("the QA result is not JSON")
	}
	if err := remote.CheckQAFiles(job, r.Files); err != nil {
		return err
	}
	for _, f := range r.Files {
		if err := writeUnder(job.Root, f.Path, f.Data); err != nil {
			return err
		}
	}
	if err := writeUnder(job.Root, job.Result, r.Result); err != nil {
		return err
	}
	job.Phase, job.Inputs = remote.QAAccepted, nil
	if err := m.saveQA(job); err != nil {
		return err
	}
	m.noteQA(job, "the laptop returned the QA report")
	return nil
}

func writeUnder(root, rel string, data []byte) error {
	path := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	realRoot, rootErr := filepath.EvalSymlinks(root)
	dir, dirErr := filepath.EvalSymlinks(filepath.Dir(path))
	if rootErr != nil || dirErr != nil || !Within(dir, realRoot) {
		return fmt.Errorf("%s leads outside the worktree, so it was not written", rel)
	}
	if info, err := os.Lstat(path); err == nil && !info.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file on the server, so it was not replaced", rel)
	}
	return state.WriteFileAtomic(path, data)
}

func (m *Runtime) noteQA(job remote.QAJob, text string) {
	for _, it := range m.snap.Items {
		if Incarnation(it.Factory) == job.Incarnation {
			m.note(it.Factory, text)
		}
	}
}

func (m *Runtime) note(f factory.Factory, text string) {
	if m.Loader != nil {
		m.Loader.Note(f, text)
	}
}
