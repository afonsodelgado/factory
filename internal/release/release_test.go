package release

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestNewerComparesReleasesNumerically(t *testing.T) {
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
		if got := Newer(c.latest, c.current); got != c.want {
			t.Errorf("Newer(%q, %q) = %v, want %v: %s", c.latest, c.current, got, c.want, c.regression)
		}
	}
}

func TestInstallReplacesTheBinaryOnlyWhenItsChecksumMatches(t *testing.T) {
	binary := []byte("new factory")
	sum := sha256.Sum256(binary)
	sums := map[string]string{
		"v9.9.9": hex.EncodeToString(sum[:]) + "  " + Asset() + "\n",
		"v9.9.8": "0000000000000000000000000000000000000000000000000000000000000000  " + Asset() + "\n",
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tag, asset := filepath.Base(filepath.Dir(r.URL.Path)), filepath.Base(r.URL.Path)
		switch asset {
		case Checksums:
			w.Write([]byte(sums[tag]))
		case Asset():
			w.Write(binary)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	g := GitHub{Base: server.URL, HTTP: server.Client()}
	target := filepath.Join(t.TempDir(), "factory")
	os.WriteFile(target, []byte("old factory"), 0o755)

	if err := g.Install(context.Background(), "v9.9.8", target); err == nil {
		t.Error("a binary whose checksum does not match was accepted")
	}
	if got, _ := os.ReadFile(target); string(got) != "old factory" {
		t.Errorf("a failed install changed the binary to %q", got)
	}
	if err := g.Install(context.Background(), "v9.9.9", target); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(target); string(got) != "new factory" {
		t.Errorf("the binary is %q after a verified install", got)
	}
}
