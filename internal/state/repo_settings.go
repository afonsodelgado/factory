package state

import (
	"bytes"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/goccy/go-yaml"
	"github.com/goccy/go-yaml/ast"
	"github.com/goccy/go-yaml/parser"
)

type RepoSettings struct {
	Values     map[string]string
	Present    map[string]bool
	Provenance map[string]Found
	path       string
	original   []byte
}

type Found struct {
	Factory string
	Day     time.Time
}

type Recorded struct {
	Recorded []string `json:"recorded"`
	Kept     []string `json:"kept"`
}

const provenanceMark = " · "

var foundComment = regexp.MustCompile(`(?:^|` + provenanceMark + `)found by (\S+), (\d{4}-\d{2}-\d{2})$`)

func commentText(node ast.Node) string {
	if node == nil || node.GetComment() == nil {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(node.GetComment().String()), "#"))
}

func foundBy(node ast.Node) (Found, bool) {
	match := foundComment.FindStringSubmatch(commentText(node))
	if match == nil {
		return Found{}, false
	}
	day, err := time.Parse(time.DateOnly, match[2])
	return Found{Factory: match[1], Day: day}, err == nil
}

func ownComment(node ast.Node) string {
	return strings.TrimSpace(foundComment.ReplaceAllString(commentText(node), ""))
}

func lineComment(own, provenance string) *ast.CommentGroupNode {
	text := own + provenanceMark + provenance
	if own == "" || provenance == "" {
		text = own + provenance
	}
	if text == "" {
		return nil
	}
	file, _ := parser.ParseBytes([]byte("x: y # "+text+"\n"), parser.ParseComments)
	return settingsNode(file, "x").GetComment()
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
	s := &RepoSettings{path: path, original: data, Values: map[string]string{}, Present: map[string]bool{}, Provenance: map[string]Found{}}
	for _, key := range SettingsKeys("browser") {
		node := settingsNode(file, key)
		s.Present[key] = node != nil
		value := ""
		if node != nil {
			err = yaml.NodeToValue(node, &value)
			if err != nil {
				return nil, fmt.Errorf("%s: %s: %w", path, key, err)
			}
			if found, ok := foundBy(node); ok {
				s.Provenance[key] = found
			}
		}
		s.Values[key] = value
	}
	return s, nil
}

type Verify func(effective map[string]string, written []string) error

func RecordDiscovered(repo, factory string, day time.Time, values map[string]string, replace []string, verify Verify) (Recorded, error) {
	provenance := "found by " + factory + ", " + day.Format(time.DateOnly)
	return record(repo, values, verify, func(s *RepoSettings, key string) (bool, string) {
		return s.Values[key] == "" || (s.Discovered(key) && slices.Contains(replace, key)), provenance
	})
}

func RecordYours(repo string, values map[string]string) (Recorded, error) {
	return record(repo, values, nil, func(*RepoSettings, string) (bool, string) { return true, "" })
}

const verifyAttempts = 3

func record(repo string, values map[string]string, verify Verify, writable func(*RepoSettings, string) (bool, string)) (Recorded, error) {
	none := Recorded{Recorded: []string{}, Kept: []string{}}
	for key := range values {
		if !slices.Contains(SettingsKeys("browser"), key) {
			return none, fmt.Errorf("%s is not a repo setting factory records; use one of %s", key, strings.Join(SettingsKeys("browser"), ", "))
		}
	}
	path, err := filepath.EvalSymlinks(RepoConfigPath(repo))
	if err != nil {
		return none, err
	}
	var verified map[string]string
	for attempt := range verifyAttempts {
		var p recordPlan
		written := false
		err := locked(path+".lock", func() error {
			if p, err = planRecord(repo, values, writable); err != nil || len(p.outcome.Recorded) == 0 {
				return err
			}
			if verify != nil && !maps.Equal(verified, p.effective) {
				return nil
			}
			written = true
			return p.write()
		})
		if err != nil || written || len(p.outcome.Recorded) == 0 {
			return p.outcome, err
		}
		if attempt == verifyAttempts-1 {
			break
		}
		if err := verify(p.effective, p.outcome.Recorded); err != nil {
			return none, err
		}
		verified = p.effective
	}
	return none, errors.New("the repo config kept changing while its commands were verified; run factory workflow record again")
}

type recordPlan struct {
	outcome    Recorded
	settings   *RepoSettings
	effective  map[string]string
	provenance map[string]string
}

func planRecord(repo string, values map[string]string, writable func(*RepoSettings, string) (bool, string)) (recordPlan, error) {
	s, err := ReadRepoSettings(repo)
	if err != nil {
		return recordPlan{}, err
	}
	p := recordPlan{outcome: Recorded{Recorded: []string{}, Kept: []string{}}, settings: s, effective: maps.Clone(s.Values), provenance: map[string]string{}}
	for _, key := range SettingsKeys("browser") {
		value, asked := values[key]
		if !asked {
			continue
		}
		ok, found := writable(s, key)
		if !ok {
			p.outcome.Kept = append(p.outcome.Kept, key)
			continue
		}
		p.effective[key], p.provenance[key] = value, found
		p.outcome.Recorded = append(p.outcome.Recorded, key)
	}
	return p, nil
}

func (p recordPlan) write() error {
	file, empty, err := p.settings.parsed()
	if err != nil {
		return err
	}
	for _, key := range p.outcome.Recorded {
		comment := lineComment(ownComment(settingsNode(file, key)), p.provenance[key])
		if err := setSetting(file, key, p.effective[key], comment); err != nil {
			return err
		}
	}
	return p.settings.write(file, empty)
}

func locked(path string, do func() error) error {
	unlock, err := lockFile(path)
	if err != nil {
		return err
	}
	defer unlock()
	return do()
}

func setSetting(file *ast.File, key, value string, comment *ast.CommentGroupNode) error {
	encoded, err := encodeSetting(value)
	if err != nil {
		return err
	}
	if err := editSetting(file, key, encoded); err != nil {
		return err
	}
	return settingsNode(file, key).SetComment(comment)
}

func SettingsKeys(mode string) []string {
	keys := []string{"commands.build", "commands.lint", "commands.test", "qa.mode"}
	if mode == "browser" {
		keys = append(keys, "qa.start", "qa.health", "qa.seed", "qa.sign_in", "qa.url")
	}
	return keys
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

func (s *RepoSettings) Discovered(key string) bool {
	_, found := s.Provenance[key]
	return found
}

func (s *RepoSettings) parsed() (*ast.File, bool, error) {
	file, err := parseRepoSettings(s.normalized())
	if err != nil {
		return nil, false, err
	}
	empty := isEmptyDocument(file)
	if empty {
		file, _ = parser.ParseBytes([]byte("{}\n"), parser.ParseComments)
	}
	return file, empty, nil
}

func (s *RepoSettings) normalized() []byte {
	return bytes.ReplaceAll(s.original, []byte("\r\n"), []byte("\n"))
}

func (s *RepoSettings) Save(values map[string]string) error {
	unlock, err := lockFile(s.path + ".lock")
	if err != nil {
		return err
	}
	defer unlock()
	file, empty, err := s.parsed()
	if err != nil {
		return err
	}
	changes := s.Changes(values)
	for _, key := range changes {
		if err := setSetting(file, key, values[key], lineComment(ownComment(settingsNode(file, key)), "")); err != nil {
			return err
		}
	}
	if len(changes) == 0 {
		return s.unchangedSince()
	}
	return s.write(file, empty)
}

func (s *RepoSettings) unchangedSince() error {
	current, err := os.ReadFile(s.path)
	if err != nil {
		return err
	}
	if !bytes.Equal(current, s.original) {
		return fmt.Errorf("the config changed since opening; reopen settings before saving")
	}
	return nil
}

func (s *RepoSettings) write(file *ast.File, empty bool) error {
	data := []byte(file.String())
	if empty {
		data = append(append(s.normalized(), '\n'), data...)
	}
	if _, err := parseRepoSettings(data); err != nil {
		return err
	}
	if err := s.unchangedSince(); err != nil {
		return err
	}
	if bytes.Contains(s.original, []byte("\r\n")) {
		data = bytes.ReplaceAll(data, []byte("\n"), []byte("\r\n"))
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
	if text, ok := value.(string); ok && strings.ContainsAny(text, ",[]{}\n\r\t") {
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
