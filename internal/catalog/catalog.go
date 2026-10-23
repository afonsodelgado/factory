package catalog

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/afonsodelgado/factory/internal/state"
)

type Model struct {
	ID      string   `json:"id"`
	Efforts []string `json:"efforts"`
}

var ErrCodexNotChatGPT = errors.New("Codex is not logged in with ChatGPT; run codex login and sign in with ChatGPT")

func Claude() []Model {
	var models []Model
	for _, id := range state.AgentModels {
		models = append(models, Model{ID: id, Efforts: state.AgentEfforts})
	}
	return models
}

func Codex(home string) ([]Model, error) {
	var auth struct {
		Mode string `json:"auth_mode"`
	}
	if err := readJSON(filepath.Join(home, "auth.json"), &auth); err != nil || auth.Mode != "chatgpt" {
		return nil, ErrCodexNotChatGPT
	}
	var cache struct {
		Models []struct {
			Slug       string `json:"slug"`
			Visibility string `json:"visibility"`
			Levels     []struct {
				Effort string `json:"effort"`
			} `json:"supported_reasoning_levels"`
		} `json:"models"`
	}
	if err := readJSON(filepath.Join(home, "models_cache.json"), &cache); err != nil {
		return nil, fmt.Errorf("Codex has no model list yet; start codex once: %w", err)
	}
	var models []Model
	for _, m := range cache.Models {
		if m.Visibility != "list" {
			continue
		}
		model := Model{ID: m.Slug}
		for _, level := range m.Levels {
			model.Efforts = append(model.Efforts, level.Effort)
		}
		models = append(models, model)
	}
	return models, nil
}

func OpenCodeCachePath(stateDir string) string {
	return filepath.Join(stateDir, "catalog", "opencode.json")
}

func OpenCode(stateDir string) ([]Model, error) {
	var models []Model
	if err := readJSON(OpenCodeCachePath(stateDir), &models); err != nil {
		return nil, errors.New("no free OpenCode models listed yet; the list refreshes in the background")
	}
	return models, nil
}

func RefreshOpenCode(ctx context.Context, stateDir string) error {
	out, err := exec.CommandContext(ctx, "opencode", "models", "opencode", "--verbose").Output()
	if err != nil {
		return fmt.Errorf("opencode models: %w", err)
	}
	models, err := ParseOpenCode(out)
	if err != nil {
		return err
	}
	data, err := json.Marshal(models)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(OpenCodeCachePath(stateDir)), 0o755); err != nil {
		return err
	}
	return state.WriteFileAtomic(OpenCodeCachePath(stateDir), data)
}

var openCodeHeader = regexp.MustCompile(`^opencode/(\S+)$`)

var effortOrder = []string{"none", "minimal", "low", "medium", "high", "xhigh", "max", "ultra"}

func ParseOpenCode(out []byte) ([]Model, error) {
	var models []Model
	var id string
	var body bytes.Buffer
	flush := func() error {
		if id == "" {
			return nil
		}
		model, free, err := parseOpenCodeModel(id, body.Bytes())
		if err != nil {
			return err
		}
		if free {
			models = append(models, model)
		}
		return nil
	}
	scanner := bufio.NewScanner(bytes.NewReader(out))
	scanner.Buffer(make([]byte, 1024*1024), 16*1024*1024)
	for scanner.Scan() {
		if header := openCodeHeader.FindStringSubmatch(scanner.Text()); header != nil {
			if err := flush(); err != nil {
				return nil, err
			}
			id = header[1]
			body.Reset()
			continue
		}
		body.Write(scanner.Bytes())
		body.WriteByte('\n')
	}
	if err := flush(); err != nil {
		return nil, err
	}
	return models, scanner.Err()
}

func parseOpenCodeModel(id string, body []byte) (Model, bool, error) {
	var m struct {
		Cost struct {
			Input  float64 `json:"input"`
			Output float64 `json:"output"`
		} `json:"cost"`
		Variants map[string]json.RawMessage `json:"variants"`
	}
	if err := json.Unmarshal(body, &m); err != nil {
		return Model{}, false, fmt.Errorf("opencode models: %s: %w", id, err)
	}
	free := m.Cost.Input == 0 && m.Cost.Output == 0
	return Model{ID: id, Efforts: orderedEfforts(m.Variants)}, free, nil
}

func orderedEfforts(variants map[string]json.RawMessage) []string {
	var efforts []string
	for name := range variants {
		efforts = append(efforts, name)
	}
	slices.SortFunc(efforts, func(a, b string) int {
		if rank(a) != rank(b) {
			return rank(a) - rank(b)
		}
		return strings.Compare(a, b)
	})
	return efforts
}

func rank(effort string) int {
	if i := slices.Index(effortOrder, effort); i >= 0 {
		return i
	}
	return len(effortOrder)
}

func readJSON(path string, v any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}
