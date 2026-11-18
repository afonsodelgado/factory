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
