package recipe

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/afonsodelgado/factory/internal/state"
	"github.com/goccy/go-yaml"
)

type Resolution struct {
	Profile state.Profile `json:"-"`
	Recipe  state.Recipe  `json:"recipe"`
}

func Resolve(root string) (Resolution, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return Resolution{}, err
	}
	r := Resolution{Recipe: state.Recipe{Commands: map[string]state.Command{}, QA: map[string]state.Command{}}}
	inputs := map[string]string{}
	read := func(path string) ([]byte, error) {
		data, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			inputs[path] = "absent"
			return nil, nil
		}
		if err == nil {
			inputs[path] = string(data)
		}
		return data, err
	}
	checkouts := []string{root}
	if out, err := exec.Command("git", "-C", root, "rev-parse", "--path-format=absolute", "--git-common-dir").Output(); err == nil {
		if main := filepath.Dir(strings.TrimSpace(string(out))); main != root {
			checkouts = []string{main, root}
		}
	}
	paths := []string{}
	for _, dir := range checkouts {
		paths = append(paths, state.LegacyProfilePath(dir))
	}
	for _, dir := range checkouts {
		paths = append(paths, state.RepoConfigPath(dir))
	}
	merged := map[string]any{}
	sources := map[string]string{}
	for _, path := range paths {
		data, err := read(path)
		if err != nil {
			return r, err
		}
		if len(data) == 0 {
			continue
		}
		var values map[string]any
		if err := yaml.Unmarshal(data, &values); err != nil {
			return r, fmt.Errorf("%s: %w", path, err)
		}
		merge(merged, values, "", path, sources)
	}
	data, err := yaml.Marshal(merged)
	if err != nil {
		return r, err
	}
	if err := yaml.Unmarshal(data, &r.Profile); err != nil {
		return r, err
	}
	put := func(group map[string]state.Command, key, command, source, dir string) {
		if command != "" {
			group[key] = state.Command{Command: command, Source: source, Directory: dir}
		}
	}
	for name, command := range map[string]string{"build": r.Profile.Commands.Build, "lint": r.Profile.Commands.Lint, "test": r.Profile.Commands.Test, "migrate": r.Profile.Commands.Migrate} {
		put(r.Recipe.Commands, name, command, sources["commands."+name], root)
	}
	for name, command := range map[string]string{"mode": r.Profile.QA.Mode, "start": r.Profile.QA.Start, "health": r.Profile.QA.Health, "url": r.Profile.QA.URL, "seed": r.Profile.QA.Seed, "sign_in": r.Profile.QA.SignIn} {
		put(r.Recipe.QA, name, command, sources["qa."+name], root)
	}
	makefile, err := read(filepath.Join(root, "Makefile"))
	if err != nil {
		return r, err
	}
	pkgData, err := read(filepath.Join(root, "package.json"))
	if err != nil {
		return r, err
	}
	var pkg struct {
		Scripts        map[string]string `json:"scripts"`
		PackageManager string            `json:"packageManager"`
	}
	if len(pkgData) > 0 {
		if err := json.Unmarshal(pkgData, &pkg); err != nil {
			return r, err
		}
	}
	managers := []string{}
	for file, manager := range map[string]string{"pnpm-lock.yaml": "pnpm", "yarn.lock": "yarn", "package-lock.json": "npm", "bun.lock": "bun", "bun.lockb": "bun"} {
		data, err := read(filepath.Join(root, file))
		if err != nil {
			return r, err
		}
		if data != nil && !slices.Contains(managers, manager) {
			managers = append(managers, manager)
		}
	}
	manager, _, _ := strings.Cut(pkg.PackageManager, "@")
	if !slices.Contains([]string{"npm", "pnpm", "yarn", "bun"}, manager) {
		manager = ""
	}
	if manager == "" && len(managers) == 1 {
		manager = managers[0]
	}
	goMod, err := read(filepath.Join(root, "go.mod"))
	if err != nil {
		return r, err
	}
	candidates, err := ciCommands(root, read)
	if err != nil {
		return r, err
	}
	defaults := map[string]string{"build": "go build ./...", "lint": "go vet ./...", "test": "go test ./..."}
	for _, key := range []string{"build", "lint", "test"} {
		if _, ok := r.Recipe.Commands[key]; ok {
			continue
		}
		switch {
		case target(makefile, key):
			put(r.Recipe.Commands, key, "make "+key, "Makefile:"+key, root)
		case pkg.Scripts[key] != "":
			if manager == "" {
				r.Recipe.Unresolved = append(r.Recipe.Unresolved, key+": declare a package manager or one unambiguous lockfile")
			} else {
				put(r.Recipe.Commands, key, manager+" run "+key, "package.json:scripts."+key, root)
			}
		case goMod != nil:
			put(r.Recipe.Commands, key, defaults[key], "go.mod", root)
		case len(candidates[key]) == 1:
			r.Recipe.Commands[key] = candidates[key][0]
		default:
			r.Recipe.Unresolved = append(r.Recipe.Unresolved, key+": no complete unambiguous local command")
		}
	}
	if r.Profile.QA.Start == "" {
		key := "dev"
		if !target(makefile, key) && pkg.Scripts[key] == "" {
			key = "start"
		}
		switch {
		case target(makefile, key):
			put(r.Recipe.QA, "start", "make "+key, "Makefile:"+key, root)
		case pkg.Scripts[key] != "" && manager != "":
			put(r.Recipe.QA, "start", manager+" run "+key, "package.json:scripts."+key, root)
		}
	}
	endpoints := map[string][]state.Command{}
	for _, name := range []string{"playwright.config.ts", "playwright.config.js", "playwright.config.mjs"} {
		data, err := read(filepath.Join(root, name))
		if err != nil {
			return r, err
		}
		for key, pattern := range map[string]string{"url": `baseURL\s*:\s*["'](http://(?:localhost|127\.0\.0\.1):[0-9]+[^"']*)["']`, "health": `webServer\s*:\s*\{[^}]*?url\s*:\s*["'](http://(?:localhost|127\.0\.0\.1):[0-9]+[^"']*)["']`} {
			if _, ok := r.Recipe.QA[key]; ok {
				continue
			}
			matches := regexp.MustCompile(pattern).FindAllSubmatch(data, -1)
			for _, match := range matches {
				command := string(match[1])
				if !slices.ContainsFunc(endpoints[key], func(c state.Command) bool { return c.Command == command }) {
					endpoints[key] = append(endpoints[key], state.Command{Command: command, Source: name + ":" + key, Directory: root})
				}
			}
		}
	}
	for key, candidates := range endpoints {
		if len(candidates) == 1 {
			r.Recipe.QA[key] = candidates[0]
		}
	}
	r.Profile.QA.URL = r.Recipe.QA["url"].Command
	r.Profile.QA.Health = r.Recipe.QA["health"].Command
	if r.Profile.QA.Mode == "browser" {
		for _, key := range []string{"start", "url", "health", "seed", "sign_in"} {
			if _, ok := r.Recipe.QA[key]; !ok {
				r.Recipe.Unresolved = append(r.Recipe.Unresolved, "qa."+key+": declare the local startup prerequisite")
			}
		}
	}
	r.Profile.Commands.Build = r.Recipe.Commands["build"].Command
	r.Profile.Commands.Lint = r.Recipe.Commands["lint"].Command
	r.Profile.Commands.Test = r.Recipe.Commands["test"].Command
	r.Profile.QA.Start = r.Recipe.QA["start"].Command
	encoded, _ := json.Marshal(inputs)
	digest := sha256.Sum256(encoded)
	r.Recipe.Digest = hex.EncodeToString(digest[:])
	return r, nil
}

func merge(dst, src map[string]any, prefix, path string, sources map[string]string) {
	for key, value := range src {
		name := prefix + key
		if nested, ok := value.(map[string]any); ok {
			child, _ := dst[key].(map[string]any)
			if child == nil {
				child = map[string]any{}
			}
			merge(child, nested, name+".", path, sources)
			dst[key] = child
		} else {
			dst[key] = value
			sources[name] = path + ":" + name
		}
	}
}

func target(data []byte, name string) bool {
	return regexp.MustCompile(`(?m)^(?:[A-Za-z0-9_.-]+[ \t]+)*` + regexp.QuoteMeta(name) + `(?:[ \t]+[A-Za-z0-9_.-]+)*[ \t]*:(?:[^=]|$)`).Match(data)
}

func ciCommands(root string, read func(string) ([]byte, error)) (map[string][]state.Command, error) {
	candidates := map[string][]state.Command{}
	paths, err := filepath.Glob(filepath.Join(root, ".github", "workflows", "*.y*ml"))
	if err != nil {
		return nil, err
	}
	for _, path := range paths {
		data, err := read(path)
		if err != nil {
			return nil, err
		}
		var config struct {
			Env      map[string]any `yaml:"env"`
			Defaults struct {
				Run struct {
					Directory string `yaml:"working-directory"`
				}
			} `yaml:"defaults"`
			Jobs map[string]struct {
				Env      map[string]any `yaml:"env"`
				Defaults struct {
					Run struct {
						Directory string `yaml:"working-directory"`
					}
				} `yaml:"defaults"`
				Steps []struct {
					Run       string         `yaml:"run"`
					Directory string         `yaml:"working-directory"`
					Env       map[string]any `yaml:"env"`
				} `yaml:"steps"`
			} `yaml:"jobs"`
		}
		if err := yaml.Unmarshal(data, &config); err != nil {
			return nil, err
		}
		for jobName, job := range config.Jobs {
			for _, step := range job.Steps {
				command := strings.TrimSpace(step.Run)
				if command == "" || len(step.Env) > 0 || len(job.Env) > 0 || len(config.Env) > 0 || strings.ContainsAny(command, "\n\r$;&|`<>") {
					continue
				}
				key := ""
				for _, name := range []string{"build", "lint", "test"} {
					if regexp.MustCompile(`^(go ` + map[string]string{"build": "build", "lint": "vet", "test": "test"}[name] + `(?: ./\.\.\.)?|make ` + name + `|(?:npm|pnpm|yarn|bun) run ` + name + `)$`).MatchString(command) {
						key = name
					}
				}
				if key == "" {
					continue
				}
				dir := step.Directory
				if dir == "" {
					dir = job.Defaults.Run.Directory
				}
				if dir == "" {
					dir = config.Defaults.Run.Directory
				}
				if dir == "" {
					dir = "."
				}
				if !filepath.IsLocal(dir) || strings.Contains(dir, "$") {
					continue
				}
				c := state.Command{Command: command, Directory: filepath.Join(root, dir), Source: filepath.ToSlash(path) + ":" + jobName}
				if !slices.ContainsFunc(candidates[key], func(old state.Command) bool { return old.Command == c.Command && old.Directory == c.Directory }) {
					candidates[key] = append(candidates[key], c)
				}
			}
		}
	}
	return candidates, nil
}
