package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"github.com/afonsodelgado/factory/internal/install"
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
	o := install.Options{ClaudeDir: install.ClaudeDir(), Binary: binaryPath(), Home: home, ConfigFile: filepath.Join(state.ConfigDir(), "config.yml")}
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
	header := fmt.Sprintf("factory doctor · %s · %s/%s · checks only, changes nothing", version, runtime.GOOS, runtime.GOARCH)
	problems, warnings := install.Report(os.Stdout, install.Doctor(doctorOptions()), header)
	summary := fmt.Sprintf("%d problems, %d warnings. Nothing was changed.", problems, warnings)
	if problems > 0 {
		summary += " Run the fix commands, then doctor again."
	}
	fmt.Println(summary)
	if problems > 0 {
		return 1
	}
	return 0
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
