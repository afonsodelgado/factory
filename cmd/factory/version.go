package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"time"
)

const releaseRepo = "afonsodelgado/factory"

type semver [3]int

func parseVersion(s string) (semver, bool) {
	var v semver
	rest, hasPrefix := strings.CutPrefix(strings.TrimSpace(s), "v")
	parts := strings.Split(rest, ".")
	if !hasPrefix || len(parts) != 3 {
		return v, false
	}
	for i, part := range parts {
		n, err := strconv.Atoi(part)
		if err != nil || n < 0 {
			return v, false
		}
		v[i] = n
	}
	return v, true
}

func isNewer(latest, current string) bool {
	l, ok := parseVersion(latest)
	if !ok {
		return false
	}
	c, ok := parseVersion(current)
	if !ok {
		return true
	}
	for i := range l {
		if l[i] != c[i] {
			return l[i] > c[i]
		}
	}
	return false
}

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

func ghRelease(timeout time.Duration, args ...string) ([]byte, error) {
	if _, err := exec.LookPath("gh"); err != nil {
		return nil, errors.New("gh is not installed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "gh", append([]string{"release"}, args...)...)
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return nil, fmt.Errorf("gh timed out after %s", timeout)
	}
	if err != nil {
		if line := firstLine(stderr.String()); line != "" {
			return nil, errors.New("gh: " + line)
		}
		return nil, fmt.Errorf("gh: %w", err)
	}
	return out, nil
}

func firstLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			return line
		}
	}
	return ""
}

func latestRelease() (string, error) {
	out, err := ghRelease(5*time.Second, "view", "--repo", releaseRepo, "--json", "tagName")
	if err != nil {
		return "", err
	}
	var release struct {
		TagName string `json:"tagName"`
	}
	if err := json.Unmarshal(out, &release); err != nil {
		return "", fmt.Errorf("unexpected gh output: %w", err)
	}
	if _, ok := parseVersion(release.TagName); !ok {
		return "", fmt.Errorf("the latest release has an unexpected tag %q", release.TagName)
	}
	return release.TagName, nil
}

func runVersion() int {
	fmt.Println(versionLine())
	latest, err := latestRelease()
	if err != nil {
		fmt.Printf("latest release unknown: %s\n", err)
		return 0
	}
	_, isRelease := parseVersion(version)
	switch {
	case !isRelease:
		fmt.Printf("latest  %s · run factory update to install it\n", latest)
	case isNewer(latest, version):
		fmt.Printf("latest  %s · run factory update\n", latest)
	default:
		fmt.Printf("latest  %s · up to date\n", latest)
	}
	return 0
}

func runUpdate(args []string) int {
	latest, err := latestRelease()
	if err != nil {
		fmt.Fprintln(os.Stderr, "factory update: cannot find the latest release:", err)
		return 1
	}
	_, isRelease := parseVersion(version)
	if isRelease && !isNewer(latest, version) {
		fmt.Printf("factory %s is the latest release. Nothing changed.\n", version)
		return 0
	}
	target := binaryPath()
	if err := replaceBinary(target, latest); err != nil {
		fmt.Fprintln(os.Stderr, "factory update:", err)
		return 1
	}
	fmt.Printf("Installed factory %s at %s.\n\n", latest, target)
	bootstrap := exec.Command(target, "bootstrap")
	bootstrap.Stdout, bootstrap.Stderr = os.Stdout, os.Stderr
	if err := bootstrap.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "factory update: the new binary is installed, but bootstrap failed: %v\nRun factory bootstrap again.\n", err)
		return 1
	}
	fmt.Printf("\nQuit the app with q and run factory again to use %s.\n", latest)
	return 0
}

func replaceBinary(target, tag string) error {
	dir := filepath.Dir(target)
	tmp, err := os.CreateTemp(dir, ".factory-update-*")
	if err != nil {
		return fmt.Errorf("cannot write to %s, where factory is installed: %w", dir, err)
	}
	tmpPath := tmp.Name()
	tmp.Close()
	installed := false
	defer func() {
		if !installed {
			os.Remove(tmpPath)
		}
	}()
	asset := fmt.Sprintf("factory_%s_%s", runtime.GOOS, runtime.GOARCH)
	if _, err := ghRelease(2*time.Minute, "download", tag, "--repo", releaseRepo, "--pattern", asset, "--output", tmpPath, "--clobber"); err != nil {
		return fmt.Errorf("cannot download %s of %s: %w", asset, tag, err)
	}
	if err := os.Chmod(tmpPath, 0o755); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, target); err != nil {
		return fmt.Errorf("cannot replace %s: %w", target, err)
	}
	installed = true
	return nil
}
