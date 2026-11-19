package trust_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/afonsodelgado/factory/internal/catalog"
	"github.com/afonsodelgado/factory/internal/trust"
)

func TestCodexTrustEscapesAPathAndNeverDuplicatesATableCodexWrote(t *testing.T) {
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(home, `we"ird\repo`)
	if err := trust.Codex(home, repo); err != nil {
		t.Fatal(err)
	}
	config, _ := os.ReadFile(filepath.Join(home, "config.toml"))
	if want := `[projects."` + strings.ReplaceAll(strings.ReplaceAll(repo, `\`, `\\`), `"`, `\"`) + `"]`; !strings.Contains(string(config), want) {
		t.Fatalf("config.toml is\n%s\nwant the escaped table %s, or Codex refuses every command with an unclosed table", config, want)
	}
	if !catalog.CodexTrusts(home, repo) {
		t.Error("the escaped table does not count as trusted, so Codex stages would refuse to start")
	}

	written := filepath.Join(home, "codex-wrote")
	literal := `[projects.'` + filepath.Join(home, `say "hi"`) + `']` + "\ntrust_level = \"trusted\"\n"
	if err := os.MkdirAll(written, 0o700); err != nil || os.WriteFile(filepath.Join(written, "config.toml"), []byte(literal), 0o600) != nil {
		t.Fatal(err)
	}
	if err := trust.Codex(written, filepath.Join(home, `say "hi"`)); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(written, "config.toml")); string(got) != literal {
		t.Errorf("a table Codex wrote as a literal key became\n%s\nwant it left as it was, not duplicated", got)
	}
}
