package state

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestResolveAttentionKeepsWhatItDoesNotKnow(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	original := `{"id":"ENG-1","waiting_on":"user","waiting_reason":"plan gate","future_field":{"kept":true},"attention":[{"id":"q-ENG-1-1","kind":"question","resolved_at":null,"note":"x"}]}`
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 11, 12, 13, 24, 3, 0, time.UTC)
	if err := ResolveAttention(path, "q-ENG-1-1", "agent", at); err != nil {
		t.Fatal(err)
	}
	if err := ResolveAttention(path, "q-ENG-1-9", "", at); err == nil {
		t.Error("resolving an id that is not there succeeded; the supervisor would believe a typo closed the item")
	}
	data, _ := os.ReadFile(path)
	st, err := ParseState(data)
	if err != nil {
		t.Fatal(err)
	}
	kept := strings.Contains(string(data), `"future_field"`) && strings.Contains(string(data), `"note": "x"`)
	if len(st.OpenAttention()) != 0 || st.WaitingOn != "agent" || !kept {
		t.Errorf("state after resolve:\n%s\nwant the item resolved, waiting_on agent and unknown fields kept", data)
	}
}
