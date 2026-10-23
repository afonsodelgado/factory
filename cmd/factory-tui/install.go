package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/afonsodelgado/factory/internal/install"
	"github.com/afonsodelgado/factory/internal/state"
)

func binaryPath() string {
	bin, err := os.Executable()
	if err != nil {
		return "factory-tui"
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
	for _, repo := range cfg.Repos {
		if profile, err := state.ReadProfile(repo); err == nil && profile.BrowserQA() {
			o.Browser = append(o.Browser, filepath.Base(repo))
		}
	}
	return o
}

func runDoctor(args []string) int {
	header := fmt.Sprintf("factory-tui doctor · %s · %s/%s · checks only, changes nothing", version, runtime.GOOS, runtime.GOARCH)
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

func runBootstrap(args []string) int {
	o := doctorOptions()
	plan, err := install.NewPlan(o.ClaudeDir, o.Binary)
	if err != nil {
		fmt.Fprintln(os.Stderr, "factory-tui bootstrap:", err)
		return 1
	}
	fmt.Printf("factory-tui bootstrap · installs the bundled skills, agents and hooks into %s\n\n", o.ClaudeDir)
	if plan.Changes() == 0 {
		fmt.Println("Everything is installed and current. Nothing changed.")
	} else {
		result, err := install.Apply(plan, time.Now())
		for _, backup := range result.Backups {
			fmt.Println("  backed up  " + backup)
		}
		for _, written := range result.Written {
			fmt.Println("  wrote      " + written)
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "factory-tui bootstrap:", err)
			return 1
		}
		fmt.Printf("\n%s written, %d backed up. Restart running Claude Code sessions so they pick up the hooks.\n", count(len(result.Written), "file"), len(result.Backups))
	}
	printToolFixes(o)
	return 0
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
		if c.Mark == install.Fail && c.Name != "bundle" && c.Name != "hooks" {
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
	remove := flags.Bool("remove", false, "remove the factory-tui hooks")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	dir := install.ClaudeDir()
	stamp := time.Now().Format("20060102-150405")
	var written, backup string
	var err error
	if *remove {
		written, backup, err = install.RemoveHooks(dir, stamp)
	} else {
		written, backup, err = install.MergeHooks(dir, install.HookCommand(binaryPath()), stamp)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "factory-tui install-hooks:", err)
		return 1
	}
	if written == "" {
		fmt.Println("settings.json already up to date. Nothing changed.")
		return 0
	}
	if backup != "" {
		fmt.Println("backed up  " + backup)
	}
	fmt.Println("wrote      " + written)
	fmt.Println("Restart running Claude Code sessions so they pick up the change.")
	return 0
}
