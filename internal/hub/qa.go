package hub

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/afonsodelgado/factory/internal/factory"
	"github.com/afonsodelgado/factory/internal/host"
	"github.com/afonsodelgado/factory/internal/remote"
	"github.com/afonsodelgado/factory/internal/state"
	"github.com/afonsodelgado/factory/internal/ui/panel"
)

const (
	qaPrepareWait = 5 * time.Minute
	qaLaunchGrace = 30 * time.Second
	qaUploadRate  = 256 << 10
	laptopStage   = "qa · laptop"
)

type qaClaimedMsg struct {
	run remote.QARun
	err error
}

type qaReturnedMsg struct {
	run     remote.QARun
	failure string
	err     error
}

func (m *Model) qaRuns() []remote.QARun {
	if m.runs == nil && m.Config.StateDir != "" {
		m.runs = remote.LoadQARuns(m.Config.StateDir)
	}
	return m.runs
}

func (m *Model) qaRunOf(key string) (remote.QARun, bool) {
	for _, run := range m.qaRuns() {
		if run.Key == key {
			return run, true
		}
	}
	return remote.QARun{}, false
}

func (m *Model) saveRuns(runs []remote.QARun) {
	m.runs = runs
	if err := remote.SaveQARuns(m.Config.StateDir, runs); err != nil {
		m.notice = problem(err.Error())
	}
}

func (m *Model) runner() string {
	id, _ := remote.ServerID(m.Config.StateDir)
	return id
}

func (m *Model) startable(it Item) (remote.QAJob, bool) {
	job := it.QA
	if job == nil || it.Factory.Remote == nil {
		return remote.QAJob{}, false
	}
	if _, running := m.qaRunOf(it.Factory.Key()); running {
		return remote.QAJob{}, false
	}
	return *job, job.Phase == remote.QAHeld || (job.Phase == remote.QAClaimed && job.Runner == m.runner())
}

func (m *Model) qaItems(it Item) []panel.Item {
	job, ok := m.startable(it)
	if !ok {
		return nil
	}
	where := it.Factory.Remote.Name
	item := panel.Item{
		Kind:   "qa",
		Title:  job.Start() + " · held on " + where,
		Body:   fmt.Sprintf("%s holds this %s for this laptop until you start it here. It fetches %s of %s into a worktree of its own and runs claude --chrome there. The report goes back to %s, which runs the fixes.", where, kindName(job.Kind), job.SHA[:7], job.Branch, where),
		Since:  job.CreatedAt,
		Action: "⏎ " + job.Start(),
	}
	checkout := "none: clone " + job.GitURL + " and run factory init in it"
	if repo, found := m.checkoutOf(job.GitURL); found {
		checkout = tilde(repo)
	}
	item.Detail = []string{"commit       " + job.SHA[:7] + " on " + job.Branch, "checkout     " + checkout}
	if _, pending := m.claiming[job.ID]; pending {
		item.Action = "starting…"
	}
	return []panel.Item{item}
}

func (m *Model) checkoutOf(gitURL string) (string, bool) {
	if m.checkouts == nil {
		m.checkouts = map[string]string{}
	}
	repo, known := m.checkouts[gitURL]
	if !known {
		repo, _ = remote.LocalCheckout(m.Config.Repos, gitURL)
		m.checkouts[gitURL] = repo
	}
	return repo, repo != ""
}

func kindName(kind string) string {
	switch kind {
	case "recheck":
		return "re-check"
	case "walkthrough":
		return "walkthrough"
	}
	return "browser QA"
}

func (m *Model) startQA(it Item) {
	job, ok := m.startable(it)
	if _, pending := m.claiming[job.ID]; !ok || pending {
		return
	}
	f := it.Factory
	if m.unreachable(f) {
		m.notice = problem(f.Remote.Name + " is unreachable, so " + kindName(job.Kind) + " did not start. Nothing is queued: press ⏎ again once it is back.")
		return
	}
	repo, found := m.checkoutOf(job.GitURL)
	if !found {
		m.notice = problem("No checkout of " + job.GitURL + " here. Clone it and run factory init in it, then press ⏎ again. " + f.Remote.Name + " keeps holding it.")
		return
	}
	if _, err := exec.LookPath("claude"); err != nil {
		m.notice = problem("claude is not installed here, and " + kindName(job.Kind) + " runs in claude --chrome. " + f.Remote.Name + " keeps holding it.")
		return
	}
	if m.claiming == nil {
		m.claiming = map[string]remote.QARun{}
	}
	run := remote.QARun{Remote: f.Remote.Name, Key: f.Key(), Slug: proxySlug(f), Repo: repo, Dir: remote.QADir(m.Config.StateDir, job.ID), Job: job}
	m.claiming[job.ID] = run
	cfg, runner := m.Config, m.runner()
	m.later = append(m.later, func() tea.Msg { return claimQA(cfg, runner, run) })
}

func claimQA(cfg *state.Config, runner string, run remote.QARun) tea.Msg {
	if err := run.Job.Validate(); err != nil {
		return qaClaimedMsg{run: run, err: err}
	}
	ctx, cancel := context.WithTimeout(context.Background(), qaPrepareWait)
	defer cancel()
	remote.RemoveQA(ctx, run.Repo, run.Dir, run.Job)
	err := remote.CheckOutQA(ctx, run.Repo, run.Dir, run.Job)
	var claimed remote.QAJob
	if err == nil {
		err = mutate(cfg, run.Remote, "qa-claim", remote.QAClaim{Job: run.Job.ID, Runner: runner}, &claimed)
	}
	if err == nil {
		run.Job = claimed
		if err = claimed.Validate(); err == nil {
			err = remote.WriteQAInputs(run.Dir, claimed)
		}
	}
	if err != nil && run.Job.Token == "" {
		remote.RemoveQA(ctx, run.Repo, run.Dir, run.Job)
	}
	return qaClaimedMsg{run: run, err: err}
}

func (m *Model) qaClaimed(msg qaClaimedMsg) {
	run := msg.run
	delete(m.claiming, run.Job.ID)
	switch {
	case errors.Is(msg.err, remote.ErrUncertain):
		m.notice = problem(run.Remote + " did not answer, so whether " + run.Job.Factory + " started is unknown. Wait for the list to refresh, then press ⏎ again.")
		return
	case msg.err != nil && run.Job.Token == "":
		m.notice = problem(kindName(run.Job.Kind) + " did not start: " + msg.err.Error() + ". " + run.Remote + " keeps holding it.")
		return
	}
	run.Launched = m.Now()
	m.saveRuns(append(slices.Clone(m.qaRuns()), run))
	if msg.err == nil {
		msg.err = m.launchQA(run)
	}
	if msg.err != nil {
		m.notice = problem(run.Job.Factory + " could not start here: " + msg.err.Error() + ". " + run.Remote + " gets a failure to retry.")
		return
	}
	if m.Notify != nil && m.Config.Alerts.Notifies() {
		go m.Notify(run.Job.Factory + " on " + run.Remote + ": QA started on this laptop")
	}
	m.refresh()
}

func (m *Model) launchQA(run remote.QARun) error {
	if m.Engine == nil {
		return nil
	}
	pane, err := m.Engine.NewStagePane(m.Layout, host.StagePane{Repo: run.Key, Slug: run.Slug, Role: "qa", Token: run.Job.Token, Dir: run.Dir})
	if err != nil {
		return err
	}
	launch := factory.StageLaunch{Setting: run.Job.Setting, Kickoff: "Read " + filepath.Join(run.Dir, run.Job.Prompt) + " and follow it.", Chrome: true}
	return m.execution().TypeLaunch(pane, launchScript(run), launch.Command())
}

func (m *Model) tendQA(name string) {
	listed := map[string]remote.Factory{}
	for _, w := range m.link(name).view.Factories {
		listed[w.Incarnation] = w
	}
	for _, run := range m.qaRuns() {
		if run.Remote != name || m.returning[run.Job.ID] {
			continue
		}
		w, ok := listed[run.Job.Incarnation]
		if !ok || w.QA == nil || w.QA.ID != run.Job.ID {
			m.dropQA(run, news(run.Job.Factory+" on "+name+" no longer waits for this QA, so its worktree here is removed."))
			continue
		}
		report, ready, err := remote.QAReport(run.Dir, run.Job)
		switch {
		case err != nil:
			m.returnQA(run, remote.FailedQA(run.Job, "the laptop could not read the QA report: "+err.Error()))
		case ready:
			m.returnQA(run, report)
		case m.qaEnded(run):
			m.returnQA(run, remote.FailedQA(run.Job, "the QA process on the laptop ended without a report"))
		}
	}
}

func (m *Model) qaEnded(run remote.QARun) bool {
	if m.Engine == nil || m.Now().Sub(run.Launched) < qaLaunchGrace {
		return false
	}
	p, found := m.proxyPane(run.Key, "qa", run.Job.Token)
	return !found || factory.IsShell(p.Command)
}

func (m *Model) returnQA(run remote.QARun, report remote.QAReturn) {
	if m.returning == nil {
		m.returning = map[string]bool{}
	}
	m.returning[run.Job.ID] = true
	cfg := m.Config
	failure, wait := remote.Failure(report), qaUploadWait(report)
	m.later = append(m.later, func() tea.Msg {
		return qaReturnedMsg{run: run, failure: failure, err: mutateWithin(cfg, run.Remote, "qa-return", wait, report, nil)}
	})
}

func qaUploadWait(report remote.QAReturn) time.Duration {
	size := 0
	for _, f := range report.Files {
		size += len(f.Data)
	}
	return remoteWait + time.Duration(size/qaUploadRate)*time.Second
}

func (m *Model) qaReturned(msg qaReturnedMsg) {
	delete(m.returning, msg.run.Job.ID)
	var unreachable *remote.TransportError
	switch {
	case errors.Is(msg.err, remote.ErrUncertain) || errors.As(msg.err, &unreachable):
		return
	case msg.err != nil:
		m.dropQA(msg.run, problem(msg.run.Remote+" refused the QA report: "+msg.err.Error()))
	case msg.failure != "":
		m.dropQA(msg.run, problem(msg.run.Job.Factory+": "+msg.failure+". "+msg.run.Remote+"'s supervisor gets the failure to retry."))
	default:
		m.dropQA(msg.run, news("The QA report of "+msg.run.Job.Factory+" went back to "+msg.run.Remote+"."))
	}
}

func (m *Model) dropQA(run remote.QARun, why notice) {
	if p, running := m.proxyPane(run.Key, "qa", run.Job.Token); running && m.Engine != nil {
		strays := host.Descendants([]int{p.PID})
		m.Engine.KillStages(run.Key, run.Slug, "qa")
		host.Terminate(strays)
	}
	m.saveRuns(slices.DeleteFunc(slices.Clone(m.qaRuns()), func(r remote.QARun) bool { return r.Job.ID == run.Job.ID }))
	for i, w := range m.link(run.Remote).view.Factories {
		if w.QA != nil && w.QA.ID == run.Job.ID {
			m.link(run.Remote).view.Factories[i].QA = nil
		}
	}
	m.refresh()
	m.notice = why
	m.later = append(m.later, func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), qaPrepareWait)
		defer cancel()
		os.Remove(launchScript(run))
		if err := remote.RemoveQA(ctx, run.Repo, run.Dir, run.Job); err != nil {
			return noticeMsg(problem("The QA worktree " + tilde(run.Dir) + " stays: " + err.Error()))
		}
		return nil
	})
}

func launchScript(run remote.QARun) string { return run.Dir + ".sh" }

func (m *Model) laptopQAPane(key string) (string, bool) {
	run, ok := m.qaRunOf(key)
	if !ok {
		return "", false
	}
	p, found := m.proxyPane(key, "qa", run.Job.Token)
	return p.ID, found
}

func (m *Model) ranHere(token string) bool {
	return slices.ContainsFunc(m.qaRuns(), func(r remote.QARun) bool { return r.Job.Token == token })
}
