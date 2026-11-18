package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"strings"
	"syscall"
	"time"

	"github.com/afonsodelgado/factory/internal/install"
	"github.com/afonsodelgado/factory/internal/node"
	"github.com/afonsodelgado/factory/internal/release"
	"github.com/afonsodelgado/factory/internal/state"
)

var releases = release.Default()

func versionLine() string {
	var details []string
	if info, ok := debug.ReadBuildInfo(); ok {
		settings := map[string]string{}
		for _, s := range info.Settings {
			settings[s.Key] = s.Value
		}
		if rev := settings["vcs.revision"]; rev != "" {
			details = append(details, rev[:min(7, len(rev))])
		}
		if t, err := time.Parse(time.RFC3339, settings["vcs.time"]); err == nil {
			details = append(details, t.Format("2006-01-02"))
		}
		if settings["vcs.modified"] == "true" {
			details = append(details, "modified")
		}
	}
	if len(details) == 0 {
		return "factory " + version
	}
	return fmt.Sprintf("factory %s (%s)", version, strings.Join(details, ", "))
}

func runVersion() int {
	fmt.Println(versionLine())
	latest, err := releases.Latest(context.Background())
	if err != nil {
		fmt.Printf("latest release unknown: %s\n", err)
		return 0
	}
	switch {
	case !release.Valid(version):
		fmt.Printf("latest  %s · run factory update to install it\n", latest)
	case release.Newer(latest, version):
		fmt.Printf("latest  %s · run factory update\n", latest)
	default:
		fmt.Printf("latest  %s · up to date\n", latest)
	}
	return 0
}

const updateUsage = "usage: factory update [--version <tag>] [--this-node]"

func runUpdate(args []string) int {
	flags := flag.NewFlagSet("update", flag.ContinueOnError)
	wanted := flags.String("version", "", "install this release instead of the latest")
	thisNode := flags.Bool("this-node", false, "update only this machine, not the other nodes in the list")
	if err := flags.Parse(args); err != nil || flags.NArg() > 0 {
		fmt.Fprintln(os.Stderr, updateUsage)
		return 2
	}
	ctx := context.Background()
	tag := *wanted
	if tag == "" {
		latest, err := releases.Latest(ctx)
		if err != nil {
			fmt.Fprintln(os.Stderr, "factory update: cannot find the latest release:", err)
			return 1
		}
		tag = latest
	}
	if !release.Valid(tag) {
		fmt.Fprintf(os.Stderr, "factory update: %q is not a release tag, such as v0.1.42\n", tag)
		return 2
	}
	if code := updateThisMachine(ctx, tag); code != 0 || *thisNode {
		return code
	}
	cfg, err := state.LoadConfig()
	if err != nil {
		fmt.Fprintln(os.Stderr, "factory update:", err)
		return 1
	}
	return updateOtherNodes(ctx, cfg, tag, os.Stdout)
}

func updateThisMachine(ctx context.Context, tag string) int {
	if release.Valid(version) && !release.Newer(tag, version) {
		fmt.Printf("factory %s is installed, the release you asked for or newer.\n", version)
		if cfg, err := state.LoadConfig(); err == nil {
			newNodeSetup(cfg, os.Stdout).reconcile(ctx)
		}
		return 0
	}
	target := binaryPath()
	if err := releases.Install(ctx, tag, target); err != nil {
		fmt.Fprintln(os.Stderr, "factory update:", err)
		return 1
	}
	fmt.Printf("Installed factory %s at %s.\n\n", tag, target)
	bootstrap := exec.Command(target, "bootstrap")
	bootstrap.Stdout, bootstrap.Stderr = os.Stdout, os.Stderr
	if err := bootstrap.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "factory update: the new binary is installed, but bootstrap failed: %v\nRun factory bootstrap again.\n", err)
		return 1
	}
	fmt.Printf("\nQuit the app with q and run factory again to use %s.\n", tag)
	return 0
}

func selfUpdate(cfg *state.Config) func(string) error {
	return func(tag string) error {
		logs := filepath.Join(cfg.StateDir, "logs")
		if err := os.MkdirAll(logs, 0o700); err != nil {
			return err
		}
		log, err := os.OpenFile(filepath.Join(logs, "update.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		defer log.Close()
		cmd := exec.Command(binaryPath(), "update", "--version", tag, "--this-node")
		cmd.Stdout, cmd.Stderr = log, log
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		if err := cmd.Start(); err != nil {
			return err
		}
		go cmd.Wait()
		return nil
	}
}

func updateOtherNodes(ctx context.Context, cfg *state.Config, tag string, out io.Writer) int {
	if _, alive := runningMe(cfg); !alive {
		return 0
	}
	fmt.Fprintf(out, "\nAsking the other nodes in your list to update to %s…\n", tag)
	ctx, cancel := context.WithTimeout(ctx, node.UpdateWait+30*time.Second)
	defer cancel()
	var updates node.Updates
	if err := node.Unix(cfg.NodeSocket()).Do(ctx, http.MethodPost, "/api/nodes/update", node.UpdateAll{Version: tag}, &updates); err != nil {
		fmt.Fprintln(os.Stderr, "factory update: could not reach the other nodes:", err)
		return 1
	}
	if len(updates.Nodes) == 0 {
		fmt.Fprintln(out, "No other nodes in the list.")
		return 0
	}
	code := 0
	for _, u := range updates.Nodes {
		c := install.Check{Name: u.Node, Mark: install.Pass}
		switch u.Outcome {
		case node.Updated:
			c.Detail = fmt.Sprintf("%s → %s, restarted", u.From, u.To)
		case node.Current:
			c.Detail = "already on " + u.From
		default:
			c.Mark, c.Detail, code = install.Fail, u.Detail, 1
		}
		install.WriteCheck(out, c)
	}
	return code
}
