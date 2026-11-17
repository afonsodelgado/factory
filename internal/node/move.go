package node

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/google/uuid"

	"github.com/afonsodelgado/factory/internal/factory"
	"github.com/afonsodelgado/factory/internal/gitclone"
	"github.com/afonsodelgado/factory/internal/host"
	"github.com/afonsodelgado/factory/internal/state"
)

const (
	ActivateRoute  = "POST /api/node/transfer/activate"
	OperationRoute = "GET /api/node/transfer/{operation}"
)

type HandOver struct {
	Operation   string `json:"operation"`
	Incarnation string `json:"incarnation"`
	Checkpoint  string `json:"checkpoint"`
	Lease       string `json:"lease"`
}

func (ts *Transfers) Move(ctx context.Context, operation, key, target string) (Transfer, error) {
	t, err := ts.Stage(ctx, operation, key, target)
	if err != nil || t.Phase == PhaseActive {
		return t, err
	}
	peer, err := ts.destination(ctx, t.Target)
	switch {
	case err != nil && t.reached(PhaseReleased):
		return ts.failed(t, err)
	case err != nil:
		return ts.refused(t, err)
	}
	if !t.reached(PhaseReleased) {
		if t, err = ts.release(ctx, t, peer); err != nil {
			return t, err
		}
	}
	return ts.handOver(ctx, t, peer)
}

func (ts *Transfers) release(ctx context.Context, t Transfer, peer Member) (Transfer, error) {
	m, err := ts.movable(ctx, t.Key)
	if err != nil {
		return ts.refused(t, err)
	}
	if t, err = ts.preflight(ctx, t, peer, m); err != nil {
		return t, err
	}
	h, lease, err := ts.Holders.Read(ctx, t.GitURL, t.Incarnation)
	switch {
	case err != nil:
		return ts.refused(t, err)
	case lease != "" && h.Node != ts.Nodes.Self.ID:
		return ts.refused(t, fmt.Errorf("this node no longer holds %s: %s took it over at generation %d", t.Factory, ts.nodeName(h.Node), h.Generation))
	}
	t.Lease, t.Generation = lease, h.Generation+1
	return ts.record(t, PhaseReleased)
}

func (ts *Transfers) handOver(ctx context.Context, t Transfer, peer Member) (Transfer, error) {
	release := host.ReleaseRequest{Repo: t.Repo, Slug: t.Slug, To: t.TargetName, Operation: t.Operation}
	if err := ts.host(ctx, "release", release, nil); err != nil {
		return ts.failed(t, fmt.Errorf("could not stop %s here, so %s was not asked to take it over: %w", t.Factory, peer.short(), err))
	}
	remote := Remote(peer.Name, ts.Nodes.HTTP)
	var theirs Transfer
	if err := remote.Do(ctx, http.MethodGet, "/api/node/transfer/"+t.Operation, nil, &theirs); err == nil && theirs.Phase == PhaseActive {
		return ts.acknowledged(t)
	}
	activation := HandOver{Operation: t.Operation, Incarnation: t.Incarnation, Checkpoint: t.Checkpoint, Lease: t.Lease}
	if err := remote.Do(ctx, http.MethodPost, routePath(ActivateRoute), activation, &theirs); err != nil {
		return ts.failed(t, fmt.Errorf("%s did not confirm it took %s over: %w", peer.short(), t.Factory, err))
	}
	return ts.acknowledged(t)
}

func (ts *Transfers) acknowledged(t Transfer) (Transfer, error) {
	t.Failure = ""
	return ts.record(t, PhaseActive)
}

func (ts *Transfers) host(ctx context.Context, kind string, payload, into any) error {
	answer, err := ts.call(ctx, kind, "", payload, into)
	if err == nil && answer.T != "done" {
		err = declined{errors.New(answer.Error)}
	}
	return err
}

type declined struct{ error }

func (d declined) Unwrap() error { return d.error }

func declinedf(format string, args ...any) error {
	return declined{fmt.Errorf(format, args...)}
}

func (ts *Transfers) settle(ctx context.Context, t Transfer, err error) (Transfer, error) {
	var changed HolderChanged
	switch {
	case interrupted(err):
		return t, err
	case ctx.Err() != nil:
		return t, ctx.Err()
	case errors.As(err, &declined{}), errors.As(err, &changed):
		return ts.refused(t, err)
	}
	return ts.failed(t, err)
}

func (ts *Transfers) Activate(ctx context.Context, a HandOver) (Transfer, error) {
	t, found, err := LoadTransfer(ts.StateDir, a.Operation)
	switch {
	case err != nil:
		return t, err
	case !found || t.Role != RoleTarget || t.Incarnation != a.Incarnation || t.Checkpoint != a.Checkpoint:
		return t, fmt.Errorf("this node staged no checkpoint %s for operation %s", a.Checkpoint, a.Operation)
	case t.Refusal != "":
		return t, errors.New(t.Refusal)
	case t.Phase == PhaseActive:
		return t, nil
	}
	m, err := ts.stagedManifest(t)
	if err != nil {
		return ts.refused(t, err)
	}
	t.Lease = a.Lease
	return ts.takeOver(ctx, t, m)
}

func (ts *Transfers) stagedManifest(t Transfer) (Manifest, error) {
	m, err := readManifest(StagedDir(ts.StateDir, t.Incarnation))
	if err != nil || m.Digest() != t.Checkpoint {
		return m, fmt.Errorf("the checkpoint staged here for %s is no longer checkpoint %s", t.Factory, t.Checkpoint)
	}
	return m, nil
}

func readManifest(staged string) (Manifest, error) {
	var m Manifest
	data, err := os.ReadFile(filepath.Join(staged, "manifest.json"))
	if err == nil {
		err = json.Unmarshal(data, &m)
	}
	return m, err
}

func (ts *Transfers) takeOver(ctx context.Context, t Transfer, m Manifest) (Transfer, error) {
	repo, err := ts.repoFor(ctx, m.GitURL)
	if err != nil {
		return ts.failed(t, err)
	}
	worktree := filepath.Join(factory.WorktreesDir(repo), m.Slug)
	if !t.reached(PhaseReleased) {
		if _, err := gitclone.Git(ctx, repo, "fetch", "-q", "origin"); err != nil {
			return ts.failed(t, err)
		}
		if err := ts.ready(ctx, repo, worktree, m); err != nil {
			return ts.settle(ctx, t, err)
		}
		if t.Generation, err = ts.claim(ctx, t, m); err != nil {
			return ts.settle(ctx, t, ts.named(err))
		}
		if t, err = ts.record(t, PhaseReleased); err != nil {
			return t, err
		}
	}
	key, err := ts.materialize(ctx, m, repo, worktree)
	if err != nil {
		return ts.failed(t, err)
	}
	ts.markVerified(t.Incarnation)
	ts.hold(ctx, key, host.Holding{Generation: t.Generation})
	t.Key, t.Repo, t.Slug, t.Failure = key, repo, m.Slug, ""
	if t, err = ts.record(t, PhaseActive); err == nil {
		ts.supersede(t)
	}
	return t, err
}

func (ts *Transfers) supersede(t Transfer) {
	for _, old := range Journals(ts.StateDir) {
		stranded := old.Role == RoleSource && old.Phase == PhaseReleased && old.Refusal == ""
		if stranded && old.Incarnation == t.Incarnation && old.Operation != t.Operation {
			ts.refused(old, fmt.Errorf("superseded: %s runs on %s again since move %s", t.Factory, t.TargetName, t.Operation))
		}
	}
}

func (ts *Transfers) ready(ctx context.Context, repo, worktree string, m Manifest) error {
	branch, head, err := checkedOut(m)
	if err != nil {
		return declined{err}
	}
	for _, b := range m.Stack {
		if _, err := gitclone.Git(ctx, repo, "cat-file", "-e", b.SHA+"^{commit}"); err != nil {
			return declinedf("%s at %s is not on the git remote; a node takes over only pushed branches", b.Branch, b.SHA)
		}
		if local, err := gitclone.Git(ctx, repo, "rev-parse", "--verify", "refs/heads/"+b.Branch); err == nil && !replaceable(ctx, repo, local, b.SHA) {
			return declinedf("branch %s here has commits the checkpoint does not hold and no git remote has; push or remove them first", b.Branch)
		}
	}
	if err := ts.host(ctx, "can-adopt", host.AdoptRequest{Repo: repo, Slug: m.Slug, Incarnation: m.Incarnation}, nil); err != nil {
		return err
	}
	if info, err := os.Stat(worktree); err != nil || !info.IsDir() {
		return nil
	}
	dirty, err := dirtyProductFiles(ctx, worktree)
	if err != nil {
		return err
	}
	if len(dirty) > 0 {
		return declinedf("%s here has uncommitted changes in %s that the checkpoint of %s would overwrite; commit or remove them first", worktree, strings.Join(dirty, ", "), branch)
	}
	current, err := gitclone.Git(ctx, worktree, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	if !replaceable(ctx, repo, current, head) {
		return declinedf("%s here has commits the checkpoint does not hold and no git remote has; push or remove them first", worktree)
	}
	return nil
}

func replaceable(ctx context.Context, repo, local, target string) bool {
	if _, err := gitclone.Git(ctx, repo, "merge-base", "--is-ancestor", local, target); err == nil {
		return true
	}
	pushed, err := gitclone.Git(ctx, repo, "for-each-ref", "--contains", local, "--format=%(refname)", "refs/remotes")
	return err == nil && pushed != ""
}

func checkedOut(m Manifest) (string, string, error) {
	if len(m.Stack) == 0 {
		return "", "", errors.New("the checkpoint names no pushed branch to check out")
	}
	branch := cmp.Or(m.Branch, m.Stack[len(m.Stack)-1].Branch)
	i := slices.IndexFunc(m.Stack, func(b StackBranch) bool { return b.Branch == branch })
	if i < 0 {
		return "", "", fmt.Errorf("the checkpoint has no pushed commit for %s", branch)
	}
	return branch, m.Stack[i].SHA, nil
}

func (ts *Transfers) named(err error) error {
	var changed HolderChanged
	if !errors.As(err, &changed) || changed.Now.Node == "" {
		return err
	}
	return declinedf("%s claimed this factory first, at generation %d", ts.nodeName(changed.Now.Node), changed.Now.Generation)
}

func (ts *Transfers) repoFor(ctx context.Context, gitURL string) (string, error) {
	if repo, ok := ts.Nodes.repos()[gitURL]; ok {
		return repo, nil
	}
	if ts.Nodes.Clone == nil {
		return "", errors.New("this node cannot clone repos")
	}
	return ts.Nodes.Clone(ctx, gitURL)
}

func (ts *Transfers) claim(ctx context.Context, t Transfer, m Manifest) (int64, error) {
	self := ts.Nodes.Self.ID
	next := Holder{Schema: HolderSchema, Incarnation: m.Incarnation, Node: self, Previous: m.Source, Generation: 1, Checkpoint: t.Checkpoint, Operation: t.Operation}
	if t.Lease != "" {
		now, sha, err := ts.Holders.Read(ctx, m.GitURL, m.Incarnation)
		switch {
		case err != nil:
			return 0, err
		case now.Node == self && now.Operation == t.Operation:
			return now.Generation, nil
		case sha != t.Lease:
			return 0, HolderChanged{Now: now}
		}
		next = now.Next(self, t.Checkpoint, t.Operation)
	}
	_, err := ts.Holders.Claim(ctx, m.GitURL, t.Lease, next)
	return next.Generation, err
}

func (ts *Transfers) materialize(ctx context.Context, m Manifest, repo, worktree string) (string, error) {
	if err := ts.host(ctx, "can-adopt", host.AdoptRequest{Repo: repo, Slug: m.Slug, Incarnation: m.Incarnation}, nil); err != nil {
		return "", err
	}
	if err := checkout(ctx, repo, worktree, m); err != nil {
		return "", err
	}
	var key string
	if err := ts.host(ctx, "adopt", host.AdoptRequest{Repo: repo, Slug: m.Slug, Incarnation: m.Incarnation}, &key); err != nil {
		return "", err
	}
	if err := unpack(StagedDir(ts.StateDir, m.Incarnation), worktree, repo, m.Slug); err != nil {
		return "", err
	}
	return key, ts.host(ctx, "launch-moved", map[string]string{"Key": key}, nil)
}

func checkout(ctx context.Context, repo, worktree string, m Manifest) error {
	branch, head, err := checkedOut(m)
	if err != nil {
		return err
	}
	for _, b := range m.Stack {
		if b.Branch != branch {
			if _, err := gitclone.Git(ctx, repo, "branch", "-f", "--", b.Branch, b.SHA); err != nil {
				return err
			}
		}
	}
	if info, err := os.Stat(worktree); err == nil && info.IsDir() {
		_, err := gitclone.Git(ctx, worktree, "checkout", "-q", "-B", branch, head)
		return err
	}
	_, err = gitclone.Git(ctx, repo, "worktree", "add", "-q", "-B", branch, worktree, head)
	return err
}

func unpack(staged, worktree, repo, slug string) error {
	if filepath.Base(slug) != slug || slug == "." || slug == ".." {
		return fmt.Errorf("the checkpoint names %q, which is not a factory folder", slug)
	}
	files := filepath.Join(staged, "files")
	places := map[string]string{
		RootFactory:  filepath.Join(worktree, ".claude", "factory", slug),
		RootEvidence: filepath.Join(worktree, ".claude", "qa", slug),
	}
	written := map[string]bool{}
	err := filepath.WalkDir(files, func(full string, d fs.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() {
			return err
		}
		rel, _ := filepath.Rel(files, full)
		root, rest, _ := strings.Cut(filepath.ToSlash(rel), "/")
		dest := filepath.Join(places[root], filepath.FromSlash(rest))
		if filepath.ToSlash(rel) == RepoConfigFile {
			dest = state.ProfilePath(repo)
		}
		data, err := os.ReadFile(full)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return err
		}
		written[dest] = true
		return os.WriteFile(dest, data, 0o644)
	})
	if err != nil {
		return err
	}
	for _, place := range places {
		if err := removeUnlisted(place, written); err != nil {
			return err
		}
	}
	return nil
}

func removeUnlisted(dir string, listed map[string]bool) error {
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() || listed[path] {
			return err
		}
		return os.Remove(path)
	})
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

func (ts *Transfers) activateRoute(w http.ResponseWriter, r *http.Request, _ string) {
	var a HandOver
	if err := decode(r, &a); err != nil || uuid.Validate(a.Operation) != nil {
		fail(w, http.StatusBadRequest, "send {operation, incarnation, checkpoint, lease}")
		return
	}
	if t, found, _ := LoadTransfer(ts.StateDir, a.Operation); found {
		if err := ts.verifiedSource(r.Context(), t.Source); err != nil {
			fail(w, http.StatusForbidden, err.Error())
			return
		}
	}
	t, err := ts.Activate(r.Context(), a)
	if err != nil {
		reply(w, http.StatusConflict, map[string]any{"error": err.Error(), "transfer": t})
		return
	}
	reply(w, http.StatusOK, t)
}

func (ts *Transfers) operationRoute(w http.ResponseWriter, r *http.Request, _ string) {
	op := r.PathValue("operation")
	t, found, err := LoadTransfer(ts.StateDir, op)
	switch {
	case uuid.Validate(op) != nil || (err == nil && !found):
		fail(w, http.StatusNotFound, "no such transfer on this node")
	case err != nil:
		fail(w, http.StatusInternalServerError, err.Error())
	default:
		reply(w, http.StatusOK, t)
	}
}
