package state

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/goccy/go-yaml"
	"github.com/goccy/go-yaml/ast"
	"github.com/goccy/go-yaml/parser"
)

type RepoSettings struct {
	Values   map[string]string
	Present  map[string]bool
	path     string
	original []byte
}

func ReadRepoSettings(repo string) (*RepoSettings, error) {
	path, err := filepath.EvalSymlinks(RepoConfigPath(repo))
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	file, err := parseRepoSettings(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	s := &RepoSettings{path: path, original: data, Values: map[string]string{}, Present: map[string]bool{}}
	for _, key := range SettingsKeys("browser") {
		node := settingsNode(file, key)
		s.Present[key] = node != nil
		value := ""
		if node != nil {
			if key == "coderabbit" && node.Type() == ast.NullType {
				value = "true"
			} else if key == "coderabbit" {
				var enabled bool
				err = yaml.NodeToValue(node, &enabled)
				value = fmt.Sprint(enabled)
			} else {
				err = yaml.NodeToValue(node, &value)
			}
			if err != nil {
				return nil, fmt.Errorf("%s: %s: %w", path, key, err)
			}
		} else if key == "coderabbit" {
			value = "true"
		} else if key == "qa.mode" {
			value = "terminal"
		}
		s.Values[key] = value
	}
	return s, nil
}

func SettingsKeys(mode string) []string {
	keys := []string{"commands.build", "commands.lint", "commands.test", "qa.mode"}
	if mode == "browser" {
		keys = append(keys, "qa.start", "qa.health")
	}
	return append(keys, "coderabbit")
}

func (s *RepoSettings) Changes(values map[string]string) []string {
	var keys []string
	for _, key := range SettingsKeys(values["qa.mode"]) {
		if !s.Present[key] || s.Values[key] != values[key] {
			keys = append(keys, key)
		}
	}
	return keys
}

func parseRepoSettings(data []byte) (*ast.File, error) {
	file, err := parser.ParseBytes(data, parser.ParseComments)
	if err != nil {
		return nil, err
	}
	if len(file.Docs) > 1 {
		return nil, fmt.Errorf("settings need a single YAML document")
	}
	if isEmptyDocument(file) {
		return file, nil
	}
	root, ok := file.Docs[0].Body.(*ast.MappingNode)
	if !ok {
		return nil, fmt.Errorf("settings need a root mapping")
	}
	if err := editableMapping(root, ""); err != nil {
		return nil, err
	}
	for _, section := range []string{"commands", "qa"} {
		node := settingsNode(file, section)
		if node == nil || node.Type() == ast.NullType {
			continue
		}
		mapping, ok := node.(*ast.MappingNode)
		if !ok {
			return nil, fmt.Errorf("%s must be a mapping without an anchor or alias", section)
		}
		if err := editableMapping(mapping, section+"."); err != nil {
			return nil, err
		}
	}
	for _, key := range SettingsKeys("browser") {
		node := settingsNode(file, key)
		if node == nil {
			continue
		}
		switch node.Type() {
		case ast.StringType, ast.LiteralType, ast.BoolType, ast.NullType:
		default:
			return nil, fmt.Errorf("%s must be a scalar without an anchor, alias or tag", key)
		}
	}
	return file, nil
}

func isEmptyDocument(file *ast.File) bool {
	if len(file.Docs) == 0 || file.Docs[0].Body == nil {
		return true
	}
	bodyType := file.Docs[0].Body.Type()
	return bodyType == ast.CommentGroupType || bodyType == ast.CommentType
}

func editableMapping(mapping *ast.MappingNode, prefix string) error {
	seen := map[string]bool{}
	for _, entry := range mapping.Values {
		key, ok := entry.Key.(*ast.StringNode)
		if !ok {
			return fmt.Errorf("%ssettings cannot edit merge or complex keys", prefix)
		}
		if seen[key.Value] {
			return fmt.Errorf("%s%s is duplicated", prefix, key.Value)
		}
		seen[key.Value] = true
	}
	return nil
}

func settingsNode(file *ast.File, key string) ast.Node {
	path, _ := yaml.PathString("$." + key)
	node, _ := path.FilterFile(file)
	return node
}

func (s *RepoSettings) Save(values map[string]string) error {
	file, err := parseRepoSettings(s.original)
	if err != nil {
		return err
	}
	empty := isEmptyDocument(file)
	if empty {
		file, _ = parser.ParseBytes([]byte("{}\n"), parser.ParseComments)
	}
	for _, key := range s.Changes(values) {
		var value any = values[key]
		if key == "coderabbit" {
			value = values[key] == "true"
		}
		encoded, err := encodeSetting(value)
		if err != nil {
			return err
		}
		if err := editSetting(file, key, encoded); err != nil {
			return err
		}
	}
	data := []byte(file.String())
	if empty {
		data = append(append(bytes.Clone(s.original), '\n'), data...)
	}
	if _, err := parseRepoSettings(data); err != nil {
		return err
	}
	current, err := os.ReadFile(s.path)
	if err != nil {
		return err
	}
	if !bytes.Equal(current, s.original) {
		return fmt.Errorf("the config changed since opening; reopen settings before saving")
	}
	info, err := os.Stat(s.path)
	if err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(s.path), ".config-*")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())
	if err = temp.Chmod(info.Mode().Perm()); err == nil {
		_, err = temp.Write(data)
	}
	closeErr := temp.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(temp.Name(), s.path)
}

func encodeSetting(value any) (string, error) {
	if text, ok := value.(string); ok && strings.ContainsAny(text, ",[]{}") {
		return strconv.Quote(text) + "\n", nil
	}
	encoded, err := yaml.Marshal(value)
	return string(encoded), err
}

func editSetting(file *ast.File, key, encoded string) error {
	path, _ := yaml.PathString("$." + key)
	if node := settingsNode(file, key); node != nil {
		comment := node.GetComment()
		if err := path.ReplaceWithReader(file, strings.NewReader(encoded)); err != nil {
			return err
		}
		return settingsNode(file, key).SetComment(comment)
	}
	parent, child, nested := strings.Cut(key, ".")
	if nested {
		node := settingsNode(file, parent)
		if node != nil && node.Type() == ast.NullType {
			comment := node.GetComment()
			parentPath, _ := yaml.PathString("$." + parent)
			if err := parentPath.ReplaceWithReader(file, strings.NewReader("{}")); err != nil {
				return err
			}
			if err := settingsNode(file, parent).SetComment(comment); err != nil {
				return err
			}
		}
		if settingsNode(file, parent) != nil {
			parentPath, _ := yaml.PathString("$." + parent)
			return parentPath.MergeFromReader(file, strings.NewReader(child+": "+encoded))
		}
		encoded = "{ " + child + ": " + strings.TrimSpace(encoded) + " }\n"
	}
	root, _ := yaml.PathString("$")
	return root.MergeFromReader(file, strings.NewReader(parent+": "+encoded))
}
