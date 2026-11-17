package node

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"
	"unicode"

	"github.com/google/uuid"

	"github.com/afonsodelgado/factory/internal/factory"
	"github.com/afonsodelgado/factory/internal/gitclone"
	"github.com/afonsodelgado/factory/internal/host"
	"github.com/afonsodelgado/factory/internal/state"
)

const (
	CheckpointSchema = 1
	RootFactory      = "factory"
	RootEvidence     = "evidence"
	RepoConfigFile   = "config/config.yml"
	kindFile         = "file"
)

type Limits struct {
	File  int64 `json:"file"`
	Total int64 `json:"total"`
	Files int   `json:"files"`
}

var DefaultLimits = Limits{File: 16 << 20, Total: 64 << 20, Files: 4096}

type StackBranch struct {
	Branch string `json:"branch"`
	SHA    string `json:"sha"`
	PR     int    `json:"pr,omitempty"`
	Base   string `json:"base,omitempty"`
}

type ManifestFile struct {
	Path   string `json:"path"`
	Kind   string `json:"kind"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type Manifest struct {
	Schema      int            `json:"schema"`
	Operation   string         `json:"operation"`
	Incarnation string         `json:"incarnation"`
	Factory     string         `json:"factory"`
	Slug        string         `json:"slug"`
	Source      string         `json:"source"`
	GitURL      string         `json:"git_url"`
	Branch      string         `json:"branch"`
	Stack       []StackBranch  `json:"stack"`
	Files       []ManifestFile `json:"files"`
	At          time.Time      `json:"at"`
}

func (m Manifest) Digest() string {
	data, _ := json.Marshal(m)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

type Payload struct {
	Path string `json:"path"`
	Data []byte `json:"data"`
}

type Checkpoint struct {
	Manifest Manifest  `json:"manifest"`
	Files    []Payload `json:"files"`
}

var excludedRuntime = []string{"factory/.heartbeat", "factory/review/stack.json", "factory/inbox"}

var secretDirs = []string{"profile", "chrome-profile", "sessions", "projects", "hooks", "launch"}

var secretNames = []string{"cookies", "cookies.json", "cookies.txt", "cookie.json", "cookie.txt", "login data", "local state", "web data", ".netrc", ".npmrc", ".pypirc", ".git-credentials", "credentials", "credentials.json", ".credentials.json", ".claude.json", "auth.json", "machine.yml", "mcp.json"}

func excluded(rel string) bool {
	return slices.ContainsFunc(excludedRuntime, func(e string) bool { return rel == e || strings.HasPrefix(rel, e+"/") })
}

func secretShaped(rel string) bool {
	parts := strings.Split(strings.ToLower(rel), "/")
	base := parts[len(parts)-1]
	inSecretDir := slices.ContainsFunc(parts[:len(parts)-1], func(p string) bool { return slices.Contains(secretDirs, p) })
	envFile := base == ".env" || strings.HasPrefix(base, ".env.")
	keyFile := strings.HasSuffix(base, ".pem") || strings.HasSuffix(base, ".key") || strings.HasPrefix(base, "id_rsa") || strings.HasPrefix(base, "id_ed25519")
	sessionStore := strings.HasSuffix(base, ".jsonl")
	return inSecretDir || envFile || keyFile || sessionStore || strings.HasSuffix(base, ".cookies") || slices.Contains(secretNames, base)
}

type captured struct {
	limits Limits
	files  []ManifestFile
	data   []Payload
	total  int64
}

func (c *captured) add(rel, full string) error {
	if secretShaped(rel) {
		return fmt.Errorf("%s looks like a secret-bearing runtime file (credentials, cookies, a browser profile or a session store), so this checkpoint copies nothing; move it out of the factory's folders or delete it", full)
	}
	if len(c.files) >= c.limits.Files {
		return fmt.Errorf("the checkpoint would hold more than %d files", c.limits.Files)
	}
	data, err := readRegular(full, c.limits.File)
	if err != nil {
		return err
	}
	c.total += int64(len(data))
	if c.total > c.limits.Total {
		return fmt.Errorf("the checkpoint would be larger than %d MiB; remove old QA evidence and try again", c.limits.Total>>20)
	}
	sum := sha256.Sum256(data)
	c.files = append(c.files, ManifestFile{Path: rel, Kind: kindFile, Size: int64(len(data)), SHA256: hex.EncodeToString(sum[:])})
	c.data = append(c.data, Payload{Path: rel, Data: data})
	return nil
}

func readRegular(full string, limit int64) ([]byte, error) {
	f, err := os.OpenFile(full, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, fmt.Errorf("%s: %w; a checkpoint copies only regular files, never symlinks", full, err)
	}
	defer f.Close()
	info, err := f.Stat()
	switch {
	case err != nil:
		return nil, err
	case !info.Mode().IsRegular():
		return nil, fmt.Errorf("%s is not a regular file", full)
	case info.Size() > limit:
		return nil, fmt.Errorf("%s is larger than %d MiB", full, limit>>20)
	}
	return io.ReadAll(io.LimitReader(f, limit))
}

func (c *captured) walk(root, dir string) error {
	info, err := os.Lstat(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist) && root == RootEvidence:
		return nil
	case err != nil:
		return err
	case !info.IsDir():
		return fmt.Errorf("%s is not a directory; a checkpoint never follows symlinks", dir)
	}
	return filepath.WalkDir(dir, func(full string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, full)
		rel = path.Join(root, filepath.ToSlash(rel))
		switch {
		case excluded(rel) && d.IsDir():
			return filepath.SkipDir
		case excluded(rel), d.IsDir():
			return nil
		case d.Type()&fs.ModeSymlink != 0:
			return fmt.Errorf("%s is a symlink; a checkpoint copies only regular files", full)
		case !d.Type().IsRegular():
			return fmt.Errorf("%s is not a regular file", full)
		}
		return c.add(rel, full)
	})
}

func Capture(m host.Movable, manifest Manifest, limits Limits) (Checkpoint, error) {
	c := &captured{limits: limits}
	if err := c.walk(RootFactory, m.Dir); err != nil {
		return Checkpoint{}, err
	}
	if err := c.walk(RootEvidence, m.Evidence); err != nil {
		return Checkpoint{}, err
	}
	if config := state.ProfilePath(m.Repo); fileExists(config) {
		if err := c.add(RepoConfigFile, config); err != nil {
			return Checkpoint{}, err
		}
	}
	manifest.Schema, manifest.Files = CheckpointSchema, c.files
	return Checkpoint{Manifest: manifest, Files: c.data}, nil
}

func fileExists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

func (cp Checkpoint) File(rel string) ([]byte, bool) {
	i := slices.IndexFunc(cp.Files, func(p Payload) bool { return p.Path == rel })
	if i < 0 {
		return nil, false
	}
	return cp.Files[i].Data, true
}

func (cp Checkpoint) Validate(limits Limits) error {
	m := cp.Manifest
	if m.Schema != CheckpointSchema {
		return fmt.Errorf("checkpoint schema %d is not %d, the one this node reads; run the same factory release on both nodes", m.Schema, CheckpointSchema)
	}
	for name, id := range map[string]string{"operation": m.Operation, "incarnation": m.Incarnation, "source": m.Source} {
		if uuid.Validate(id) != nil {
			return fmt.Errorf("checkpoint %s %q is not a UUID", name, id)
		}
	}
	if factory.Slugify(m.Factory) != strings.ToLower(m.Factory) || m.Factory == "" {
		return fmt.Errorf("checkpoint factory %q is not a factory id", m.Factory)
	}
	unsafePrefix := strings.HasPrefix(m.Slug, ".") || strings.HasPrefix(m.Slug, "-")
	unsafeCharacters := strings.ContainsAny(m.Slug, "/\\") || strings.ContainsFunc(m.Slug, unicode.IsControl)
	invalidSlug := m.Slug == "" || len(m.Slug) > 255 || unsafePrefix || unsafeCharacters
	if invalidSlug {
		return fmt.Errorf("checkpoint slug %q is not a factory folder", m.Slug)
	}
	if err := gitclone.CheckURL(m.GitURL); err != nil {
		return err
	}
	if len(m.Files) > limits.Files || len(cp.Files) != len(m.Files) {
		return fmt.Errorf("the checkpoint lists %d files and carries %d; at most %d are allowed", len(m.Files), len(cp.Files), limits.Files)
	}
	payloads := map[string][]byte{}
	for _, p := range cp.Files {
		if _, twice := payloads[p.Path]; twice {
			return fmt.Errorf("the checkpoint carries %s twice", p.Path)
		}
		payloads[p.Path] = p.Data
	}
	var total int64
	listed := map[string]bool{}
	for _, f := range m.Files {
		if err := validEntry(f, limits); err != nil {
			return err
		}
		if listed[f.Path] {
			return fmt.Errorf("the checkpoint lists %s twice", f.Path)
		}
		listed[f.Path] = true
		data, carried := payloads[f.Path]
		sum := sha256.Sum256(data)
		switch {
		case !carried:
			return fmt.Errorf("the checkpoint lists %s but does not carry it", f.Path)
		case int64(len(data)) != f.Size || hex.EncodeToString(sum[:]) != f.SHA256:
			return fmt.Errorf("%s does not match its size and hash in the manifest", f.Path)
		}
		if total += f.Size; total > limits.Total {
			return fmt.Errorf("the checkpoint is larger than %d MiB", limits.Total>>20)
		}
	}
	return cp.ownIdentity()
}

func validEntry(f ManifestFile, limits Limits) error {
	root, _, _ := strings.Cut(f.Path, "/")
	inRoot := root == RootFactory || root == RootEvidence || f.Path == RepoConfigFile
	switch {
	case f.Kind != kindFile:
		return fmt.Errorf("%s is a %s; a checkpoint holds only regular files", f.Path, f.Kind)
	case path.Clean(f.Path) != f.Path || !filepath.IsLocal(filepath.FromSlash(f.Path)) || !inRoot:
		return fmt.Errorf("%s is outside the factory folder, its QA evidence and its repo config", f.Path)
	case excluded(f.Path) || secretShaped(f.Path):
		return fmt.Errorf("%s looks like a secret-bearing runtime file, so this node takes nothing from the checkpoint", f.Path)
	case f.Size < 0 || f.Size > limits.File:
		return fmt.Errorf("%s is larger than %d MiB", f.Path, limits.File>>20)
	}
	return nil
}

func (cp Checkpoint) ownIdentity() error {
	data, ok := cp.File(RootFactory + "/state.json")
	if !ok {
		return errors.New("the checkpoint carries no state.json")
	}
	s, err := state.ParseState(data)
	if err != nil {
		return fmt.Errorf("the checkpoint's state.json does not parse: %w", err)
	}
	m := cp.Manifest
	switch {
	case s.ID != m.Factory:
		return fmt.Errorf("the checkpoint names factory %s but its state.json is %s", m.Factory, s.ID)
	case s.Checkpoint == nil || s.Checkpoint.Operation != m.Operation || s.Checkpoint.Status != state.CheckpointReady:
		return fmt.Errorf("the supervisor's state.json has no ready checkpoint for operation %s", m.Operation)
	}
	return nil
}

func CheckpointPath(stateDir, incarnation string) string {
	return filepath.Join(stateDir, "checkpoints", incarnation+".json")
}

func Retain(stateDir string, cp Checkpoint) error {
	return host.SavePrivate(CheckpointPath(stateDir, cp.Manifest.Incarnation), cp)
}

func PushStack(ctx context.Context, m host.Movable, gitURL string) ([]StackBranch, error) {
	if dirty, err := dirtyProductFiles(ctx, m.Worktree); err != nil || len(dirty) > 0 {
		return nil, cmp.Or(err, fmt.Errorf("%s has uncommitted changes in %s; the node never commits them, so the supervisor must checkpoint them or say why it cannot", m.Worktree, strings.Join(dirty, ", ")))
	}
	stack := stackOf(m)
	for i := range stack {
		sha, err := pushed(ctx, m.Worktree, gitURL, stack[i].Branch)
		if err != nil {
			return nil, err
		}
		stack[i].SHA = sha
	}
	return stack, headCovered(ctx, m.Worktree, stack)
}

func stackOf(m host.Movable) []StackBranch {
	names := []string{m.Branch}
	for _, pr := range m.PRs {
		names = append(names, pr.Branch)
	}
	branches := factory.FactoryBranches(m.Repo, names)
	var stack []StackBranch
	base := ""
	for _, pr := range m.PRs {
		if slices.Contains(branches, pr.Branch) {
			stack = append(stack, StackBranch{Branch: pr.Branch, PR: pr.Number, Base: base})
			base = pr.Branch
		}
	}
	for _, b := range branches {
		if !slices.ContainsFunc(stack, func(s StackBranch) bool { return s.Branch == b }) {
			stack = append(stack, StackBranch{Branch: b})
		}
	}
	return stack
}

func headCovered(ctx context.Context, worktree string, stack []StackBranch) error {
	for _, b := range stack {
		if _, err := gitclone.Git(ctx, worktree, "merge-base", "--is-ancestor", "HEAD", b.SHA); err == nil {
			return nil
		}
	}
	return fmt.Errorf("%s has commits on HEAD that no pushed branch of the factory holds; the supervisor must put them on a stack branch first", worktree)
}

func pushed(ctx context.Context, worktree, gitURL, branch string) (string, error) {
	ref := "refs/heads/" + branch
	sha, err := gitclone.Git(ctx, worktree, "rev-parse", "--verify", ref)
	if err != nil {
		return "", fmt.Errorf("branch %s is not in %s: %w", branch, worktree, err)
	}
	if _, err := gitclone.Git(ctx, worktree, "push", "--", gitURL, ref+":"+ref); err != nil {
		return "", fmt.Errorf("could not push %s: %w", branch, err)
	}
	remote, err := gitclone.Git(ctx, worktree, "ls-remote", "--", gitURL, ref)
	if err != nil {
		return "", err
	}
	if got, _, _ := strings.Cut(remote, "\t"); got != sha {
		return "", fmt.Errorf("%s on %s is at %q after the push, not %s", branch, gitURL, got, sha)
	}
	return sha, nil
}

var toolCaches = append([]string{".impeccable/", ".revmux/"}, state.FactoryPaths...)

func dirtyProductFiles(ctx context.Context, worktree string) ([]string, error) {
	changed, err := gitclone.Git(ctx, worktree, "diff", "--name-only", "HEAD")
	if err != nil {
		return nil, err
	}
	untracked, err := gitclone.Git(ctx, worktree, "ls-files", "--others", "--exclude-standard")
	if err != nil {
		return nil, err
	}
	dirty := strings.Fields(changed)
	for _, file := range strings.Fields(untracked) {
		if !slices.ContainsFunc(toolCaches, func(d string) bool { return strings.HasPrefix(file, d) }) {
			dirty = append(dirty, file)
		}
	}
	return dirty, nil
}
