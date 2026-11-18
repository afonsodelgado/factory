package main

import (
	"bufio"
	"cmp"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/x/term"
	"github.com/google/uuid"

	"github.com/afonsodelgado/factory/bundle"
	"github.com/afonsodelgado/factory/internal/install"
	"github.com/afonsodelgado/factory/internal/node"
	"github.com/afonsodelgado/factory/internal/phone"
	"github.com/afonsodelgado/factory/internal/state"
)

func binaryPath() string {
	bin, err := os.Executable()
	if err != nil {
		return "factory"
	}
	if resolved, err := filepath.EvalSymlinks(bin); err == nil {
		return resolved
	}
	return bin
}

func doctorOptions() install.Options {
	home, _ := os.UserHomeDir()
	o := install.Options{ClaudeDir: install.ClaudeDir(), Version: version, Binary: binaryPath(), Home: home, ConfigFile: filepath.Join(state.ConfigDir(), "config.yml")}
	cfg, err := state.LoadConfig()
	if err != nil {
		o.ConfigErr = err
		return o
	}
	o.Repos, o.Config = cfg.Repos, cfg
	for _, repo := range cfg.Repos {
		if profile, err := state.ReadProfile(repo); err == nil && profile.BrowserQA() {
			o.Browser = append(o.Browser, filepath.Base(repo))
		}
	}
	return o
}

func runDoctor(args []string) int {
	if len(args) != 0 {
		fmt.Fprintln(os.Stderr, "usage: factory doctor")
		return 2
	}
	header := fmt.Sprintf("factory doctor · %s · %s/%s · checks only, changes nothing", version, runtime.GOOS, runtime.GOARCH)
	checks := install.Doctor(doctorOptions())
	if cfg, err := state.LoadConfig(); err == nil {
		checks = append(checks, moveChecks(cfg)...)
	}
	if summarize(header, checks) > 0 {
		return 1
	}
	return 0
}

func summarize(header string, checks []install.Check) int {
	problems, warnings := install.Report(os.Stdout, checks, header)
	summary := fmt.Sprintf("%d problems, %d warnings.", problems, warnings)
	if skipped := len(slices.DeleteFunc(slices.Clone(checks), func(c install.Check) bool { return c.Mark != install.Skip })); skipped > 0 {
		summary = fmt.Sprintf("%d problems, %d warnings, %d skipped.", problems, warnings, skipped)
	}
	summary += " Nothing was changed."
	if problems > 0 {
		summary += " Run the fix commands, then doctor again."
	}
	fmt.Println(summary)
	return problems
}

func installPlans(binary string) ([]install.Plan, error) {
	plan, err := install.NewPlan(install.ClaudeDir(), binary)
	if err != nil {
		return nil, err
	}
	plans := []install.Plan{plan}
	if onPath("codex") {
		if plan, err = install.NewCodexPlan(state.CodexHome(), binary); err != nil {
			return nil, err
		}
		plans = append(plans, plan)
	}
	if onPath("opencode") {
		if plan, err = install.NewOpenCodePlan(state.OpenCodeConfigDir(), binary); err != nil {
			return nil, err
		}
		plans = append(plans, plan)
	}
	return plans, nil
}

func onPath(tool string) bool {
	_, err := exec.LookPath(tool)
	return err == nil
}

func runBootstrap(args []string) int {
	o := doctorOptions()
	plans, err := installPlans(o.Binary)
	if err != nil {
		fmt.Fprintln(os.Stderr, "factory bootstrap:", err)
		return 1
	}
	for _, plan := range plans {
		if !applyPlan(plan) {
			return 1
		}
	}
	if o.Config != nil {
		newNodeSetup(o.Config, os.Stdout).reconcile(context.Background())
	}
	printToolFixes(o)
	return 0
}

func applyPlan(plan install.Plan) bool {
	fmt.Printf("factory bootstrap · installs the bundled files for %s into %s\n", plan.Tool, plan.Dir)
	if plan.Changes() == 0 {
		fmt.Print("Everything is installed and current. Nothing changed.\n\n")
		return true
	}
	result, err := install.Apply(plan, time.Now())
	for _, backup := range result.Backups {
		fmt.Println("  backed up  " + backup)
	}
	for _, written := range result.Written {
		fmt.Println("  wrote      " + written)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "factory bootstrap:", err)
		return false
	}
	fmt.Printf("%s written, %d backed up. Restart running %s sessions so they pick up the change.\n\n", count(len(result.Written), "file"), len(result.Backups), plan.Tool)
	return true
}

func count(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

func printToolFixes(o install.Options) {
	var missing []install.Check
	for _, c := range install.Doctor(o) {
		if c.Mark == install.Fail && c.Name != "bundle" && c.Name != "hooks" && c.Name != "repos" && c.Name != "repo config" {
			missing = append(missing, c)
		}
	}
	if len(missing) == 0 {
		return
	}
	fmt.Println("\nbootstrap installs no system packages. Still missing:")
	for _, c := range missing {
		fmt.Printf("  %s %-12s %s\n%17sfix  %s\n", c.Mark, c.Name, c.Detail, "", c.Fix)
	}
}

func runInstallHooks(args []string) int {
	flags := flag.NewFlagSet("install-hooks", flag.ContinueOnError)
	remove := flags.Bool("remove", false, "remove the factory hooks")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	stamp := time.Now().Format("20060102-150405")
	command := install.HookCommand(binaryPath())
	rewrites := []func() (string, string, error){func() (string, string, error) { return install.MergeHooks(install.ClaudeDir(), command, stamp) }}
	if onPath("codex") {
		rewrites = append(rewrites, func() (string, string, error) { return install.MergeCodexHooks(state.CodexHome(), command, stamp) })
	}
	if *remove {
		rewrites = []func() (string, string, error){
			func() (string, string, error) { return install.RemoveHooks(install.ClaudeDir(), stamp) },
			func() (string, string, error) { return install.RemoveCodexHooks(state.CodexHome(), stamp) },
		}
	}
	changed := false
	for _, rewrite := range rewrites {
		written, backup, err := rewrite()
		if err != nil {
			fmt.Fprintln(os.Stderr, "factory install-hooks:", err)
			return 1
		}
		if backup != "" {
			fmt.Println("backed up  " + backup)
		}
		if written != "" {
			fmt.Println("wrote      " + written)
			changed = true
		}
	}
	if !changed {
		fmt.Println("The hooks are already up to date. Nothing changed.")
		return 0
	}
	fmt.Println("Restart running Claude Code and Codex sessions so they pick up the change.")
	return 0
}

const (
	setupUsage = "usage: factory node install [--join | --no-join] | uninstall | status"
	healthWait = 30 * time.Second
)

type joinChoice int

const (
	askToJoin joinChoice = iota
	alwaysJoin
	neverJoin
)

type nodeSetup struct {
	cfg       *state.Config
	tailscale node.Tailscale
	service   node.Service
	app       func() error
	wait      time.Duration
	out       io.Writer
	in        io.Reader
	tty       bool
	join      joinChoice
}

func newNodeSetup(cfg *state.Config, out io.Writer) nodeSetup {
	home, _ := os.UserHomeDir()
	return nodeSetup{
		cfg: cfg, tailscale: node.Tailscale{Run: node.System}, service: node.NewService(cfg, binaryPath(), home, node.System),
		app: func() error { _, err := phone.App(bundle.Files); return err }, wait: healthWait, out: out,
	}
}

func installFlags(args []string) (joinChoice, bool) {
	flags := flag.NewFlagSet("node install", flag.ContinueOnError)
	join := flags.Bool("join", false, "join the factory node found on the tailnet without asking")
	noJoin := flags.Bool("no-join", false, "stay a node of its own without asking")
	if err := flags.Parse(args); err != nil || flags.NArg() > 0 || (*join && *noJoin) {
		return askToJoin, false
	}
	switch {
	case *join:
		return alwaysJoin, true
	case *noJoin:
		return neverJoin, true
	}
	return askToJoin, true
}

func runNodeSetup(args []string) int {
	join, ok := askToJoin, len(args) == 1
	if len(args) > 0 && args[0] == "install" {
		join, ok = installFlags(args[1:])
	}
	if !ok {
		fmt.Fprintln(os.Stderr, setupUsage)
		return 2
	}
	cfg, err := state.LoadConfig()
	if err != nil {
		fmt.Fprintln(os.Stderr, "factory node:", err)
		return 1
	}
	s, ctx := newNodeSetup(cfg, os.Stdout), context.Background()
	s.in, s.tty, s.join = os.Stdin, term.IsTerminal(os.Stdin.Fd()), join
	switch args[0] {
	case "install":
		err = s.install(ctx)
	case "uninstall":
		err = s.uninstall(ctx)
	case "status":
		s.status(ctx)
	default:
		fmt.Fprintln(os.Stderr, setupUsage)
		return 2
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "factory node:", err)
		return 1
	}
	return 0
}

func (s nodeSetup) install(ctx context.Context) error {
	record, err := node.LoadInstall(s.cfg.StateDir)
	if err != nil {
		return err
	}
	if err := s.app(); err != nil {
		return err
	}
	self, mapping, err := s.preflight(ctx)
	if err != nil {
		return err
	}
	fmt.Fprintf(s.out, "factory node install · %s\n\n", self.Short())
	s.passed("tailscale", "logged in as "+self.Login+" on "+self.Tailnet)
	s.passed("https", "certificates on for the tailnet")
	s.passed("funnel", "off")
	if err := s.saveIdentity(self); err != nil {
		return err
	}
	record.Binary = s.service.Binary
	if err := node.SaveInstall(s.cfg.StateDir, record); err != nil {
		return err
	}
	if err := s.startService(ctx); err != nil {
		return err
	}
	target := node.Target(s.cfg.NodePort())
	if mapping == node.Unmapped {
		if _, err := s.tailscale.Run(ctx, "tailscale", node.ServeArgs(node.HomeHTTPS, s.cfg.NodePort())...); err != nil {
			return fmt.Errorf("the node runs, but Tailscale Serve did not take https://%s: %w.%s Then run factory node install again", self.DNSName, err, node.PrivilegeNote())
		}
	}
	s.passed("serve", fmt.Sprintf("https://%s → 127.0.0.1:%d, tailnet only", self.DNSName, s.cfg.NodePort()))
	firstInstall := !record.Complete
	record.Host, record.Serve, record.Complete = self.DNSName, target, true
	if err := node.SaveInstall(s.cfg.StateDir, record); err != nil {
		return err
	}
	local := node.Unix(s.cfg.NodeSocket())
	joined, found, err := s.offerJoin(ctx, local, self.Short())
	if err != nil {
		return err
	}
	if joined {
		return s.ready(ctx, local, self.Short())
	}
	return s.welcome(self, firstInstall, found)
}

func (s nodeSetup) offerJoin(ctx context.Context, c *node.Client, here string) (joined, found bool, err error) {
	var roster node.Roster
	if err := c.Do(ctx, http.MethodGet, "/api/nodes", nil, &roster); err != nil {
		return false, false, err
	}
	if len(roster.Members) > 1 {
		fmt.Fprintf(s.out, "\n%s already shares its node list with %s.\n", here, count(len(roster.Members)-1, "other node"))
		return false, true, nil
	}
	if s.join == neverJoin {
		return false, true, nil
	}
	var answer node.Candidates
	if err := c.Do(ctx, http.MethodGet, "/api/nodes/candidates", nil, &answer); err != nil {
		return false, false, err
	}
	i := slices.IndexFunc(answer.Candidates, func(c node.Candidate) bool { return c.Selectable })
	if i < 0 {
		return false, false, nil
	}
	target := answer.Candidates[i]
	if s.join == askToJoin && !s.tty {
		return false, true, fmt.Errorf("found your factory node on %s, and there is no terminal to ask whether %s joins it. Run factory node install --join or factory node install --no-join", target.Short, here)
	}
	if !s.wantsToJoin(target, here) {
		return false, true, nil
	}
	var result node.AddResult
	err = c.Do(ctx, http.MethodPost, "/api/nodes/join", node.NodeRequest{Operation: uuid.NewString(), Name: target.Name}, &result)
	if err != nil {
		printChecks(s.out, result.Report.Checks)
		return false, true, fmt.Errorf("%s did not join %s: %w. factory node install tries again", here, target.Short, err)
	}
	fmt.Fprintln(s.out)
	s.passed("joined", fmt.Sprintf("%s lists %s, and %s lists %s", target.Short, here, here, target.Short))
	printPush(s.out, result.Push, target.Short, here)
	s.passed("phone app", "trusts "+result.Roster.Home+", where your phone app lives")
	return true, true, nil
}

func printPush(out io.Writer, push node.PushOutcome, from, to string) {
	switch {
	case push.Shared:
		install.WriteCheck(out, install.Check{Name: "push key", Mark: install.Pass, Detail: "copied from " + from + " over the tailnet; never printed or written", Lines: []string{"anywhere but " + to + "'s state directory"}})
	case push.Conflict != "":
		install.WriteCheck(out, install.Check{Name: "push key", Mark: install.Warn, Detail: push.Conflict})
	}
}

func (s nodeSetup) wantsToJoin(target node.Candidate, here string) bool {
	factories := 0
	if target.Hello != nil {
		factories = target.Hello.Factories
	}
	question := fmt.Sprintf("\nFound your factory node on %s, with %s. Join it? The hub on %s\nand your phone then list %s, and %s notifies your phone with the same\npush key. [Y/n] ", target.Short, factoryCount(factories), target.Short, here, here)
	switch s.join {
	case alwaysJoin:
		fmt.Fprint(s.out, question+"y (--join)\n")
		return true
	case askToJoin:
		fmt.Fprint(s.out, question)
		line, _ := bufio.NewReader(s.in).ReadString('\n')
		answer := strings.ToLower(strings.TrimSpace(line))
		return answer == "" || answer == "y" || answer == "yes"
	}
	return false
}

func factoryCount(n int) string {
	if n == 1 {
		return "1 factory"
	}
	return fmt.Sprintf("%d factories", n)
}

func (s nodeSetup) ready(ctx context.Context, c *node.Client, here string) error {
	var report node.Report
	if err := c.Do(ctx, http.MethodGet, "/api/readiness", nil, &report); err != nil {
		return err
	}
	fmt.Fprint(s.out, "\nReady for factories?\n\n")
	printChecks(s.out, report.Checks)
	if report.Problems > 0 {
		fmt.Fprintf(s.out, "\n%s. %s is a node now, but factories won't start here until it passes.\nfactory doctor checks again.\n", count(report.Problems, "problem"), here)
	}
	return nil
}

func (s nodeSetup) passed(name, detail string) {
	install.WriteCheck(s.out, install.Check{Name: name, Mark: install.Pass, Detail: detail})
}

func (s nodeSetup) preflight(ctx context.Context) (node.Self, node.Mapping, error) {
	self, err := s.tailscale.Status(ctx)
	if err != nil {
		return self, 0, err
	}
	saved := s.cfg.Node.Owner
	switch {
	case self.Tagged:
		return self, 0, errors.New("this machine is a tagged Tailscale device, so Serve passes no user login and your phone could never prove it is you. Run " + node.UntagFix + ", sign in as yourself, then factory node install again")
	case saved != "" && !strings.EqualFold(saved, self.Login):
		return self, 0, fmt.Errorf("node.owner in %s is %s, but Tailscale is logged in as %s. Log Tailscale in as %s, or change node.owner, then run factory node install again", s.cfg.ConfigFile(), saved, self.Login, saved)
	case !self.HTTPS():
		return self, 0, errors.New("HTTPS certificates are off for this tailnet, so your phone cannot open the node; " + node.HTTPSFix + ", then run factory node install again")
	}
	sc, err := s.tailscale.Serve(ctx)
	if err != nil {
		return self, 0, fmt.Errorf("%w; nothing is installed while the node cannot tell whether Funnel is off", err)
	}
	if funnels := sc.Funnels(); len(funnels) > 0 {
		return self, 0, funnelError(funnels)
	}
	mapping, uses := sc.Mapping(self.DNSName, node.HomeHTTPS, node.Target(s.cfg.NodePort()))
	if mapping == node.Foreign {
		return self, 0, fmt.Errorf("https://%s already serves something else (%s), and the node never takes it over. Move that to another port with tailscale serve, then run factory node install again", self.DNSName, uses)
	}
	return self, mapping, nil
}

func funnelError(funnels []node.Funnel) error {
	var b strings.Builder
	b.WriteString("Funnel puts this machine on the public internet, and the node never runs beside it. Turn off each entry, then run factory node install again:")
	for _, f := range funnels {
		fmt.Fprintf(&b, "\n  %s\n    %s", f.Where, f.Fix)
	}
	if note := node.PrivilegeNote(); note != "" {
		b.WriteString("\n" + strings.TrimSpace(note))
	}
	return errors.New(b.String())
}

func (s nodeSetup) saveIdentity(self node.Self) error {
	err := state.UpdateMachine(s.cfg.ConfigDir, func(m *state.Machine) error {
		m.Node.Owner = cmp.Or(m.Node.Owner, self.Login)
		m.Node.Hostname = self.DNSName
		return nil
	})
	s.cfg.Node.Owner, s.cfg.Node.Hostname = cmp.Or(s.cfg.Node.Owner, self.Login), self.DNSName
	return err
}

func (s nodeSetup) startService(ctx context.Context) error {
	if _, alive := runningMe(s.cfg); alive && !s.service.State(ctx).Running {
		return errors.New("a factory node already runs in the foreground (factory node run). Stop it, then run factory node install again so the service takes over")
	}
	detail, fix, persists := s.service.Boot(ctx, true)
	me, err := s.restartService(ctx)
	if err != nil {
		return err
	}
	s.passed("service", s.service.Loaded(persists))
	if !persists {
		install.WriteCheck(s.out, install.Check{Name: "at boot", Mark: install.Warn, Detail: detail, Fix: fix})
	}
	s.passed("node", fmt.Sprintf("%s, answering on 127.0.0.1:%d and its local socket", me.Version, s.cfg.NodePort()))
	return nil
}

func (s nodeSetup) restartService(ctx context.Context) (node.Me, error) {
	if err := s.service.Write(); err != nil {
		return node.Me{}, err
	}
	if err := s.service.Load(ctx); err != nil {
		return node.Me{}, fmt.Errorf("could not load the %s: %w", s.service.Kind(), err)
	}
	me, answered := s.awaitNode()
	if !answered {
		s.service.Unload(ctx)
		return me, fmt.Errorf("the %s started, but the node did not answer within %s, so it is stopped again. Its log is %s; fix what it says, then run factory node install again", s.service.Kind(), s.wait, s.service.Log)
	}
	return me, nil
}

func (s nodeSetup) reconcile(ctx context.Context) {
	record, err := node.LoadInstall(s.cfg.StateDir)
	if err != nil || !record.Complete || record.Binary != s.service.Binary {
		return
	}
	before, alive := runningMe(s.cfg)
	if alive && before.Version == version {
		return
	}
	me, err := s.restartService(ctx)
	if err != nil {
		install.WriteCheck(s.out, install.Check{Name: "node", Mark: install.Fail, Detail: err.Error(), Fix: "factory node install"})
		return
	}
	was := cmp.Or(before.Version, "not running")
	s.passed("node", fmt.Sprintf("restarted on %s (was %s)", me.Version, was))
}

func (s nodeSetup) awaitNode() (node.Me, bool) {
	deadline := time.Now().Add(s.wait)
	for {
		me, alive := runningMe(s.cfg)
		if alive && listening(s.cfg.NodePort()) {
			return me, true
		}
		if time.Now().After(deadline) {
			return me, false
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func runningMe(cfg *state.Config) (node.Me, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var me node.Me
	err := node.Unix(cfg.NodeSocket()).Do(ctx, http.MethodGet, "/api/me", nil, &me)
	return me, err == nil
}

func listening(port int) bool {
	conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), time.Second)
	if err == nil {
		conn.Close()
	}
	return err == nil
}

func (s nodeSetup) welcome(self node.Self, firstInstall, foundNodes bool) error {
	url := "https://" + self.DNSName
	fmt.Fprintf(s.out, "\n%s is a factory node. It keeps running with the hub closed.\n\n", self.Short())
	if !foundNodes {
		fmt.Fprintf(s.out, "No other factory nodes on your tailnet yet. To add another machine, run this there:\n  %s\nIt sets that machine up and joins %s.\n\n", installLine, self.Short())
	}
	fmt.Fprintf(s.out, "Next\n  hub      factory\n  phone    open %s with Tailscale on, then add it to\n           the Home Screen. Tailscale already says it's you: no code, no pairing.\n", url)
	if !firstInstall {
		return nil
	}
	code, err := phone.QR(url)
	if err != nil {
		return err
	}
	fmt.Fprintln(s.out)
	for _, line := range code {
		fmt.Fprintln(s.out, "  "+line)
	}
	fmt.Fprintln(s.out, "\n  That is the same URL as a QR, not a code. It opens only for you, on your tailnet.")
	return nil
}

func (s nodeSetup) uninstall(ctx context.Context) error {
	record, err := node.LoadInstall(s.cfg.StateDir)
	if err != nil {
		return err
	}
	if err := s.service.Uninstall(ctx); err != nil {
		return fmt.Errorf("could not unload the %s: %w", s.service.Kind(), err)
	}
	fmt.Fprintf(s.out, "removed  the %s\n", s.service.Kind())
	record.Binary, record.Complete = "", false
	if err := node.SaveInstall(s.cfg.StateDir, record); err != nil {
		return err
	}
	if record.Serve != "" {
		if err := s.unserve(ctx, record); err != nil {
			return err
		}
	}
	if err := node.SaveInstall(s.cfg.StateDir, node.Install{}); err != nil {
		return err
	}
	fmt.Fprintln(s.out, "Factories, their panes and phone notifications stay. factory node install brings the node back.")
	return nil
}

func (s nodeSetup) unserve(ctx context.Context, record node.Install) error {
	sc, err := s.tailscale.Serve(ctx)
	if err != nil {
		return fmt.Errorf("%w. Run factory node uninstall again to remove https://%s → %s", err, record.Host, record.Serve)
	}
	switch mapping, uses := sc.Mapping(record.Host, node.HomeHTTPS, record.Serve); mapping {
	case node.Ours:
		if _, err := s.tailscale.Run(ctx, "tailscale", node.UnserveArgs(node.HomeHTTPS)...); err != nil {
			return fmt.Errorf("could not remove https://%s → %s: %w.%s Then run factory node uninstall again", record.Host, record.Serve, err, node.PrivilegeNote())
		}
		fmt.Fprintf(s.out, "removed  https://%s → %s\n", record.Host, record.Serve)
	case node.Foreign:
		fmt.Fprintf(s.out, "kept     https://%s: it now serves %s, not the node\n", record.Host, uses)
	}
	return nil
}

func (s nodeSetup) status(ctx context.Context) {
	st := s.service.State(ctx)
	record, _ := node.LoadInstall(s.cfg.StateDir)
	service := "not installed; factory node install installs it"
	switch {
	case st.Running:
		service = s.service.Kind() + ", running as pid " + st.PID
	case st.Installed:
		service = s.service.Kind() + ", installed but not running; " + s.service.RestartCommand() + " starts it"
	}
	running, awake := "not answering", "unknown while the node is not answering"
	if me, alive := runningMe(s.cfg); alive {
		running = me.Version
		if me.Awake != nil {
			awake = me.Awake.Detail
		}
	}
	tailnet, url := "unknown until factory node install or factory node run learns it", "unknown"
	if host := s.cfg.NodeHostname(); host != "" {
		_, tailnet, _ = strings.Cut(host, ".")
		url = "https://" + host
	}
	fmt.Fprintf(s.out, "service     %s\nrunning     %s\nconfigured  %s\nport        127.0.0.1:%d\ntailnet     %s\nurl         %s\nkeep awake  %s\n",
		service, running, s.configured(ctx, record), s.cfg.NodePort(), tailnet, url, awake)
}

func (s nodeSetup) configured(ctx context.Context, record node.Install) string {
	if record.Binary == "" {
		return "not installed"
	}
	out, err := s.service.Run(ctx, record.Binary, "--version")
	fields := strings.Fields(string(out))
	if err != nil || len(fields) < 2 {
		return record.Binary + " does not run; factory node install points the service at this binary"
	}
	return fields[1] + " at " + record.Binary
}
