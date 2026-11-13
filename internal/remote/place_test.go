package remote

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/afonsodelgado/factory/internal/state"
)

func offlineConfig(t *testing.T) *state.Config {
	t.Helper()
	bin, root := t.TempDir(), t.TempDir()
	standIn, err := filepath.Abs("testdata/ssh")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(standIn, filepath.Join(bin, "ssh")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FACTORY_SSH_STANDIN_ROOT", root)
	t.Setenv("FACTORY_SSH_STANDIN_BIN", filepath.Join(bin, "missing"))
	t.Setenv("FACTORY_SSH_STANDIN_DOWN", "1")
	short, err := os.MkdirTemp("/tmp", "frp-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(short) })
	cfg := &state.Config{StateDir: short}
	cfg.Remotes = map[string]state.Remote{"homelab": {Host: "homelab"}, "build": {Host: "build"}}
	return cfg
}

func known(t *testing.T, cfg *state.Config, name, id string) {
	t.Helper()
	if err := UpdateCache(cfg.StateDir, name, func(c *Cache) error {
		c.Inventory = append(c.Inventory, Listing{ID: id, Incarnation: "inc-" + id})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestOfflineCreationChecksKnownIDsWithoutBlocking(t *testing.T) {
	t.Run("a remote that is down and was never seen does not hold up a local id", func(t *testing.T) {
		cfg := offlineConfig(t)
		start := time.Now()
		where, err := Place(cfg, "csv", "", false)
		if err == nil {
			err = RefuseKnownID(cfg, "csv")
		}
		if err != nil || where != "" || time.Since(start) > RefreshWait+time.Second {
			t.Fatalf("local creation got %q, %v after %s; want it here at once", where, err, time.Since(start))
		}
	})
	t.Run("an id a remote reported earlier cannot be created again here", func(t *testing.T) {
		cfg := offlineConfig(t)
		known(t, cfg, "homelab", "csv")
		if err := RefuseKnownID(cfg, "csv"); err == nil {
			t.Fatal("a cached duplicate was allowed to start on this machine")
		}
		if _, err := Place(cfg, "csv", "", true); err == nil {
			t.Fatal("--local created a second csv while homelab has one")
		}
	})
	t.Run("two creations racing for one id reserve it once", func(t *testing.T) {
		cfg := offlineConfig(t)
		var wg sync.WaitGroup
		errs := make(chan error, 2)
		for _, name := range []string{"homelab", "build"} {
			wg.Go(func() {
				_, err := reserve(cfg, name, Create{ID: "api", GitURL: "https://git.example/acme/app.git"})
				errs <- err
			})
		}
		wg.Wait()
		close(errs)
		reserved := 0
		for err := range errs {
			if err == nil {
				reserved++
			}
		}
		if reserved != 1 {
			t.Fatalf("%d creations reserved api; want exactly one", reserved)
		}
	})
	t.Run("a collision found after reconnecting needs an explicit place", func(t *testing.T) {
		cfg := offlineConfig(t)
		registry, err := state.LoadRegistry(cfg.RegistryPath())
		if err != nil {
			t.Fatal(err)
		}
		registry.Upsert(state.Entry{Repo: "/r/app", Slug: "dup"})
		if err := registry.Save(); err != nil {
			t.Fatal(err)
		}
		known(t, cfg, "homelab", "dup")
		if _, err := Place(cfg, "dup", "", false); err == nil || !strings.Contains(err.Error(), "--local") {
			t.Fatalf("an unqualified dup went somewhere (%v); want a refusal naming --local and --remote", err)
		}
		here, errHere := Place(cfg, "dup", "", true)
		there, errThere := Place(cfg, "dup", "homelab", false)
		if errHere != nil || errThere != nil || here != "" || there != "homelab" {
			t.Fatalf("explicit places gave %q %v and %q %v", here, errHere, there, errThere)
		}
	})
}
