package host

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/afonsodelgado/factory/internal/tmux"
)

const (
	captureLines   = 60
	promptPolls    = 20
	promptInterval = 100 * time.Millisecond
	tellsPrefix    = "No, and tell Claude what to do differently"
)

type Console interface {
	Capture(pane string) (string, error)
	Press(pane, key string) error
}

type tmuxConsole struct{ tm tmux.Client }

func (c tmuxConsole) Capture(pane string) (string, error) { return c.tm.Capture(pane, captureLines) }

func (c tmuxConsole) Press(pane, key string) error { return c.tm.SendKeys(pane, key) }

func (m *Runtime) console() Console {
	if m.Console != nil {
		return m.Console
	}
	return tmuxConsole{m.TM}
}

type PermissionOption struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Tells bool   `json:"tells,omitempty"`
}

type PermissionMenu struct {
	Tool        string             `json:"tool"`
	Title       string             `json:"title"`
	Command     []string           `json:"command"`
	Question    string             `json:"question"`
	Options     []PermissionOption `json:"options"`
	Fingerprint string             `json:"fingerprint"`
}

var (
	optionLine    = regexp.MustCompile(`^(?:[❯>›]\s*)?(\d+)\.\s+(\S.*)$`)
	menuLine      = regexp.MustCompile(`^([❯>›]?)\s*(\d+)\.\s+(\S.*)$`)
	yesNoQuestion = regexp.MustCompile(`\(yes/no[^)]*\)\??:?$`)
	yNQuestion    = regexp.MustCompile(`(?:\[y/N\]|\[Y/n\]|\(y/n\))\??:?$`)
)

const (
	boxChars     = "│┃|╭╮╰╯─━"
	contextLines = 4
	shownLines   = 5
	menuSettle   = 300 * time.Millisecond
)

type Choice struct {
	ID    string   `json:"id"`
	Label string   `json:"label"`
	keys  []string `json:"-"`
}

type Menu struct {
	Choices     []Choice `json:"choices"`
	Fingerprint string   `json:"fingerprint"`
	context     []string
	cursor      string
}

var supportedTitles = map[string][]string{
	"Bash": {"Bash command"},
}

func screenLines(screen string) []string {
	var lines []string
	for _, l := range strings.Split(screen, "\n") {
		lines = append(lines, strings.Trim(l, " \t"+boxChars))
	}
	return lines
}

func ParsePermission(screen, tool string) (PermissionMenu, bool) {
	lines := screenLines(screen)
	q := lastIndexFunc(lines, isProceedQuestion)
	if q < 0 {
		return PermissionMenu{}, false
	}
	options, rest, ok := parseOptions(lines[q+1:])
	if !ok || slices.ContainsFunc(rest, notFooter) {
		return PermissionMenu{}, false
	}
	title, command, ok := heading(lines[:q])
	if !ok || !slices.Contains(supportedTitles[tool], title) {
		return PermissionMenu{}, false
	}
	return PermissionMenu{Tool: tool, Title: title, Command: command, Question: lines[q], Options: options}, true
}

func ParseMenu(screen string) (Menu, bool) {
	lines := screenLines(screen)
	end := lastIndexFunc(lines, func(l string) bool { return !footerLine(l) })
	if end < 0 {
		return Menu{}, false
	}
	lines = lines[:end+1]
	switch {
	case yesNoQuestion.MatchString(lines[end]):
		return answeredBy(lines, "yes", "no"), true
	case yNQuestion.MatchString(lines[end]):
		return answeredBy(lines, "y", "n"), true
	}
	return numberedMenu(lines)
}

func footerLine(l string) bool {
	lower := strings.ToLower(l)
	return l == "" || strings.Contains(lower, "enter to ") || strings.HasPrefix(lower, "esc to ")
}

func answeredBy(lines []string, yes, no string) Menu {
	return Menu{
		context: lastNonEmpty(lines, contextLines),
		Choices: []Choice{{ID: "yes", Label: "yes", keys: []string{yes, "Enter"}}, {ID: "no", Label: "no", keys: []string{no, "Enter"}}},
	}
}

func numberedMenu(lines []string) (Menu, bool) {
	start := len(lines)
	for start > 0 && menuLine.MatchString(lines[start-1]) {
		start--
	}
	menu := Menu{context: lastNonEmpty(lines[:start], contextLines)}
	cursors := 0
	for i, l := range lines[start:] {
		match := menuLine.FindStringSubmatch(l)
		if match[2] != strconv.Itoa(i+1) {
			return Menu{}, false
		}
		if match[1] != "" {
			cursors++
			menu.cursor = match[2]
		}
		menu.Choices = append(menu.Choices, Choice{ID: match[2], Label: match[3], keys: []string{match[2]}})
	}
	return menu, len(menu.Choices) >= 2 && cursors == 1
}

func (m Menu) numbered() bool { return m.cursor != "" }

func lastNonEmpty(lines []string, n int) []string {
	kept := slices.DeleteFunc(slices.Clone(lines), func(l string) bool { return l == "" })
	return kept[max(0, len(kept)-n):]
}

func ScreenTail(screen string) []string {
	var lines []string
	for _, l := range strings.Split(screen, "\n") {
		l = withoutBorder(strings.TrimRight(l, " \t"+boxChars))
		if strings.TrimSpace(l) != "" {
			lines = append(lines, l)
		}
	}
	return dedent(lines[max(0, len(lines)-shownLines):])
}

func withoutBorder(l string) string {
	rest := strings.TrimLeft(l, " \t")
	inner := strings.TrimLeft(rest, boxChars)
	if inner == rest {
		return l
	}
	return strings.TrimPrefix(inner, " ")
}

func dedent(lines []string) []string {
	indent := -1
	for _, l := range lines {
		if n := len(l) - len(strings.TrimLeft(l, " ")); indent < 0 || n < indent {
			indent = n
		}
	}
	for i := range lines {
		lines[i] = lines[i][indent:]
	}
	return lines
}

func promptFingerprint(pane *tmux.Pane, menu Menu) string {
	parts := append([]string{pane.ID, pane.Token}, menu.context...)
	for _, c := range menu.Choices {
		parts = append(parts, c.ID+" "+c.Label)
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:16])
}

func isProceedQuestion(l string) bool {
	return strings.HasPrefix(l, "Do you want to") && strings.HasSuffix(l, "?")
}

func lastIndexFunc(lines []string, f func(string) bool) int {
	for i := len(lines) - 1; i >= 0; i-- {
		if f(lines[i]) {
			return i
		}
	}
	return -1
}

func parseOptions(lines []string) ([]PermissionOption, []string, bool) {
	var options []PermissionOption
	for i, l := range lines {
		match := optionLine.FindStringSubmatch(l)
		if match == nil || match[1] != strconv.Itoa(len(options)+1) {
			return options, lines[i:], len(options) >= 2
		}
		options = append(options, PermissionOption{ID: match[1], Label: match[2], Tells: strings.HasPrefix(match[2], tellsPrefix)})
	}
	return options, nil, len(options) >= 2
}

func notFooter(l string) bool {
	return l != "" && !strings.HasPrefix(strings.ToLower(l), "esc to")
}

func heading(lines []string) (string, []string, bool) {
	i := len(lines) - 1
	for i >= 0 && lines[i] == "" {
		i--
	}
	end := i
	for i >= 0 && lines[i] != "" {
		i--
	}
	command := slices.Clone(lines[i+1 : end+1])
	for i >= 0 && lines[i] == "" {
		i--
	}
	if i < 0 || len(command) == 0 || (i > 0 && lines[i-1] != "") {
		return "", nil, false
	}
	return lines[i], command, true
}

func fingerprint(item AttentionItem, menu PermissionMenu) string {
	parts := []string{item.Prompt.At.UTC().Format(time.RFC3339Nano), item.Pane.ID, item.Pane.Token, menu.Tool, menu.Title, strings.Join(menu.Command, "\n"), menu.Question}
	for _, o := range menu.Options {
		parts = append(parts, o.ID+" "+o.Label)
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:16])
}

func (m *Runtime) PermissionMenu(item AttentionItem) (PermissionMenu, bool) {
	answerable := item.Pane != nil && !item.Pane.Dead && !item.Pane.InMode && item.Prompt != nil
	if !answerable {
		return PermissionMenu{}, false
	}
	screen, err := m.console().Capture(item.Pane.ID)
	if err != nil {
		return PermissionMenu{}, false
	}
	menu, ok := ParsePermission(screen, item.Prompt.Tool)
	if ok {
		menu.Fingerprint = fingerprint(item, menu)
	}
	return menu, ok
}

func (m *Runtime) livePane(p *tmux.Pane) (*tmux.Pane, bool) {
	if p == nil {
		return nil, false
	}
	now := m.refreshed(Target{Pane: p}).Pane
	return now, now != nil && !now.Dead && !now.InMode && now.Token == p.Token
}

const (
	promptChanged = "The prompt in the session changed, so nothing was typed. Pick again."
	optionGone    = "That option is no longer on the prompt. Nothing was typed."
)

func (m *Runtime) answerPermission(it Item, r ActionRequest) (Receipt, error) {
	item, receipt, ok := m.liveScreenItem(it, r, "permission", "The permission prompt was answered or replaced since it was loaded. Nothing was typed.")
	if !ok {
		return receipt, nil
	}
	menu, ok := m.PermissionMenu(item)
	if !ok || menu.Fingerprint != r.Fingerprint {
		return stale(r, promptChanged), nil
	}
	i := slices.IndexFunc(menu.Options, func(o PermissionOption) bool { return o.ID == r.Option })
	switch {
	case i < 0:
		return stale(r, optionGone), nil
	case menu.Options[i].Tells && strings.TrimSpace(r.Text) == "":
		return Receipt{}, errors.New("say what Claude should do differently first")
	}
	if err := m.console().Press(item.Pane.ID, r.Option); err != nil {
		return refused(r, "Not sent. "+err.Error()), nil
	}
	mark := m.markTyped(it, item, r, ActPermission, "answered a permission prompt from a phone")
	if menu.Options[i].Tells {
		m.tellClaude(item.Pane.ID, mark, r.Text)
	}
	return m.receiptOf(mark), nil
}

func (m *Runtime) answerPrompt(it Item, r ActionRequest) (Receipt, error) {
	item, receipt, ok := m.liveScreenItem(it, r, "prompt", "The prompt was answered or replaced since it was loaded. Nothing was typed.")
	if !ok {
		return receipt, nil
	}
	defer m.rereadSoon(item.Pane.ID)
	menu, ok := m.freshMenu(item.Pane)
	if !ok || menu.Fingerprint != r.Fingerprint {
		return stale(r, promptChanged), nil
	}
	i := slices.IndexFunc(menu.Choices, func(c Choice) bool { return c.ID == r.Option })
	if i < 0 {
		return stale(r, optionGone), nil
	}
	if err := m.choose(item.Pane, menu, menu.Choices[i]); err != nil {
		return refused(r, "Not sent. "+err.Error()), nil
	}
	return m.receiptOf(m.markTyped(it, item, r, ActPrompt, "answered a prompt from a phone")), nil
}

func (m *Runtime) liveScreenItem(it Item, r ActionRequest, kind, replaced string) (AttentionItem, Receipt, bool) {
	item, ok := openItem(it, r.Item, r.Since)
	if !ok || item.Kind != kind {
		return item, stale(r, replaced), false
	}
	if receipt, claimed := m.alreadyClaimed(r); claimed {
		return item, receipt, false
	}
	pane, live := m.livePane(item.Pane)
	if !live {
		return item, stale(r, "The session pane closed, changed or is in copy mode. Nothing was typed."), false
	}
	item.Pane = pane
	return item, Receipt{}, true
}

func (m *Runtime) markTyped(it Item, item AttentionItem, r ActionRequest, action, note string) Answer {
	mark := Answer{At: m.Now(), ItemSince: item.Since, Factory: r.Factory, ItemID: r.Item, Operation: r.Operation, Device: r.Device, Source: SourcePhone, Action: action}
	m.PutClaim(mark)
	m.note(it.Factory, note)
	return mark
}

func (m *Runtime) freshMenu(p *tmux.Pane) (Menu, bool) {
	screen, err := m.console().Capture(p.ID)
	if err != nil {
		return Menu{}, false
	}
	menu, ok := ParseMenu(screen)
	menu.Fingerprint = promptFingerprint(p, menu)
	return menu, ok
}

func (m *Runtime) rereadSoon(pane string) {
	if r, ok := m.readings[pane]; ok {
		r.at = time.Time{}
		m.readings[pane] = r
	}
}

func (m *Runtime) choose(p *tmux.Pane, menu Menu, c Choice) error {
	for _, key := range c.keys {
		if err := m.console().Press(p.ID, key); err != nil {
			return err
		}
	}
	if !menu.numbered() {
		return nil
	}
	time.Sleep(menuSettle)
	if after, ok := m.freshMenu(p); ok && after.Fingerprint == menu.Fingerprint && after.cursor == c.ID {
		return m.console().Press(p.ID, "Enter")
	}
	return nil
}

func (m *Runtime) tellClaude(pane string, mark Answer, text string) {
	if !m.awaitInputPrompt(pane) {
		m.settle(mark.Key(), StatusUnknown, "Claude did not show its input prompt after the refusal, so the instruction was not typed. Look at the session.")
		return
	}
	if err := m.Typist.Type(pane, text); err != nil {
		m.settle(mark.Key(), StatusUnknown, "The instruction could not be typed: "+err.Error())
	}
}

func (m *Runtime) awaitInputPrompt(pane string) bool {
	for range promptPolls {
		if screen, err := m.console().Capture(pane); err == nil && showsInputPrompt(screen) {
			return true
		}
		time.Sleep(promptInterval)
	}
	return false
}

func showsInputPrompt(screen string) bool {
	lines := slices.DeleteFunc(screenLines(screen), func(l string) bool { return l == "" })
	recent := lines[max(0, len(lines)-4):]
	return slices.Contains(recent, ">") && !slices.ContainsFunc(recent, optionLine.MatchString)
}
