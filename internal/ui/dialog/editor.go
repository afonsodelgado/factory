package dialog

import (
	"errors"
	"os"
	"os/exec"
	"strings"
	"unicode"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"github.com/rivo/uniseg"

	"github.com/afonsodelgado/factory/internal/ui/theme"
)

type textEditor struct {
	text   string
	cursor int
	goal   int
	aiming bool
	top    int
}

type visualRow struct {
	start, end int
	line       int
	first      bool
}

func wrapRows(text string, width int) []visualRow {
	var rows []visualRow
	offset := 0
	for line, para := range strings.Split(text, "\n") {
		rows = append(rows, wrapLine(para, offset, width, line)...)
		offset += len(para) + 1
	}
	return rows
}

func wrapLine(para string, offset, width, line int) []visualRow {
	var rows []visualRow
	cut := func(start, end int) {
		rows = append(rows, visualRow{start: offset + start, end: offset + end, line: line, first: len(rows) == 0})
	}
	rowStart, rowWidth, afterSpace := 0, 0, -1
	g := uniseg.NewGraphemes(para)
	for g.Next() {
		from, to := g.Positions()
		w := theme.Width(g.Str())
		if rowWidth+w > width && from > rowStart {
			end := from
			if afterSpace > rowStart {
				end = afterSpace
			}
			cut(rowStart, end)
			rowStart, rowWidth, afterSpace = end, theme.Width(para[end:from]), -1
		}
		rowWidth += w
		if g.Str() == " " {
			afterSpace = to
		}
	}
	cut(rowStart, len(para))
	return rows
}

func lastOfLine(rows []visualRow, i int) bool {
	return i == len(rows)-1 || rows[i+1].line != rows[i].line
}

func (e *textEditor) locate(rows []visualRow) (row, col int) {
	for i, r := range rows {
		insideRow := e.cursor >= r.start && (e.cursor < r.end || e.cursor == r.end && lastOfLine(rows, i))
		if insideRow {
			return i, theme.Width(e.text[r.start:e.cursor])
		}
	}
	return len(rows) - 1, 0
}

func (e *textEditor) offsetAt(rows []visualRow, i, col int) int {
	r := rows[i]
	g := uniseg.NewGraphemes(e.text[r.start:r.end])
	w, last := 0, r.start
	for g.Next() {
		from, _ := g.Positions()
		last = r.start + from
		cw := theme.Width(g.Str())
		if w+cw > col {
			return last
		}
		w += cw
	}
	if lastOfLine(rows, i) {
		return r.end
	}
	return last
}

func (e *textEditor) vertical(width, delta int) {
	rows := wrapRows(e.text, width)
	row, col := e.locate(rows)
	if !e.aiming {
		e.goal, e.aiming = col, true
	}
	target := row + delta
	switch {
	case target < 0:
		e.cursor = 0
	case target >= len(rows):
		e.cursor = len(e.text)
	default:
		e.cursor = e.offsetAt(rows, target, e.goal)
	}
}

func (e *textEditor) scroll(row, room, total int) {
	if row < e.top {
		e.top = row
	}
	if row >= e.top+room {
		e.top = row - room + 1
	}
	e.top = max(min(e.top, total-room), 0)
}

func (e *textEditor) insert(s string) {
	e.text = e.text[:e.cursor] + s + e.text[e.cursor:]
	e.cursor += len(s)
}

func (e *textEditor) remove(from, to int) {
	e.text = e.text[:from] + e.text[to:]
	e.cursor = from
}

func (e *textEditor) replace(text string) {
	e.text, e.cursor, e.top = text, len(text), 0
}

func previousGrapheme(s string, i int) int {
	g := uniseg.NewGraphemes(s[:i])
	last := 0
	for g.Next() {
		last, _ = g.Positions()
	}
	return last
}

func nextGrapheme(s string, i int) int {
	cluster, _, _, _ := uniseg.FirstGraphemeClusterInString(s[i:], -1)
	return i + len(cluster)
}

func isWordRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsMark(r) || r == '_'
}

func wordStart(s string, i int) int {
	for _, inWord := range []bool{false, true} {
		for i > 0 {
			r, size := utf8.DecodeLastRuneInString(s[:i])
			if isWordRune(r) != inWord {
				break
			}
			i -= size
		}
	}
	return i
}

func wordEnd(s string, i int) int {
	for _, inWord := range []bool{false, true} {
		for i < len(s) {
			r, size := utf8.DecodeRuneInString(s[i:])
			if isWordRune(r) != inWord {
				break
			}
			i += size
		}
	}
	return i
}

func (e *textEditor) lineStart() int {
	return strings.LastIndexByte(e.text[:e.cursor], '\n') + 1
}

func (e *textEditor) lineEnd() int {
	if i := strings.IndexByte(e.text[e.cursor:], '\n'); i >= 0 {
		return e.cursor + i
	}
	return len(e.text)
}

func (e *textEditor) key(msg tea.KeyPressMsg, width int) {
	key := msg.String()
	switch key {
	case "up", "down", "pgup", "pgdown":
	default:
		e.aiming = false
	}
	switch key {
	case "enter", "ctrl+j", "shift+enter", "alt+enter":
		e.insert("\n")
	case "left", "ctrl+b":
		e.cursor = previousGrapheme(e.text, e.cursor)
	case "right", "ctrl+f":
		e.cursor = nextGrapheme(e.text, e.cursor)
	case "alt+left", "ctrl+left", "alt+b":
		e.cursor = wordStart(e.text, e.cursor)
	case "alt+right", "ctrl+right", "alt+f":
		e.cursor = wordEnd(e.text, e.cursor)
	case "up":
		e.vertical(width, -1)
	case "down":
		e.vertical(width, 1)
	case "pgup":
		e.vertical(width, -10)
	case "pgdown":
		e.vertical(width, 10)
	case "home", "ctrl+a":
		e.cursor = e.lineStart()
	case "end", "ctrl+e":
		e.cursor = e.lineEnd()
	case "ctrl+home", "alt+<":
		e.cursor = 0
	case "ctrl+end", "alt+>":
		e.cursor = len(e.text)
	case "backspace", "ctrl+h":
		e.remove(previousGrapheme(e.text, e.cursor), e.cursor)
	case "delete", "ctrl+d":
		e.remove(e.cursor, nextGrapheme(e.text, e.cursor))
	case "alt+backspace", "ctrl+w":
		e.remove(wordStart(e.text, e.cursor), e.cursor)
	case "alt+delete", "alt+d":
		e.remove(e.cursor, wordEnd(e.text, e.cursor))
	case "ctrl+k":
		e.remove(e.cursor, max(e.lineEnd(), nextGrapheme(e.text, e.cursor)))
	case "ctrl+u":
		e.remove(min(e.lineStart(), previousGrapheme(e.text, e.cursor)), e.cursor)
	default:
		e.insert(DecodeKeys(msg.Text))
	}
}

type promptEdited struct {
	text string
	err  error
}

func externalEditor() string {
	for _, name := range []string{"VISUAL", "EDITOR"} {
		if editor := strings.TrimSpace(os.Getenv(name)); editor != "" {
			return editor
		}
	}
	return "vi"
}

func editExternally(text string) tea.Cmd {
	file, err := os.CreateTemp("", "factory-prompt-*.md")
	if err != nil {
		return func() tea.Msg { return promptEdited{err: err} }
	}
	path := file.Name()
	_, err = file.WriteString(text)
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		os.Remove(path)
		return func() tea.Msg { return promptEdited{err: err} }
	}
	editor := exec.Command("sh", "-c", externalEditor()+` "$1"`, "sh", path)
	return tea.ExecProcess(editor, func(runErr error) tea.Msg {
		defer os.Remove(path)
		data, readErr := os.ReadFile(path)
		edited := strings.ReplaceAll(string(data), "\r\n", "\n")
		if !strings.HasSuffix(text, "\n") {
			edited = strings.TrimSuffix(edited, "\n")
		}
		return promptEdited{text: edited, err: errors.Join(runErr, readErr)}
	})
}
