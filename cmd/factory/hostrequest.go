package main

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/goccy/go-yaml"

	"github.com/afonsodelgado/factory/internal/factory"
	"github.com/afonsodelgado/factory/internal/host"
	"github.com/afonsodelgado/factory/internal/hub"
	"github.com/afonsodelgado/factory/internal/install"
	"github.com/afonsodelgado/factory/internal/remote"
	"github.com/afonsodelgado/factory/internal/state"
	"github.com/afonsodelgado/factory/internal/tmux"
)

const cloneTimeout = 10 * time.Minute

func answerRequest(in io.Reader) remote.Reply {
	reply := remote.Reply{Protocol: remote.Protocol, Status: remote.Rejected}
	cfg, err := state.LoadConfig()
	if err == nil {
		reply.Server, err = remote.ServerID(cfg.StateDir)
	}
	if err != nil {
		reply.Status, reply.Error = remote.Failed, err.Error()
		return reply
	}
	var req remote.Request
	switch {
	case json.NewDecoder(in).Decode(&req) != nil:
		reply.Error = "the request is not factory protocol JSON"
	case req.Protocol != remote.Protocol:
		reply.Error = fmt.Sprintf("this server speaks factory protocol %d and the request %d; run factory update on both machines", remote.Protocol, req.Protocol)
	case req.Server != "" && req.Server != reply.Server:
		reply.Error = "this is not the server this remote was set up with; if it replaced the old one, remove the remote and add it again under a new name"
	default:
		reply.Data, reply.Status, err = serveRequest(cfg, req)
		if err != nil {
			reply.Error = err.Error()
		}
	}
	return reply
}

func serveRequest(cfg *state.Config, req remote.Request) (json.RawMessage, remote.Status, error) {
	switch req.Action {
	case "hello":
		return completed(remote.Hello{Server: mustServerID(cfg), Release: version, Protocol: remote.Protocol, Platform: runtime.GOOS + "/" + runtime.GOARCH})
	case "inventory":
		listings, err := inventory(cfg)
		if err != nil {
			return nil, remote.Failed, err
		}
		return completed(listings)
	case "create":
		var c remote.Create
		if err := json.Unmarshal(req.Data, &c); err != nil {
			return nil, remote.Rejected, err
		}
		if req.Op == "" {
			return nil, remote.Rejected, errors.New("a create needs an operation id")
		}
		return settled(host.Journal(cfg.StateDir, req.Op, req.Action, req.Data, true, func() host.Message {
			listing, err := createHere(cfg, c)
			if err != nil {
				return host.Message{T: "error", Error: err.Error()}
			}
			return host.Message{T: "done", Data: mustJSON(listing)}
		}))
	case "result":
		var path string
		if err := json.Unmarshal(req.Data, &path); err != nil {
			return nil, remote.Rejected, err
		}
		if !isStageResult(cfg, path) {
			return nil, remote.Rejected, fmt.Errorf("%s is not a stage result of a factory on this server", path)
		}
		return waitForFile(path), remote.Completed, nil
	case "doctor":
		var urls []string
		if err := json.Unmarshal(req.Data, &urls); err != nil {
			return nil, remote.Rejected, err
		}
		return completed(install.ServerDoctor(doctorOptions(), withClones(cfg, urls)))
	case "removal":
		f, err := targetFactory(cfg, req.Data)
		if err != nil {
			return nil, remote.Failed, err
		}
		removal := factory.PlanRemoval(f, cfg)
		work := factory.CheckWork(removal)
		preview := remote.Removal{What: whatGoes(f, removal), Worktree: removal.Worktree, WorktreeGone: !host.DirExists(removal.Worktree), Branches: removal.Branches, Dirty: work.Dirty, Unpushed: work.Unpushed}
		if !work.Empty() {
			preview.Risk = workAtRisk(f, work)
		}
		return completed(preview)
	case "delete":
		if reply, done := host.Finished(cfg.StateDir, req.Op, req.Action); done {
			return settled(reply)
		}
		f, err := targetFactory(cfg, req.Data)
		if err != nil {
			return nil, remote.Failed, err
		}
		return forward(cfg, "delete", req.Op, mustJSON(hub.DeleteRequest{Repo: f.Repo, Slug: f.Slug}))
	case "stage", "review", "qa-claim", "qa-return":
		return forward(cfg, req.Action, req.Op, req.Data)
	case "view":
		return forward(cfg, "view", "", nil)
	case "link", "opened", "events", "push", "push-test":
		return forward(cfg, req.Action, "", req.Data)
	case "wake", "pause", "archive", "unarchive", "answer":
		f, err := targetFactory(cfg, req.Data)
		if err != nil {
			return nil, remote.Failed, err
		}
		data, err := keyedFor(f, req.Data)
		if err != nil {
			return nil, remote.Rejected, err
		}
		return forward(cfg, req.Action, req.Op, data)
	case "attach":
		f, err := targetFactory(cfg, req.Data)
		if err != nil {
			return nil, remote.Failed, err
		}
		var pane remote.PaneTarget
		if err := json.Unmarshal(req.Data, &pane); err != nil {
			return nil, remote.Rejected, err
		}
		id, err := host.PrepareView(tmux.Client{Socket: cfg.Tmux.Socket}, f, pane.Role, pane.Token)
		if err != nil {
			return nil, remote.Failed, err
		}
		return completed(id)
	}
	return nil, remote.Rejected, fmt.Errorf("this server does not know the action %q; run factory update on both machines", req.Action)
}

func completed(value any) (json.RawMessage, remote.Status, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, remote.Failed, err
	}
	return data, remote.Completed, nil
}

func mustServerID(cfg *state.Config) string {
	id, _ := remote.ServerID(cfg.StateDir)
	return id
}

func forward(cfg *state.Config, kind, op string, data json.RawMessage) (json.RawMessage, remote.Status, error) {
	if err := startHost(cfg, tmux.Client{Socket: cfg.Tmux.Socket}); err != nil {
		return nil, remote.Failed, err
	}
	reply, err := send(cfg.SocketPath(), hub.Message{T: kind, Op: op, Pane: "cli", Data: data})
	switch {
	case err != nil && op != "":
		return nil, remote.Uncertain, err
	case err != nil:
		return nil, remote.Failed, err
	}
	return settled(reply)
}

func settled(reply host.Message) (json.RawMessage, remote.Status, error) {
	switch reply.T {
	case "done":
		return reply.Data, remote.Completed, nil
	case "uncertain":
		return nil, remote.Uncertain, errors.New(reply.Error)
	}
	return nil, remote.Failed, errors.New(reply.Error)
}

func inventory(cfg *state.Config) ([]remote.Listing, error) {
	registry, err := state.LoadRegistry(cfg.RegistryPath())
	if err != nil {
		return nil, err
	}
	origins := map[string]string{}
	listings := []remote.Listing{}
	for _, e := range registry.Factories {
		if _, known := origins[e.Repo]; !known {
			origins[e.Repo] = originOf(e.Repo)
		}
		listings = append(listings, remote.Listing{ID: e.Slug, GitURL: origins[e.Repo], Incarnation: cmp.Or(e.Incarnation, e.Key())})
	}
	return listings, nil
}

func originOf(repo string) string {
	url, err := host.Git(repo, "config", "--get", "remote.origin.url")
	if err != nil {
		return ""
	}
	return url
}

func createHere(cfg *state.Config, c remote.Create) (remote.Listing, error) {
	if c.ID == "" || factory.Slugify(c.ID) != c.ID {
		return remote.Listing{}, fmt.Errorf("%q is not a factory id", c.ID)
	}
	listings, err := inventory(cfg)
	if err != nil {
		return remote.Listing{}, err
	}
	if i := slices.IndexFunc(listings, func(l remote.Listing) bool { return l.ID == c.ID }); i >= 0 {
		if listings[i].GitURL != c.GitURL {
			return remote.Listing{}, fmt.Errorf("this server already has a factory %s, in %s", c.ID, listings[i].GitURL)
		}
		return listings[i], nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), cloneTimeout)
	defer cancel()
	clone, err := remote.EnsureClone(ctx, cfg.StateDir, c.GitURL)
	if err != nil {
		return remote.Listing{}, err
	}
	if err := writeProfile(clone, c.Profile); err != nil {
		return remote.Listing{}, err
	}
	if len(c.Rules) > 0 {
		if err := state.WriteFileAtomic(cfg.RulesPath(), c.Rules); err != nil {
			return remote.Listing{}, err
		}
	}
	if _, err := install.Init(clone, cfg.StateDir); err != nil {
		return remote.Listing{}, err
	}
	if _, status, err := forward(cfg, "repo", "", mustJSON(hub.RepoRequest{Repo: clone})); status != remote.Completed {
		return remote.Listing{}, err
	}
	open := hub.OpenRequest{Name: c.ID, Repo: clone, Prompt: c.Prompt, Pipeline: c.Pipeline, Stages: c.Stages, Agents: c.Agents, Supervisor: c.Supervisor}
	if _, status, err := forward(cfg, "open", "", mustJSON(open)); status != remote.Completed {
		return remote.Listing{}, err
	}
	registry, err := state.LoadRegistry(cfg.RegistryPath())
	if err != nil {
		return remote.Listing{}, err
	}
	entry := registry.Find(clone, c.ID)
	if entry == nil {
		return remote.Listing{}, fmt.Errorf("%s did not start on this server; its host window says why", c.ID)
	}
	return remote.Listing{ID: c.ID, GitURL: c.GitURL, Incarnation: cmp.Or(entry.Incarnation, entry.Key())}, nil
}

func writeProfile(clone string, profile []byte) error {
	if len(profile) == 0 {
		return nil
	}
	var p state.Profile
	if err := yaml.Unmarshal(profile, &p); err != nil {
		return fmt.Errorf("the repo's .factory/config.yml: %w", err)
	}
	if err := os.MkdirAll(state.RepoDir(clone), 0o755); err != nil {
		return err
	}
	return state.WriteFileAtomic(state.RepoConfigPath(clone), profile)
}

func targetFactory(cfg *state.Config, data json.RawMessage) (factory.Factory, error) {
	var target remote.Target
	if err := json.Unmarshal(data, &target); err != nil {
		return factory.Factory{}, err
	}
	f, err := findFactory(cfg, "", target.ID)
	if err == nil && target.Incarnation != "" && host.Incarnation(f) != target.Incarnation {
		return f, fmt.Errorf("%s on this server is a newer factory than the one you acted on; nothing was done", target.ID)
	}
	return f, err
}

func keyedFor(f factory.Factory, data json.RawMessage) (json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return nil, err
	}
	delete(fields, "id")
	delete(fields, "incarnation")
	fields["Key"] = mustJSON(f.Key())
	return json.Marshal(fields)
}

func withClones(cfg *state.Config, urls []string) []string {
	for _, repo := range cfg.Repos {
		if url := originOf(repo); url != "" && remote.IsClone(cfg.StateDir, repo) {
			urls = append(urls, url)
		}
	}
	slices.Sort(urls)
	return slices.Compact(urls)
}

func mustJSON(value any) json.RawMessage {
	data, _ := json.Marshal(value)
	return data
}

func isStageResult(cfg *state.Config, path string) bool {
	clean := filepath.Clean(path)
	if clean != path || !strings.HasSuffix(clean, ".result.json") || filepath.Base(filepath.Dir(clean)) != "stages" {
		return false
	}
	for _, repo := range cfg.Repos {
		if strings.HasPrefix(clean, repo+string(filepath.Separator)) {
			return true
		}
	}
	return false
}
