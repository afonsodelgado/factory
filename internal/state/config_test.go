package state

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStalledAfterWithoutAUnitIsRefused(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.yml"), []byte("stalled_after: 30\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FACTORY_CONFIG_DIR", dir)
	t.Setenv("FACTORY_STATE_DIR", t.TempDir())
	if cfg, err := LoadConfig(); err == nil {
		t.Errorf("stalled_after: 30 loaded as %v; want it refused, since the user meant 30 of something and silently got the default", cfg.Stalled())
	}
}
