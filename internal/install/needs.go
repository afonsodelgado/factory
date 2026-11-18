package install

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/goccy/go-yaml"

	"github.com/afonsodelgado/factory/internal/node"
	"github.com/afonsodelgado/factory/internal/state"
)

type Need = node.Need

var composeFiles = []string{"compose.yaml", "compose.yml", "docker-compose.yaml", "docker-compose.yml"}

var shellBuiltins = []string{"cd", "export", "source", ".", "set", "unset", "test", "[", "true", "false", "exit", "echo", "printf", "eval"}

var commandWrappers = []string{"env", "exec", "sudo", "nohup", "time", "command"}

var commandSeparators = regexp.MustCompile(`&&|\|\||[;|]`)

var variablePattern = regexp.MustCompile(`\$\{?([A-Za-z_][A-Za-z0-9_]*)`)

type needs struct {
	checkout string
	list     []Need
}

func (n *needs) add(need Need) {
	known := slices.ContainsFunc(n.list, func(have Need) bool { return have.Kind == need.Kind && have.Name == need.Name })
	if need.Name != "" && !known {
		n.list = append(n.list, need)
	}
}

func (n *needs) file(name string) string {
	return filepath.Join(n.checkout, name)
}

func RepoNeeds(checkout string) []Need {
	profile, _ := state.ReadProfile(checkout)
	n := &needs{checkout: checkout}
	n.packageManager()
	n.nodeVersion()
	n.goVersion()
	n.docker()
	n.programs(profile)
	n.envFiles()
	n.variables(profile.QA.SignIn)
	return n.list
}

func (n *needs) programs(p state.Profile) {
	for _, step := range []struct{ key, command string }{
		{"commands.build", p.Commands.Build}, {"commands.lint", p.Commands.Lint}, {"commands.test", p.Commands.Test}, {"commands.migrate", p.Commands.Migrate},
		{"qa.start", p.QA.Start}, {"qa.seed", p.QA.Seed}, {"qa.sign_in", p.QA.SignIn},
	} {
		n.add(Need{Name: firstProgram(step.command), Kind: node.NeedTool, Why: step.key + " runs it"})
	}
}

func firstProgram(command string) string {
	if command == state.NotNeeded {
		return ""
	}
	for _, part := range commandSeparators.Split(command, -1) {
		for _, word := range strings.Fields(part) {
			word = strings.Trim(word, `"'`)
			isAssignment := strings.Contains(word, "=") && !strings.HasPrefix(word, "=")
			if isAssignment || slices.Contains(commandWrappers, word) {
				continue
			}
			if slices.Contains(shellBuiltins, word) {
				break
			}
			isRepoScript := strings.ContainsAny(word, "/$")
			if isRepoScript {
				return ""
			}
			return word
		}
	}
	return ""
}

func (n *needs) packageManager() {
	data, err := os.ReadFile(n.file("package.json"))
	if err != nil {
		return
	}
	var manifest struct {
		PackageManager string `json:"packageManager"`
	}
	if json.Unmarshal(data, &manifest) != nil || manifest.PackageManager == "" {
		return
	}
	pinned, _, _ := strings.Cut(manifest.PackageManager, "+")
	name, version, _ := strings.Cut(pinned, "@")
	n.add(Need{Name: name, Kind: node.NeedTool, Why: "package.json asks for " + pinned, Version: version})
}

func (n *needs) nodeVersion() {
	data, err := os.ReadFile(n.file(".nvmrc"))
	if err != nil {
		return
	}
	wanted := strings.TrimPrefix(strings.TrimSpace(string(data)), "v")
	why := ".nvmrc asks for node " + wanted
	if !numericVersion.MatchString(wanted) {
		wanted = ""
	}
	n.add(Need{Name: "node", Kind: node.NeedTool, Why: why, Version: wanted})
}

var numericVersion = regexp.MustCompile(`^\d+(\.\d+)*$`)

var goLine = regexp.MustCompile(`(?m)^go\s+(\S+)`)

func (n *needs) goVersion() {
	data, err := os.ReadFile(n.file("go.mod"))
	if err != nil {
		return
	}
	need := Need{Name: "go", Kind: node.NeedTool, Why: "go.mod asks for go"}
	if m := goLine.FindSubmatch(data); m != nil {
		need.Version = string(m[1])
		need.Why += " " + need.Version
	}
	n.add(need)
}

func (n *needs) composeFile() string {
	for _, name := range composeFiles {
		if exists(n.file(name)) {
			return name
		}
	}
	return ""
}

func (n *needs) docker() {
	repo := filepath.Base(n.checkout)
	switch compose := n.composeFile(); {
	case compose != "":
		n.add(Need{Name: "docker", Kind: node.NeedTool, Why: compose + " runs " + repo + "'s QA stack"})
	case exists(n.file("Dockerfile")):
		n.add(Need{Name: "docker", Kind: node.NeedTool, Why: "Dockerfile builds " + repo + "'s image"})
	}
}

func (n *needs) envFiles() {
	if compose := n.composeFile(); compose != "" {
		for _, name := range composeEnvFiles(n.file(compose)) {
			n.add(Need{Name: name, Kind: node.NeedFile, Why: compose + " reads it"})
		}
	}
	templates, _ := filepath.Glob(n.file(".env*.example"))
	for _, template := range templates {
		name := filepath.Base(template)
		n.add(Need{Name: strings.TrimSuffix(name, ".example"), Kind: node.NeedFile, Why: name + " is its template"})
	}
}

func composeEnvFiles(path string) []string {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var compose struct {
		Services map[string]struct {
			EnvFile any `yaml:"env_file"`
		} `yaml:"services"`
	}
	if yaml.Unmarshal(data, &compose) != nil {
		return nil
	}
	var names []string
	for _, service := range compose.Services {
		for _, name := range envFileNames(service.EnvFile) {
			if filepath.IsLocal(name) && !strings.Contains(name, "$") {
				names = append(names, filepath.Clean(name))
			}
		}
	}
	slices.Sort(names)
	return slices.Compact(names)
}

func envFileNames(entry any) []string {
	switch e := entry.(type) {
	case string:
		return []string{e}
	case map[string]any:
		path, _ := e["path"].(string)
		return []string{path}
	case []any:
		var names []string
		for _, item := range e {
			names = append(names, envFileNames(item)...)
		}
		return names
	}
	return nil
}

func (n *needs) variables(signIn string) {
	for _, m := range variablePattern.FindAllStringSubmatch(signIn, -1) {
		if !strings.HasPrefix(m[1], "FACTORY_") {
			n.add(Need{Name: m[1], Kind: node.NeedVariable, Why: "qa.sign_in reads it"})
		}
	}
}

func CheckNeeds(ctx context.Context, on, repo, clone string, needs []Need) []node.Check {
	checks := make([]node.Check, 0, len(needs))
	for _, need := range needs {
		check := node.Check{Name: need.Name, Mark: node.MarkOK, On: on}
		switch need.Kind {
		case node.NeedTool:
			check = checkTool(ctx, clone, need, check)
		case node.NeedFile:
			if !inClone(clone, need.Name) {
				check.Mark, check.Detail, check.Fix = node.MarkFail, "not in "+on+"'s clone; "+need.Why, "put it in "+repo+"'s clone; it is never copied"
			}
		case node.NeedVariable:
			if os.Getenv(need.Name) == "" {
				check.Mark, check.Detail, check.Fix = node.MarkFail, "not set for "+on+"'s node; "+need.Why, "set it in the node's environment"
			}
		}
		checks = append(checks, check)
	}
	return checks
}

func inClone(clone, name string) bool {
	return clone != "" && filepath.IsLocal(name) && exists(filepath.Join(clone, name))
}

func checkTool(ctx context.Context, clone string, need Need, check node.Check) node.Check {
	if !hasCommand(need.Name) {
		check.Mark, check.Detail, check.Fix = node.MarkFail, "not installed; "+need.Why, toolFix(need)
		return check
	}
	if need.Version == "" {
		return check
	}
	have := installedVersion(ctx, clone, need.Name)
	check.Detail = have
	if have != "" && !compatible(have, need.Version) {
		check.Mark, check.Detail, check.Fix = node.MarkFail, have+" here; "+need.Why, toolFix(need)
	}
	return check
}

func compatible(have, want string) bool {
	haveMajor, _, _ := strings.Cut(have, ".")
	wantMajor, _, _ := strings.Cut(want, ".")
	return haveMajor == wantMajor && atLeast(have, want)
}

func installedVersion(ctx context.Context, clone, tool string) string {
	if strings.ContainsAny(tool, `/\`) || strings.HasPrefix(tool, "-") {
		return ""
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	flag := "--version"
	if tool == "go" {
		flag = "version"
	}
	cmd := exec.CommandContext(ctx, tool, flag)
	if dirExists(clone) {
		cmd.Dir = clone
	}
	out, _ := cmd.Output()
	return version(string(out))
}

func toolFix(need Need) string {
	if slices.Contains([]string{"pnpm", "yarn"}, need.Name) && need.Version != "" {
		return "npm install -g " + need.Name + "@" + need.Version
	}
	return installCommand(need.Name)
}
