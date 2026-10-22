package state

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/goccy/go-yaml"
)

type Profile struct {
	Commands struct {
		Build   string `yaml:"build"`
		Lint    string `yaml:"lint"`
		Test    string `yaml:"test"`
		Migrate string `yaml:"migrate"`
	} `yaml:"commands"`
	CodebaseSkill string `yaml:"codebase_skill"`
	Design        struct {
		Source string   `yaml:"source"`
		Files  []string `yaml:"files"`
	} `yaml:"design"`
	QA struct {
		Mode    string `yaml:"mode"`
		Notes   string `yaml:"notes"`
		APIPort int    `yaml:"api_port"`
		WebPort int    `yaml:"web_port"`
		Start   string `yaml:"start"`
		Health  string `yaml:"health"`
	} `yaml:"qa"`
	CodeRabbit *bool `yaml:"coderabbit"`
}

func ProfilePath(repo string) string {
	return filepath.Join(repo, ".claude", "factory.yml")
}

func ReadProfile(repo string) (Profile, error) {
	var p Profile
	data, err := os.ReadFile(ProfilePath(repo))
	if errors.Is(err, fs.ErrNotExist) {
		return p, nil
	}
	if err != nil {
		return p, err
	}
	err = yaml.Unmarshal(data, &p)
	return p, err
}

func (p Profile) BrowserQA() bool {
	return p.QA.Mode == "browser"
}
