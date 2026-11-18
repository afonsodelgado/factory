package trust

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTrustLeavesAFileThatKeepsChangingAsItIs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	writes := 0
	err := update(path, func([]byte) ([]byte, error) {
		writes++
		os.WriteFile(path, []byte("written by Codex meanwhile "+string(rune('0'+writes))), 0o600)
		return []byte("trusted by factory"), nil
	})
	if got, _ := os.ReadFile(path); err == nil || string(got) == "trusted by factory" {
		t.Errorf("update over a file that changed twice returned %v and left %q; want it to give up and keep the other writer's content", err, got)
	}
}
