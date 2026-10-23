package catalog

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestCodexOffersOnlyListedModelsOfAChatGPTLogin(t *testing.T) {
	home := t.TempDir()
	cache := `{"models":[
		{"slug":"gpt-5.6-sol","visibility":"list","supported_reasoning_levels":[{"effort":"low"},{"effort":"ultra"}]},
		{"slug":"gpt-reserve","visibility":"hide","supported_reasoning_levels":[{"effort":"low"}]}]}`
	os.WriteFile(filepath.Join(home, "models_cache.json"), []byte(cache), 0o644)

	os.WriteFile(filepath.Join(home, "auth.json"), []byte(`{"auth_mode":"apikey","OPENAI_API_KEY":"sk-x"}`), 0o600)
	if _, err := Codex(home); !errors.Is(err, ErrCodexNotChatGPT) {
		t.Errorf("an API-key login gave %v; want ErrCodexNotChatGPT, so no factory bills an API key", err)
	}

	os.WriteFile(filepath.Join(home, "auth.json"), []byte(`{"auth_mode":"chatgpt"}`), 0o600)
	models, err := Codex(home)
	if err != nil {
		t.Fatal(err)
	}
	want := []Model{{ID: "gpt-5.6-sol", Efforts: []string{"low", "ultra"}}}
	if !reflect.DeepEqual(models, want) {
		t.Errorf("Codex offers %+v; want %+v, without the hidden model", models, want)
	}
}

func TestOpenCodeOffersOnlyFreeModels(t *testing.T) {
	out := `opencode/big-pickle
{
  "id": "big-pickle",
  "cost": {"input": 0, "output": 0, "cache": {"read": 0, "write": 0}},
  "variants": {}
}
opencode/paid-pro
{
  "id": "paid-pro",
  "cost": {"input": 1.25, "output": 10},
  "variants": {"high": {}}
}
opencode/flint-40b-free
{
  "id": "flint-40b-free",
  "cost": {"input": 0, "output": 0},
  "variants": {"max": {}, "low": {}, "high": {}}
}
`
	models, err := ParseOpenCode([]byte(out))
	if err != nil {
		t.Fatal(err)
	}
	want := []Model{{ID: "big-pickle"}, {ID: "flint-40b-free", Efforts: []string{"low", "high", "max"}}}
	if !reflect.DeepEqual(models, want) {
		t.Errorf("OpenCode offers %+v; want %+v, without the paid model", models, want)
	}
}
