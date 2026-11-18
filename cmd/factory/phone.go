package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/afonsodelgado/factory/internal/gitclone"
	"github.com/afonsodelgado/factory/internal/install"
	"github.com/afonsodelgado/factory/internal/node"
	"github.com/afonsodelgado/factory/internal/phone"
	"github.com/afonsodelgado/factory/internal/state"
	"github.com/afonsodelgado/factory/internal/ui/dialog"
)

const (
	phoneUsage = "usage: factory phone status | test | check"
	nodeAsk    = 2 * time.Second
	nodeWait   = 15 * time.Second
)

func runPhone(args []string) int {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, phoneUsage)
		return 2
	}
	cfg, err := state.LoadConfig()
	if err != nil {
		fmt.Fprintln(os.Stderr, "factory phone:", err)
		return 1
	}
	needsTailscale := (args[0] == "test" || args[0] == "check") && !cfg.TailscaleNode()
	switch {
	case needsTailscale:
		fmt.Fprintln(os.Stderr, "factory phone: "+node.NeedsTailscale(node.PhoneFeature, cfg.NodeName())+".\n"+node.AddTailscale+" adds it. Every factory keeps running.")
		return 1
	case args[0] == "status":
		err = printPhoneStatus(os.Stdout, cfg)
	case args[0] == "test":
		err = printTestPush(os.Stdout, cfg)
	case args[0] == "check":
		if summarize("factory phone check · checks only, changes nothing", phoneDoctor(cfg)) > 0 {
			return 1
		}
	default:
		fmt.Fprintln(os.Stderr, phoneUsage)
		return 2
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "factory phone:", err)
		return 1
	}
	return 0
}

func phoneStatus(cfg *state.Config) (dialog.PhoneContext, error) {
	subs, err := phone.Open(cfg.StateDir).Subscriptions()
	return dialog.PhoneContext{Machine: cfg.NodeName(), Tailscale: cfg.TailscaleNode(), Running: node.Answers(cfg.NodeSocket()), Hostname: cfg.NodeHostname(), Port: cfg.NodePort(), Subscriptions: len(subs)}, err
}

func nodesContext(cfg *state.Config, phone dialog.PhoneContext, known dialog.NodesContext) dialog.NodesContext {
	_, tailnet, _ := strings.Cut(cfg.NodeHostname(), ".")
	c := dialog.NodesContext{Machine: cfg.NodeName(), Tailnet: tailnet, Phone: phone, Owner: cfg.Node.Owner, Target: cfg.Node.Target, Repos: repoURLs(cfg.Repos)}
	ctx, cancel := context.WithTimeout(context.Background(), nodeAsk)
	defer cancel()
	local := node.Unix(cfg.NodeSocket())
	var snap node.Snapshot
	if local.Do(ctx, http.MethodGet, "/api/snapshot", nil, &snap) != nil {
		return c
	}
	c.Self, c.Version, c.Factories = snap.Node, snap.Version, len(snap.Factories)
	for _, f := range snap.Factories {
		if !f.NeedsSince.IsZero() {
			c.NeedsYou++
		}
	}
	if snap.Roster != nil {
		c.Home, c.Others = snap.Roster.Home, rosterRows(*snap.Roster, known.Others)
		c.Leaving = snap.Roster.Leaving
		c.Offer = offerHomeOnce(cfg.StateDir, *snap.Roster)
	}
	return c
}

func rosterHome(cfg *state.Config) string {
	ctx, cancel := context.WithTimeout(context.Background(), nodeAsk)
	defer cancel()
	var roster node.Roster
	if node.Unix(cfg.NodeSocket()).Do(ctx, http.MethodGet, "/api/nodes", nil, &roster) != nil {
		return ""
	}
	return roster.Home
}

func homeOfferedPath(stateDir string) string {
	return filepath.Join(stateDir, "hub", "home-offered.json")
}

func offerHomeOnce(stateDir string, roster node.Roster) string {
	var offered []string
	if data, err := os.ReadFile(homeOfferedPath(stateDir)); err == nil {
		json.Unmarshal(data, &offered)
	}
	i := slices.IndexFunc(roster.Members, func(m node.RosterMember) bool {
		return !m.Self && m.OS == "linux" && "https://"+m.Name != roster.Home && !slices.Contains(offered, m.ID)
	})
	if i < 0 {
		return ""
	}
	id := roster.Members[i].ID
	data, _ := json.Marshal(append(offered, id))
	if os.MkdirAll(filepath.Dir(homeOfferedPath(stateDir)), 0o700) != nil || state.WriteFileAtomic(homeOfferedPath(stateDir), data) != nil {
		return ""
	}
	return id
}

func rosterRows(roster node.Roster, known []dialog.NodeRow) []dialog.NodeRow {
	var rows []dialog.NodeRow
	for _, m := range roster.Members {
		if m.Self {
			continue
		}
		row := dialog.NodeRow{ID: m.ID, Name: m.Name, OS: m.OS}
		if i := slices.IndexFunc(known, func(r dialog.NodeRow) bool { return r.ID == m.ID }); i >= 0 {
			row = known[i]
			row.Name, row.OS = m.Name, m.OS
		}
		rows = append(rows, row)
	}
	return rows
}

func shareableOrigin(repo string) string {
	url := gitclone.Origin(repo)
	if url == "" || gitclone.CheckURL(url) != nil {
		return ""
	}
	return url
}

func repoURLs(repos []string) []dialog.RepoURL {
	var urls []dialog.RepoURL
	for _, repo := range repos {
		if url := shareableOrigin(repo); url != "" {
			urls = append(urls, dialog.RepoURL{Name: strings.TrimSuffix(path.Base(url), ".git"), URL: url})
		}
	}
	return urls
}

func printPhoneStatus(w io.Writer, cfg *state.Config) error {
	status, err := phoneStatus(cfg)
	if err != nil {
		return err
	}
	url := "unknown; put node.hostname, this machine's ts.net name, in " + cfg.ConfigFile()
	if status.Hostname != "" {
		url = "https://" + status.Hostname
	}
	if home := rosterHome(cfg); home != "" && home != url {
		url = home + ", the phone app's home"
	}
	running := "not running; start it with factory node run"
	if status.Running {
		running = "running"
	}
	if !cfg.TailscaleNode() {
		fmt.Fprintf(w, "node      %s\nurl       none: %s\nport      127.0.0.1:%d, this machine only\nnotify    %d subscribed\n%s serves the app on your tailnet. Every factory keeps running.\n", running, node.NeedsTailscale(node.PhoneFeature, cfg.NodeName()), status.Port, status.Subscriptions, node.AddTailscale)
		return nil
	}
	fmt.Fprintf(w, "node      %s\nurl       %s\nport      127.0.0.1:%d\nnotify    %d subscribed\n", running, url, status.Port, status.Subscriptions)
	keys, err := phone.Open(cfg.StateDir).KeyState()
	if health, detail := keys.Health(); health != phone.HealthNoKey {
		fmt.Fprintln(w, strings.TrimSpace("push key  "+keys.Fingerprint+" "+detail))
	}
	return err
}

var errPushRejected = errors.New("a test notification did not reach the phone's push service")

func printTestPush(w io.Writer, cfg *state.Config) error {
	client, err := localNode(cfg)
	if err != nil {
		return err
	}
	var results []phone.Delivery
	if err := client.Do(context.Background(), http.MethodPost, "/api/push/test", nil, &results); err != nil {
		return err
	}
	if len(results) == 0 {
		fmt.Fprintln(w, "No phone has turned on notifications yet.")
		return nil
	}
	var failed error
	for _, r := range results {
		outcome := "accepted by the push service; check that the phone shows it"
		if !r.Accepted {
			outcome, failed = r.Problem, errPushRejected
		}
		fmt.Fprintf(w, "%s: %s\n", r.Subscription, outcome)
	}
	return failed
}

func phoneDoctor(cfg *state.Config) []install.Check {
	o := doctorOptions()
	o.Config = cfg
	return install.NodeChecks(o)
}

type phoneActions struct{ cfg *state.Config }

func (a phoneActions) Status() (dialog.PhoneContext, error) { return phoneStatus(a.cfg) }

func (a phoneActions) Test() (string, error) {
	var b strings.Builder
	err := printTestPush(&b, a.cfg)
	outcome := strings.ReplaceAll(strings.TrimSpace(b.String()), "\n", "; ")
	if errors.Is(err, errPushRejected) {
		return "", errors.New(outcome)
	}
	return outcome, err
}

var checkMarks = map[install.Mark]string{install.Pass: "ok", install.Fail: "fail", install.Skip: "skip", install.Off: "skip", install.Warn: "warn"}

func (a phoneActions) Check() []dialog.CheckRow {
	var rows []dialog.CheckRow
	for _, c := range phoneDoctor(a.cfg) {
		rows = append(rows, dialog.CheckRow{Name: c.Name, Mark: checkMarks[c.Mark], Detail: c.Detail, Fix: c.Fix})
	}
	return rows
}

type nodeActions struct {
	phoneActions
	local *node.Client
	peers *http.Client
	ts    node.Tailscale
}

func newNodeActions(cfg *state.Config) nodeActions {
	return nodeActions{phoneActions: phoneActions{cfg}, local: node.Unix(cfg.NodeSocket()), peers: node.PeerHTTP(), ts: node.Tailscale{Run: node.System}}
}

func (a nodeActions) Ask(ctx context.Context, name string, repos []string) (dialog.NodeAnswer, error) {
	peer := node.Remote(name, a.peers)
	var answer dialog.NodeAnswer
	snapshotCtx, cancel := context.WithTimeout(ctx, node.ProbeWait)
	err := peer.Do(snapshotCtx, http.MethodGet, "/api/snapshot", nil, &answer.Snapshot)
	cancel()
	if err != nil {
		return answer, err
	}
	readyCtx, cancel := context.WithTimeout(ctx, node.ReadyWait+node.ProbeWait)
	defer cancel()
	if err := peer.Do(readyCtx, http.MethodGet, "/api/readiness?"+url.Values{"repo": repos}.Encode(), nil, &answer.Report); err != nil {
		return answer, err
	}
	if answer.Snapshot.Supports(node.CanNeeds) {
		answer.Needs, _ = askNeeds(ctx, peer, repoNeeds(a.cfg.Repos))
	}
	return answer, nil
}

func repoNeeds(repos []string) []node.RepoNeeds {
	var asked []node.RepoNeeds
	for _, repo := range repos {
		if url := shareableOrigin(repo); url != "" {
			asked = append(asked, node.RepoNeeds{URL: url, Needs: install.RepoNeeds(repo)})
		}
	}
	return asked
}

func askNeeds(ctx context.Context, peer *node.Client, repos []node.RepoNeeds) (node.NeedsReport, error) {
	ctx, cancel := context.WithTimeout(ctx, node.ReadyWait)
	defer cancel()
	var report node.NeedsReport
	return report, peer.Do(ctx, http.MethodPost, node.NeedsRoute, node.NeedsRequest{Repos: repos}, &report)
}

func (a nodeActions) Seen(ctx context.Context, name string) (node.Peer, error) {
	inv, err := a.ts.Inventory(ctx)
	if err != nil {
		return node.Peer{}, err
	}
	peer, ok := inv.Named(name)
	if !ok {
		return node.Peer{}, fmt.Errorf("tailscale status on this machine lists no %s", name)
	}
	return peer, nil
}

func (a nodeActions) Machines(ctx context.Context) (node.Inventory, error) {
	return a.ts.Inventory(ctx)
}

func (a nodeActions) Candidates(ctx context.Context) ([]node.Candidate, error) {
	var listed node.Candidates
	err := a.local.Do(ctx, http.MethodGet, "/api/nodes/candidates", nil, &listed)
	return listed.Candidates, err
}

func (a nodeActions) Inspect(ctx context.Context, name string) (node.CheckReport, error) {
	var report node.CheckReport
	err := a.local.Do(ctx, http.MethodPost, "/api/nodes/check", node.NodeRequest{Name: name}, &report)
	return report, err
}

func (a nodeActions) Add(ctx context.Context, op, name string, replaceKey bool) (node.AddResult, error) {
	var result node.AddResult
	err := a.local.Do(ctx, http.MethodPost, "/api/nodes", node.NodeRequest{Operation: op, Name: name, ReplaceKey: replaceKey}, &result)
	return result, err
}

func (a nodeActions) Remove(op, id string) (node.Roster, error) {
	ctx, cancel := context.WithTimeout(context.Background(), nodeWait)
	defer cancel()
	var roster node.Roster
	err := a.local.Do(ctx, http.MethodPost, "/api/nodes/"+url.PathEscape(id)+"/remove", node.RemoveRequest{Operation: op}, &roster)
	return roster, err
}

func (a nodeActions) MoveHome(op, id string) (node.Roster, error) {
	ctx, cancel := context.WithTimeout(context.Background(), nodeWait)
	defer cancel()
	var roster node.Roster
	err := a.local.Do(ctx, http.MethodPost, "/api/nodes/home", node.HomeRequest{Operation: op, Node: id}, &roster)
	return roster, err
}

func (a nodeActions) SetTarget(name string) error {
	return state.UpdateMachine(a.cfg.ConfigDir, func(m *state.Machine) error {
		m.Node.Target = name
		return nil
	})
}

func probeNode(name string, repo dialog.RepoOption) (dialog.NodeReadiness, error) {
	short, _, _ := strings.Cut(name, ".")
	ctx, cancel := context.WithTimeout(context.Background(), node.ReadyWait+node.ProbeWait)
	defer cancel()
	peer := node.Remote(name, node.PeerHTTP())
	unanswered := func(err error) (dialog.NodeReadiness, error) {
		return dialog.NodeReadiness{}, fmt.Errorf("%s did not answer its readiness check: %w", short, err)
	}
	var snap node.Snapshot
	if err := peer.Do(ctx, http.MethodGet, "/api/snapshot", nil, &snap); err != nil {
		return unanswered(err)
	}
	var report node.Report
	if err := peer.Do(ctx, http.MethodGet, "/api/readiness", nil, &report); err != nil {
		return unanswered(err)
	}
	for _, c := range report.Checks {
		if c.Mark == node.MarkFail {
			return dialog.NodeReadiness{}, fmt.Errorf("%s can't start factories yet: %s: %s", short, c.Name, c.Detail)
		}
	}
	if !snap.Supports(node.CanNeeds) {
		return dialog.NodeReadiness{Unchecked: true, PeerVersion: snap.Version, HubVersion: version}, nil
	}
	needs, err := askNeeds(context.Background(), peer, []node.RepoNeeds{{URL: repo.GitURL, Needs: install.RepoNeeds(repo.Path)}})
	if err != nil {
		return unanswered(err)
	}
	readiness := dialog.NodeReadiness{}
	if len(needs.Repos) > 0 {
		readiness.Needs = needs.Repos[0]
	}
	return readiness, nil
}

func (a nodeActions) Clone(ctx context.Context, name, op, repo string) error {
	return node.Remote(name, a.peers).Do(ctx, http.MethodPost, "/api/clone", node.CloneRequest{Operation: op, URL: repo}, nil)
}

func (a nodeActions) on(name string) *node.Client {
	if name == "" {
		return a.local
	}
	return node.Remote(name, a.peers)
}

func plainRefusal(err error) error {
	var refusal node.StatusError
	if errors.As(err, &refusal) {
		return errors.New(refusal.Message)
	}
	return err
}

func (a nodeActions) transfer(ctx context.Context, on, method, path string, body any) (node.Progress, error) {
	var p node.Progress
	return p, plainRefusal(a.on(on).Do(ctx, method, path, body, &p))
}

func (a nodeActions) Ready(ctx context.Context, on string) (node.Report, error) {
	var report node.Report
	return report, plainRefusal(a.on(on).Do(ctx, http.MethodGet, "/api/readiness", nil, &report))
}

func (a nodeActions) Start(ctx context.Context, on string, r node.MoveRequest) (node.Progress, error) {
	return a.transfer(ctx, on, http.MethodPost, "/api/transfers", r)
}

func (a nodeActions) Progress(ctx context.Context, on, operation string) (node.Progress, error) {
	return a.transfer(ctx, on, http.MethodGet, "/api/transfers/"+url.PathEscape(operation), nil)
}

func (a nodeActions) Cancel(ctx context.Context, on, operation string) (node.Progress, error) {
	return a.transfer(ctx, on, http.MethodPost, "/api/transfers/"+url.PathEscape(operation)+"/cancel", struct{}{})
}

func (a nodeActions) Takeover(ctx context.Context, on string, r node.TakeoverRequest) (node.Progress, error) {
	var answer struct {
		node.Progress
		Warning *node.Unconfirmed `json:"warning"`
	}
	err := a.on(on).Do(ctx, http.MethodPost, "/api/transfers/takeover", r, &answer)
	var refusal node.StatusError
	if errors.As(err, &refusal) && refusal.Status == http.StatusPreconditionRequired && answer.Warning != nil {
		return node.Progress{}, *answer.Warning
	}
	return answer.Progress, plainRefusal(err)
}

func (a nodeActions) Moves(ctx context.Context, on string) ([]node.Progress, error) {
	var moves []node.Progress
	return moves, plainRefusal(a.on(on).Do(ctx, http.MethodGet, "/api/transfers", nil, &moves))
}

func (a nodeActions) batch(ctx context.Context, method, path string, body any) (node.BatchProgress, error) {
	var b node.BatchProgress
	return b, plainRefusal(a.local.Do(ctx, method, path, body, &b))
}

func (a nodeActions) MoveAll(ctx context.Context, op, target string) (node.BatchProgress, error) {
	return a.batch(ctx, http.MethodPost, "/api/transfers/all", node.MoveAllRequest{Operation: op, Target: target})
}

func (a nodeActions) Batch(ctx context.Context, op string) (node.BatchProgress, error) {
	return a.batch(ctx, http.MethodGet, "/api/transfers/all/"+url.PathEscape(op), nil)
}

func (a nodeActions) CancelAll(ctx context.Context, op string) (node.BatchProgress, error) {
	return a.batch(ctx, http.MethodPost, "/api/transfers/all/"+url.PathEscape(op)+"/cancel", struct{}{})
}
