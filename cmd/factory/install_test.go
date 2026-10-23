package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/afonsodelgado/factory/internal/install"
)

func TestDoctorReportsAConfigThatStopsTheHub(t *testing.T) {
	config := t.TempDir()
	if err := os.WriteFile(filepath.Join(config, "config.yml"), []byte("stalled_after: 30\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FACTORY_CONFIG_DIR", config)
	t.Setenv("FACTORY_STATE_DIR", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PATH", "")

	var out bytes.Buffer
	install.Report(&out, install.Doctor(doctorOptions()), "doctor")
	if !strings.Contains(out.String(), `stalled_after "30"`) {
		t.Errorf("doctor said nothing about the config that stops hub and open:\n%s", out.String())
	}
}
