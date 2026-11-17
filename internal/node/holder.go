package node

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/google/uuid"

	"github.com/afonsodelgado/factory/internal/gitclone"
)

const (
	HolderSchema    = 1
	holderRefPrefix = "refs/factory/holders/"
)

type Holder struct {
	Schema      int    `json:"schema"`
	Incarnation string `json:"incarnation"`
	Node        string `json:"node"`
	Previous    string `json:"previous,omitempty"`
	Generation  int64  `json:"generation"`
	Checkpoint  string `json:"checkpoint"`
	Operation   string `json:"operation"`
}

func HolderRef(incarnation string) string { return holderRefPrefix + incarnation }

func (h Holder) Next(node, checkpoint, operation string) Holder {
	return Holder{Schema: HolderSchema, Incarnation: h.Incarnation, Node: node, Previous: h.Node, Generation: h.Generation + 1, Checkpoint: checkpoint, Operation: operation}
}

func (h Holder) Validate() error {
	var problems []error
	if h.Schema != HolderSchema {
		problems = append(problems, fmt.Errorf("holder schema %d is not %d; update factory on this node", h.Schema, HolderSchema))
	}
	for name, id := range map[string]string{"incarnation": h.Incarnation, "node": h.Node, "operation": h.Operation} {
		if uuid.Validate(id) != nil {
			problems = append(problems, fmt.Errorf("holder %s %q is not a UUID", name, id))
		}
	}
	if h.Previous != "" && uuid.Validate(h.Previous) != nil {
		problems = append(problems, fmt.Errorf("holder previous node %q is not a UUID", h.Previous))
	}
	if h.Generation < 1 {
		problems = append(problems, errors.New("holder generation starts at 1"))
	}
	if !isDigest(h.Checkpoint) {
		problems = append(problems, fmt.Errorf("holder checkpoint %q is not a sha256 digest", h.Checkpoint))
	}
	return errors.Join(problems...)
}

func (h Holder) Encode() ([]byte, error) {
	if err := h.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(h)
}

func DecodeHolder(data []byte) (Holder, error) {
	var h Holder
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&h); err != nil {
		return Holder{}, fmt.Errorf("the holder record is not one this node reads: %w", err)
	}
	return h, h.Validate()
}

func isDigest(s string) bool {
	raw, err := hex.DecodeString(s)
	return err == nil && len(raw) == 32
}

const holderFile = "holder.json"

type HolderChanged struct{ Now Holder }

func (e HolderChanged) Error() string {
	if e.Now.Node == "" {
		return "the holder record changed while this node claimed it"
	}
	return fmt.Sprintf("node %s claimed this factory first, at generation %d", e.Now.Node, e.Now.Generation)
}

type Holders struct {
	Dir string

	mu sync.Mutex
}

func (hs *Holders) Read(ctx context.Context, gitURL, incarnation string) (Holder, string, error) {
	hs.mu.Lock()
	defer hs.mu.Unlock()
	return hs.read(ctx, gitURL, incarnation)
}

func (hs *Holders) read(ctx context.Context, gitURL, incarnation string) (Holder, string, error) {
	ref := HolderRef(incarnation)
	if err := hs.ensure(ctx); err != nil {
		return Holder{}, "", err
	}
	listed, err := gitclone.Git(ctx, hs.Dir, "ls-remote", "--", gitURL, ref)
	if err != nil {
		return Holder{}, "", fmt.Errorf("cannot read who holds the factory from %s: %w", gitURL, err)
	}
	sha, _, _ := strings.Cut(listed, "\t")
	if sha == "" {
		return Holder{}, "", nil
	}
	if _, err := gitclone.Git(ctx, hs.Dir, "fetch", "-q", "--no-tags", "--", gitURL, "+"+ref+":"+ref); err != nil {
		return Holder{}, "", fmt.Errorf("cannot fetch the holder record from %s: %w", gitURL, err)
	}
	fetched, err := gitclone.Git(ctx, hs.Dir, "rev-parse", "--verify", ref)
	if err != nil {
		return Holder{}, "", err
	}
	data, err := gitclone.Git(ctx, hs.Dir, "show", fetched+":"+holderFile)
	if err != nil {
		return Holder{}, "", err
	}
	h, err := DecodeHolder([]byte(data))
	return h, fetched, err
}

func (hs *Holders) Claim(ctx context.Context, gitURL, expected string, h Holder) (string, error) {
	hs.mu.Lock()
	defer hs.mu.Unlock()
	data, err := h.Encode()
	if err != nil {
		return "", err
	}
	if err := hs.ensure(ctx); err != nil {
		return "", err
	}
	commit, err := hs.record(ctx, h, data)
	if err != nil {
		return "", err
	}
	ref := HolderRef(h.Incarnation)
	_, pushErr := gitclone.Git(ctx, hs.Dir, "push", "-q", "--force-with-lease="+ref+":"+expected, "--", gitURL, commit+":"+ref)
	now, sha, err := hs.read(ctx, gitURL, h.Incarnation)
	switch {
	case err != nil:
		return "", errors.Join(pushErr, err)
	case now.Node == h.Node && now.Operation == h.Operation && now.Generation == h.Generation:
		return sha, nil
	case pushErr == nil:
		return "", HolderChanged{Now: now}
	case sha != expected:
		return "", HolderChanged{Now: now}
	}
	return "", fmt.Errorf("cannot write the holder record to %s: %w", gitURL, pushErr)
}

func (hs *Holders) ensure(ctx context.Context) error {
	if _, err := os.Stat(filepath.Join(hs.Dir, "HEAD")); err == nil {
		return nil
	}
	if err := os.MkdirAll(hs.Dir, 0o700); err != nil {
		return err
	}
	_, err := gitclone.Git(ctx, hs.Dir, "init", "-q", "--bare")
	return err
}

func (hs *Holders) record(ctx context.Context, h Holder, data []byte) (string, error) {
	blob, err := hs.git(ctx, string(data), "hash-object", "-w", "--stdin")
	if err != nil {
		return "", err
	}
	tree, err := hs.git(ctx, "100644 blob "+blob+"\t"+holderFile+"\n", "mktree")
	if err != nil {
		return "", err
	}
	return hs.git(ctx, "", "commit-tree", tree, "-m", fmt.Sprintf("factory %s held by %s, generation %d", h.Incarnation, h.Node, h.Generation))
}

func (hs *Holders) git(ctx context.Context, stdin string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", hs.Dir}, args...)...)
	cmd.Stdin = strings.NewReader(stdin)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_AUTHOR_NAME=factory", "GIT_AUTHOR_EMAIL=factory@localhost", "GIT_COMMITTER_NAME=factory", "GIT_COMMITTER_EMAIL=factory@localhost")
	out, err := cmd.Output()
	if ctxErr := ctx.Err(); err != nil && ctxErr != nil {
		return "", fmt.Errorf("git %s: %w", args[0], ctxErr)
	}
	if err != nil {
		return "", fmt.Errorf("git %s: %w", args[0], err)
	}
	return strings.TrimSpace(string(out)), nil
}
