package install

import (
	"cmp"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"maps"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/afonsodelgado/factory/internal/catalog"
	"github.com/afonsodelgado/factory/internal/factory"
	"github.com/afonsodelgado/factory/internal/node"
	"github.com/afonsodelgado/factory/internal/state"
)

func toolChecks(o Options) []Check {
	if o.Config == nil {
		return nil
	}
	var checks []Check
	if _, err := os.Stat(o.Config.RulesPath()); err != nil {
		checks = append(checks, Check{Name: "rules", Mark: Warn, Detail: "no rules file, so stage agents get only the repo's own rules", Fix: "write your coding rules to " + tilde(o.Config.RulesPath())})
	}
	used := toolsInUse(o)
	if repos, ok := used[state.ToolCodex]; ok {
		checks = append(checks, codexChecks(o, repos)...)
	}
	if _, ok := used[state.ToolOpenCode]; ok {
		checks = append(checks, openCodeChecks(o)...)
	}
	return append(checks, agentValueChecks(o)...)
}

func toolsInUse(o Options) map[string][]string {
	used := map[string][]string{}
	note := func(setting state.AgentSetting, repos ...string) {
		if tool := setting.ToolName(); tool != state.ToolClaude {
			used[tool] = append(used[tool], repos...)
		}
	}
	note(o.Config.Supervisor, o.Repos...)
	for _, setting := range o.Config.Agents {
		note(setting, o.Repos...)
	}
	for _, repo := range o.Repos {
		profile, _ := state.ReadProfile(repo)
		note(profile.Supervisor, repo)
		for _, setting := range profile.Agents {
			note(setting, repo)
		}
	}
	for _, entry := range liveEntries(o.Config) {
		note(entry.Supervisor, entry.Repo)
		for _, setting := range entry.Agents {
			note(setting, entry.Repo)
		}
	}
	for tool, repos := range used {
		slices.Sort(repos)
		used[tool] = slices.Compact(repos)
	}
	return used
}

func liveEntries(cfg *state.Config) []state.Entry {
	registry, err := state.LoadRegistry(cfg.RegistryPath())
	if err != nil {
		return nil
	}
	return slices.DeleteFunc(registry.Factories, func(e state.Entry) bool { return e.Archived })
}

func agentValueChecks(o Options) []Check {
	catalogs := catalog.Local(o.Config.StateDir)
	results := []error{
		catalogs.ValidateAgents(o.ConfigFile, o.Config.Agents),
		catalogs.Validate(o.ConfigFile, "supervisor", o.Config.Supervisor),
	}
	for _, repo := range o.Repos {
		profile, _ := state.ReadProfile(repo)
		results = append(results, catalogs.ValidateAgents(tilde(state.ProfilePath(repo)), profile.Agents), catalogs.Validate(tilde(state.ProfilePath(repo)), "supervisor", profile.Supervisor))
	}
	var checks []Check
	for _, err := range results {
		switch {
		case err == nil:
		case errors.Is(err, catalog.ErrUnavailable):
			checks = append(checks, Check{Name: "agents", Mark: Warn, Detail: err.Error()})
		default:
			checks = append(checks, Check{Name: "agents", Mark: Fail, Detail: "a stage would fail inside its pane", Lines: []string{err.Error()}, Fix: "pick a listed model and effort in that file"})
		}
	}
	return checks
}

func codexChecks(o Options, repos []string) []Check {
	checks := []Check{tool("codex", "0.156", "codex", []string{"--version"}, "npm install -g @openai/codex")}
	if checks[0].Mark == Fail {
		return checks
	}
	home := state.CodexHome()
	if _, err := catalog.Codex(home); err != nil {
		return append(checks, Check{Name: "codex login", Mark: Fail, Detail: err.Error(), Fix: "codex login"})
	}
	checks = append(checks, Check{Name: "codex login", Mark: Pass, Detail: "ChatGPT subscription"})
	var untrusted []string
	for _, repo := range repos {
		if !catalog.CodexTrusts(home, repo) {
			untrusted = append(untrusted, tilde(repo))
		}
	}
	if len(untrusted) > 0 {
		checks = append(checks, Check{Name: "codex trust", Mark: Fail, Detail: "Codex would stop at a trust prompt in " + strings.Join(untrusted, ", "), Fix: "run codex once in each repo and choose Trust and continue"})
	}
	return append(checks, codexSandbox(o))
}

func codexSandbox(o Options) Check {
	dir, err := os.MkdirTemp("", "factory-sandbox-")
	if err != nil {
		return Check{Name: "codex sandbox", Mark: Fail, Detail: err.Error()}
	}
	defer os.RemoveAll(dir)
	dir, _ = filepath.EvalSymlinks(dir)
	repo, remote := filepath.Join(dir, "repo"), filepath.Join(dir, "remote.git")
	worktree := filepath.Join(factory.WorktreesDir(repo), "probe")
	setup := [][]string{{"init", "-q", "--bare", remote}, {"init", "-q", repo}, {"-C", repo, "remote", "add", "origin", remote}, {"-C", repo, "-c", "user.name=f", "-c", "user.email=f@f", "commit", "-q", "--allow-empty", "-m", "init"}, {"-C", repo, "worktree", "add", "-q", "-b", "probe", worktree}}
	for _, args := range setup {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			return Check{Name: "codex sandbox", Mark: Fail, Detail: "git " + args[0] + ": " + strings.TrimSpace(string(out))}
		}
	}
	probe := `git -c user.name=f -c user.email=f@f commit -q --allow-empty -m probe || { echo "commit is blocked"; exit 1; }
git push -q origin HEAD:refs/heads/probe || { echo "push is blocked"; exit 1; }
gh auth status >/dev/null 2>&1 || { echo "gh cannot reach its login"; exit 1; }`
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	roots := append(factory.CodexWritableDirs(worktree, o.Config.Codex.WritableDirs), remote)
	quoted := make([]string, len(roots))
	for i, root := range roots {
		quoted[i] = strconv.Quote(root)
	}
	cmd := exec.CommandContext(ctx, "codex", "sandbox", "-c", `sandbox_mode="workspace-write"`, "-c", "sandbox_workspace_write.network_access=true", "-c", "sandbox_workspace_write.writable_roots=["+strings.Join(quoted, ", ")+"]", "--", "sh", "-c", probe)
	cmd.Dir = worktree
	if out, err := cmd.CombinedOutput(); err != nil {
		return Check{Name: "codex sandbox", Mark: Fail, Detail: "a stage in Codex would stop: " + strings.TrimSpace(string(out)), Fix: "add the blocked directories to codex.writable_dirs in " + tilde(o.ConfigFile)}
	}
	return Check{Name: "codex sandbox", Mark: Pass, Detail: "commit, push and gh work without approvals"}
}

func openCodeChecks(o Options) []Check {
	checks := []Check{tool("opencode", "1.18", "opencode", []string{"--version"}, "curl -fsSL https://opencode.ai/install | bash")}
	if checks[0].Mark == Fail {
		return checks
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	catalog.RefreshOpenCode(ctx, o.Config.StateDir)
	models, err := catalog.OpenCode(o.Config.StateDir)
	if err != nil || len(models) == 0 {
		return append(checks, Check{Name: "opencode models", Mark: Fail, Detail: "no free models listed", Fix: "opencode models opencode"})
	}
	checks = append(checks, Check{Name: "opencode models", Mark: Pass, Detail: fmt.Sprintf("%d free models", len(models))})
	plan, err := NewOpenCodePlan(state.OpenCodeConfigDir(), o.Binary)
	if err != nil || len(plan.Pending()) > 0 {
		return append(checks, Check{Name: "opencode plugin", Mark: Fail, Detail: "OpenCode panes cannot report their status", Fix: "factory bootstrap"})
	}
	return append(checks, Check{Name: "opencode plugin", Mark: Pass, Detail: "installed"})
}

var pushServices = []string{"fcm.googleapis.com:443", "web.push.apple.com:443", "updates.push.services.mozilla.com:443"}

func NodeChecks(o Options) []Check {
	if o.Config == nil {
		return nil
	}
	run := o.Run
	if run == nil {
		run = node.System
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	checks := tailnetChecks(ctx, o.Config, node.Tailscale{Run: run})
	checks = append(checks, serviceChecks(ctx, o, node.NewService(o.Config, o.Binary, o.Home, run))...)
	checks = append(checks, keepAwake(ctx, o.Config), permissions(o.Config))
	if !o.Config.TailscaleNode() {
		return checks
	}
	return append(checks, pushReach(ctx, o.DialTLS))
}

func nodeProcess(ctx context.Context, o Options, pid string) Check {
	var me node.Me
	err := node.Unix(o.Config.NodeSocket()).Do(ctx, http.MethodGet, "/api/me", nil, &me)
	stale := err == nil && o.Version != "" && me.Version != o.Version
	if stale {
		return Check{Name: "node process", Mark: Fail, Detail: fmt.Sprintf("pid %s runs factory %s, but this binary is %s, so the node runs old code", pid, me.Version, o.Version), Fix: "factory update  restarts it on this binary"}
	}
	return Check{Name: "node process", Mark: Pass, Detail: "pid " + pid + ", answering on node.sock"}
}

func keepAwake(ctx context.Context, cfg *state.Config) Check {
	if unsupported := node.NewAwake(cfg.KeepsAwake()).Unsupported; unsupported != "" {
		return Check{Name: "keep awake", Mark: Skip, Detail: unsupported}
	}
	var me node.Me
	if err := node.Unix(cfg.NodeSocket()).Do(ctx, http.MethodGet, "/api/me", nil, &me); err != nil || me.Awake == nil {
		return Check{Name: "keep awake", Mark: Skip, Detail: "needs a running node"}
	}
	if me.Awake.Problem {
		return Check{Name: "keep awake", Mark: Warn, Detail: me.Awake.Detail, Fix: "pmset -g ps  and  caffeinate -i true  must both run for the node's user"}
	}
	return Check{Name: "keep awake", Mark: Pass, Detail: me.Awake.Detail}
}

func tailnetChecks(ctx context.Context, cfg *state.Config, ts node.Tailscale) []Check {
	if !cfg.TailscaleNode() {
		return []Check{{Name: "tailscale", Mark: Off, Detail: "not connected: " + node.AddTailscale + " adds the phone and other machines"}}
	}
	self, err := ts.Status(ctx)
	switch {
	case errors.Is(err, node.ErrNoTailscale):
		return append([]Check{{Name: "tailscale", Mark: Fail, Detail: "not installed", Fix: "install Tailscale from https://tailscale.com/download and log in"}}, skipped("owner", "ts.net name", "https cert", "serve", "funnel")...)
	case errors.Is(err, node.ErrLoggedOut):
		return append([]Check{{Name: "tailscale", Mark: Fail, Detail: "installed but not logged in or not running", Fix: "tailscale up"}}, skipped("owner", "ts.net name", "https cert", "serve", "funnel")...)
	case err != nil:
		return append([]Check{{Name: "tailscale", Mark: Fail, Detail: err.Error(), Fix: "tailscale status"}}, skipped("owner", "ts.net name", "https cert", "serve", "funnel")...)
	}
	host := cfg.NodeHostname()
	checks := []Check{{Name: "tailscale", Mark: Pass, Detail: "logged in as " + self.Login + " on " + self.Tailnet}, ownerCheck(cfg, self), machineName(self.DNSName, host), certificate(self, host)}
	sc, err := ts.Serve(ctx)
	return append(checks, mapping(sc, err, host, cfg.NodePort()), funnel(sc, err))
}

func skipped(names ...string) []Check {
	checks := make([]Check, len(names))
	for i, name := range names {
		checks[i] = Check{Name: name, Mark: Skip, Detail: "needs Tailscale first"}
	}
	return checks
}

func ownerCheck(cfg *state.Config, self node.Self) Check {
	owner := cfg.Node.Owner
	switch {
	case self.Tagged:
		return Check{Name: "owner", Mark: Fail, Detail: "this machine is a tagged Tailscale device, so Serve passes no user login and no phone can prove it is you", Lines: []string{"sign in as yourself when the command asks"}, Fix: node.UntagFix}
	case owner == "":
		return Check{Name: "owner", Mark: Fail, Detail: "node.owner is not set, so the node refuses to start", Fix: "factory node install  records " + self.Login}
	case !strings.EqualFold(owner, self.Login):
		return Check{Name: "owner", Mark: Fail, Detail: fmt.Sprintf("node.owner is %s, but Tailscale is logged in as %s, so the node refuses this device", owner, self.Login), Fix: "log Tailscale in as " + owner + ", or set node.owner: " + self.Login + " in config.yml and restart the node"}
	}
	return Check{Name: "owner", Mark: Pass, Detail: owner + ", an untagged user"}
}

func machineName(name, host string) Check {
	fix := "put node.hostname: " + name + " in config.yml, then restart the node"
	switch {
	case host == "":
		return Check{Name: "ts.net name", Mark: Fail, Detail: "node.hostname is not set, so requests through Serve get 421", Fix: fix}
	case name != host:
		return Check{Name: "ts.net name", Mark: Fail, Detail: fmt.Sprintf("node.hostname is %s, but this machine is %s", host, name), Fix: fix}
	}
	return Check{Name: "ts.net name", Mark: Pass, Detail: host}
}

func certificate(self node.Self, host string) Check {
	if !self.HTTPS() {
		return Check{Name: "https cert", Mark: Fail, Detail: "HTTPS is off for this tailnet", Fix: node.HTTPSFix}
	}
	return Check{Name: "https cert", Mark: Pass, Detail: "Tailscale can issue a certificate for " + cmp.Or(host, self.DNSName)}
}

func mapping(sc node.ServeConfig, err error, host string, port int) Check {
	target := node.Target(port)
	fix := "tailscale " + strings.Join(node.ServeArgs(node.HomeHTTPS, port), " ")
	if err != nil {
		return Check{Name: "serve", Mark: Fail, Detail: err.Error(), Fix: fix}
	}
	switch found, uses := sc.Mapping(host, node.HomeHTTPS, target); found {
	case node.Foreign:
		return Check{Name: "serve", Mark: Fail, Detail: fmt.Sprintf("https://%s serves %s, not the node", host, uses), Fix: "move that to another port, then " + fix}
	case node.Unmapped:
		return Check{Name: "serve", Mark: Fail, Detail: fmt.Sprintf("https://%s/ does not forward to %s", host, target), Fix: fix}
	}
	return Check{Name: "serve", Mark: Pass, Detail: fmt.Sprintf("https://%s/ → %s", host, target)}
}

func funnel(sc node.ServeConfig, err error) Check {
	if err != nil {
		return Check{Name: "funnel", Mark: Fail, Detail: "could not tell whether Funnel is on, so the node answers nothing over the network", Lines: []string{err.Error()}, Fix: "tailscale funnel status"}
	}
	funnels := sc.Funnels()
	if len(funnels) == 0 {
		return Check{Name: "funnel", Mark: Pass, Detail: "off on every port; the node stays on the tailnet"}
	}
	c := Check{Name: "funnel", Mark: Fail, Detail: "on, so the node answers nothing over the network until every entry is off"}
	var fixes []string
	for _, f := range funnels {
		c.Lines = append(c.Lines, "public: "+f.Where)
		fixes = append(fixes, f.Fix)
	}
	if note := node.PrivilegeNote(); note != "" {
		c.Lines = append(c.Lines, strings.TrimSpace(note))
	}
	c.Fix = strings.Join(fixes, "  &&  ")
	return c
}

func serviceChecks(ctx context.Context, o Options, svc node.Service) []Check {
	st := svc.State(ctx)
	record, _ := node.LoadInstall(o.Config.StateDir)
	answers := node.Answers(o.Config.NodeSocket())
	switch {
	case !st.Installed && answers:
		return []Check{{Name: "service", Mark: Fail, Detail: "not installed; a node runs in the foreground, and its factories stop with it", Fix: "stop factory node run, then factory node install"}}
	case !st.Installed:
		return []Check{{Name: "service", Mark: Fail, Detail: "not installed, so no node runs factories", Fix: "factory node install"}}
	}
	checks := []Check{{Name: "service", Mark: Pass, Detail: svc.Kind() + " at " + tilde(svc.UnitPath())}}
	if record.Binary != "" && record.Binary != o.Binary {
		checks[0] = Check{Name: "service", Mark: Fail, Detail: svc.Kind() + " runs " + record.Binary + ", not this binary", Fix: "factory node install"}
	}
	switch {
	case !st.Running:
		checks = append(checks, Check{Name: "node process", Mark: Fail, Detail: "the service is not running; its log is " + tilde(svc.Log), Fix: svc.RestartCommand()})
	case !answers:
		checks = append(checks, Check{Name: "node process", Mark: Fail, Detail: "pid " + st.PID + " runs, but node.sock does not answer; its log is " + tilde(svc.Log), Fix: svc.RestartCommand()})
	default:
		checks = append(checks, nodeProcess(ctx, o, st.PID))
	}
	detail, fix, persists := svc.Boot(ctx, false)
	if !persists {
		return append(checks, Check{Name: "at boot", Mark: Warn, Detail: detail, Fix: fix})
	}
	return append(checks, Check{Name: "at boot", Mark: Pass, Detail: detail})
}

func permissions(cfg *state.Config) Check {
	var open []string
	for _, path := range []string{cfg.StateDir, filepath.Join(cfg.StateDir, "logs"), cfg.NodeSocket(), cfg.ReviewSocket(), filepath.Join(cfg.StateDir, "logs", "node.log"), node.InstallPath(cfg.StateDir)} {
		if info, err := os.Stat(path); err == nil && info.Mode().Perm()&0o077 != 0 {
			open = append(open, path)
		}
	}
	if len(open) > 0 {
		return Check{Name: "permissions", Mark: Fail, Detail: "other users can reach the node's private files", Lines: open, Fix: "chmod go-rwx " + strings.Join(open, " ")}
	}
	return Check{Name: "permissions", Mark: Pass, Detail: "the state directory, sockets and logs are yours only"}
}

func pushReach(ctx context.Context, dialTLS func(context.Context, string) error) Check {
	if dialTLS == nil {
		dialTLS = dialPushService
	}
	var unreachable []string
	for _, address := range pushServices {
		if dialTLS(ctx, address) != nil {
			unreachable = append(unreachable, strings.TrimSuffix(address, ":443"))
		}
	}
	if len(unreachable) > 0 {
		return Check{Name: "push reach", Mark: Fail, Detail: "cannot open TLS to " + strings.Join(unreachable, ", "), Fix: "allow outbound HTTPS from this machine to the browser push services"}
	}
	return Check{Name: "push reach", Mark: Pass, Detail: "reachable; this does not prove a phone gets notifications, so send a test push"}
}

func dialPushService(ctx context.Context, address string) error {
	dialer := tls.Dialer{NetDialer: &net.Dialer{Timeout: 5 * time.Second}}
	conn, err := dialer.DialContext(ctx, "tcp", address)
	if err == nil {
		conn.Close()
	}
	return err
}

func Readiness(o Options, origins map[string]string, scope node.Scope) []node.Probe {
	probe := func(name string, run func(context.Context) []Check) node.Probe {
		return node.Probe{Name: name, Run: func(ctx context.Context) node.Check { return combined(name, run(ctx)) }}
	}
	remotes := slices.Sorted(maps.Keys(origins))
	probes := []node.Probe{
		probe("tools", func(context.Context) []Check {
			return []Check{tool("tmux", "3.3", "tmux", []string{"-V"}, installCommand("tmux")), tool("git", "2.31", "git", []string{"--version"}, installCommand("git")), ghCheck(), ghStack(), browserQA(o, false)}
		}),
		probe("claude", func(context.Context) []Check {
			return []Check{tool("claude", "2.1.300", "claude", []string{"--version"}, "curl -fsSL https://claude.ai/install.sh | bash"), claudeLogin(o)}
		}),
		probe("git access", func(ctx context.Context) []Check {
			var checks []Check
			for _, url := range remotes {
				checks = append(checks, gitAccess(ctx, url))
			}
			return checks
		}),
	}
	if scope.Repo == "" {
		probes = append(probes, probe("repos", func(context.Context) []Check { return ownRepos(o.Repos) }))
	}
	if o.Config == nil {
		return probes
	}
	if repos, ok := toolsInUse(o)[state.ToolCodex]; ok {
		probes = append(probes, probe("codex", func(context.Context) []Check { return codexChecks(o, repos) }))
	}
	return probes
}

func ownRepos(repos []string) []Check {
	var checks []Check
	for _, repo := range repos {
		name := filepath.Base(repo)
		if dirExists(repo) {
			checks = append(checks, Check{Mark: Pass, Detail: name + " at " + tilde(repo)})
		} else {
			checks = append(checks, Check{Mark: Warn, Detail: name + " is missing at " + tilde(repo)})
		}
	}
	return checks
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

var markRank = map[Mark]int{Skip: 0, Pass: 1, Warn: 2, Fail: 3}

var nodeMarks = map[Mark]string{Pass: node.MarkOK, Warn: node.MarkWarn, Fail: node.MarkFail, Skip: node.MarkSkip, Off: node.MarkSkip}

func combined(name string, checks []Check) node.Check {
	worst := Skip
	var details, lines []string
	fix := ""
	for _, c := range checks {
		if markRank[c.Mark] > markRank[worst] {
			worst = c.Mark
		}
		label := c.Name
		if label == name {
			label = ""
		}
		detail := strings.TrimSpace(label + " " + c.Detail)
		if c.Mark != Skip && detail != "" {
			details = append(details, detail)
		}
		lines = append(lines, c.Lines...)
		if c.Mark == Fail && fix == "" {
			fix = c.Fix
		}
	}
	if len(details) == 0 {
		details = []string{"nothing to check here yet"}
	}
	return node.Check{Name: name, Mark: nodeMarks[worst], Detail: strings.Join(details, " · "), Fix: fix, Lines: lines}
}
