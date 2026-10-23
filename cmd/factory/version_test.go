package main

import (
	"io"
	"os"
	"strings"
	"testing"
)

func TestVersionFlagPrintsOnlyTheLocalVersion(t *testing.T) {
	t.Setenv("PATH", "")
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout := os.Stdout
	os.Stdout = write
	code := run([]string{"--version"})
	os.Stdout = stdout
	write.Close()
	out, _ := io.ReadAll(read)
	if lines := strings.Split(strings.TrimSpace(string(out)), "\n"); code != 0 || len(lines) != 1 || !strings.HasPrefix(lines[0], "factory ") {
		t.Errorf("factory --version exited %d and printed %q; want only the local version line, with no gh call that can hang for 5 s", code, out)
	}
}

func TestIsNewerComparesReleasesNumerically(t *testing.T) {
	cases := []struct {
		latest, current string
		want            bool
		regression      string
	}{
		{"v0.1.10", "v0.1.9", true, "a string compare offers no update past patch 9"},
		{"v0.1.9", "v0.1.10", false, "a string compare offers a downgrade"},
		{"v0.2.0", "v0.1.99", true, "a minor bump is not seen as newer"},
		{"v0.1.0", "v0.1.0", false, "an up-to-date binary is told to update"},
		{"v0.1.0", "dev", true, "a dev build is never offered the release"},
		{"garbage", "v0.1.0", false, "a malformed tag is installed over a release"},
		{"v1.2", "dev", false, "a tag with two parts panics or counts as a release"},
		{"v1.x.3", "v1.0.0", false, "a non-numeric part counts as a release"},
	}
	for _, c := range cases {
		if got := isNewer(c.latest, c.current); got != c.want {
			t.Errorf("isNewer(%q, %q) = %v, want %v: %s", c.latest, c.current, got, c.want, c.regression)
		}
	}
}
