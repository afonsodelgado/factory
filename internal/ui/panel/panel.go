package panel

import (
	"fmt"
	"strings"
	"time"

	"github.com/afonsodelgado/factory/internal/ui/theme"
)

const ListWidth = 30

type line []theme.Span

func text(s string, ink theme.Ink) line {
	return line{{Text: s, Ink: ink}}
}

func blank() line {
	return nil
}

var kindLook = map[string]struct {
	icon string
	ink  theme.Ink
	word string
}{
	"question":   {"question", theme.FgInk(theme.Orange), "question"},
	"gate":       {"gate", theme.FgInk(theme.Orange), "gate"},
	"comment":    {"comment", theme.FgInk(theme.Cyan), "comment"},
	"cap":        {"cap", theme.FgInk(theme.Yellow), "cap tripped"},
	"failure":    {"failure", theme.FgInk(theme.Red), "failure"},
	"handoff":    {"done", theme.FgInk(theme.Green), "work done"},
	"permission": {"perm", theme.FgInk(theme.Orange), "permission"},
	"unreadable": {"warn", theme.FgInk(theme.Red), "unreadable"},
}

func look(it Item) (string, theme.Ink, string) {
	if it.Kind == "asleep" {
		return theme.StatusIcon(it.Status), theme.StatusInk(it.Status), "asleep"
	}
	l, ok := kindLook[it.Kind]
	if !ok {
		return "info", theme.Quiet, it.Kind
	}
	return l.icon, l.ink, l.word
}

func Render(v View, ic theme.Icons, w, h int) string {
	c := theme.NewCanvas(w, h)
	c.Dim = v.Dim
	if w < 10 || h < 1 {
		return c.Render()
	}
	if v.Collapsed || h < 3 {
		renderFolded(c, v, ic)
		return c.Render()
	}
	border := theme.Line
	if v.Focused {
		border = theme.Focused
	}
	title := append(line{{Text: "─", Ink: border}}, tabs(v, ic)...)
	c.Box(0, 0, w, h, theme.Box{Ink: border, Title: title, TitleRight: label(v.Label, w-2-theme.SpansWidth(title)-1)})
	x, y, iw, ih := 2, 1, w-4, h-2
	switch v.Tab {
	case Attention:
		renderAttention(c, v, ic, x, y, iw, ih)
	case PRs:
		renderPRs(c, v, ic, x, y, iw, ih)
	case Artifacts:
		renderArtifacts(c, v, ic, x, y, iw, ih)
	case Activity:
		renderActivity(c, v, ic, x, y, iw, ih)
	}
	return c.Render()
}

func tabs(v View, ic theme.Icons) line {
	names := []struct {
		tab  Tab
		icon string
		name string
	}{{Attention, "needs", "Attention"}, {PRs, "pr", "PRs"}, {Artifacts, "file", "Artifacts"}, {Activity, "hist", "Activity"}}
	var spans line
	for i, t := range names {
		label := " " + ic.Icon(t.icon) + t.name
		if int(t.tab) < len(v.Counts) {
			label += fmt.Sprintf(" %d", v.Counts[t.tab])
		}
		label += " "
		ink := theme.Quiet
		switch {
		case t.tab == v.Tab && v.Focused && !v.Collapsed:
			ink = theme.Ink{FG: theme.Black, BG: theme.Blue, Bold: true}
		case t.tab == v.Tab && !v.Collapsed:
			ink = theme.Ink{FG: theme.Hi, BG: theme.Selection, Bold: true}
		case t.tab == Attention && v.Counts[Attention] > 0:
			ink = theme.FgInk(theme.Orange)
		}
		if i > 0 {
			spans = append(spans, theme.Span{Text: "─", Ink: theme.Line})
		}
		spans = append(spans, theme.Span{Text: label, Ink: ink})
	}
	return spans
}

func renderFolded(c *theme.Canvas, v View, ic theme.Icons) {
	c.HLine(0, 0, c.W, theme.Line)
	left := tabs(v, ic)
	c.Spans(2, 0, left)
	expand := line{{Text: "─", Ink: theme.Line}, {Text: " z", Ink: theme.FgInk(theme.Cyan)}, {Text: " expand ", Ink: theme.Muted}}
	right := append(label(v.Label, c.W-4-theme.SpansWidth(left)-theme.SpansWidth(expand)-1), expand...)
	c.Spans(c.W-2-theme.SpansWidth(right), 0, right)
}

func label(text string, room int) line {
	fitted := theme.KeepTail(text, room-2)
	if fitted == "" {
		return nil
	}
	return line{{Text: " " + fitted + " ", Ink: theme.Muted}}
}

func place(c *theme.Canvas, x, y, w int, l line) {
	for _, s := range l {
		if w <= 0 {
			return
		}
		t := theme.Truncate(s.Text, w)
		x = c.Put(x, y, t, s.Ink)
		w -= theme.Width(t)
	}
}

const formRows = 3

func block(c *theme.Canvas, x, y, w, h int, top, middle, bottom []line, focus int) {
	keep := min(len(middle), formRows)
	bottom = bottom[max(0, len(bottom)-max(h-1-keep, 0)):]
	top = top[:max(1, min(len(top), h-len(bottom)-keep))]
	for i, l := range top {
		if i < h {
			place(c, x, y+i, w, l)
		}
	}
	room := h - len(top) - len(bottom)
	start := 0
	if focus >= room && room > 0 {
		start = focus - room + 1
	}
	for i := 0; i < room && start+i < len(middle); i++ {
		place(c, x, y+len(top)+i, w, middle[start+i])
	}
	for i, l := range bottom {
		if row := h - len(bottom) + i; row >= 0 {
			place(c, x, y+row, w, l)
		}
	}
}

func hints(pairs ...string) line {
	var l line
	for i := 0; i+1 < len(pairs); i += 2 {
		if i > 0 {
			l = append(l, theme.Span{Text: "   "})
		}
		l = append(l, theme.Span{Text: pairs[i], Ink: theme.Key}, theme.Span{Text: " " + pairs[i+1], Ink: theme.Text})
	}
	return l
}

func ago(now, then time.Time) string {
	if then.IsZero() {
		return ""
	}
	return theme.Age(now.Sub(then))
}

func renderAttention(c *theme.Canvas, v View, ic theme.Icons, x, y, w, h int) {
	if len(v.Items) == 0 && len(v.Settled) > 0 {
		for i, s := range v.Settled {
			c.Spans(x, y+i, settledLine(s))
		}
		y = paragraph(c, x, y+len(v.Settled)+1, w, "Nothing else needs you here. The supervisor closes each item once it has acted on it.")
		nextLine(c, v, x, y+1)
		return
	}
	if len(v.Items) == 0 {
		c.Put(x, y, "Nothing needs you here.", theme.Bright)
		y = paragraph(c, x, y+2, w, "Open items appear here when the supervisor asks, a gate lights, a cap trips, a stage fails twice, or the session waits on a permission prompt.")
		nextLine(c, v, x, y+1)
		return
	}
	renderList(c, v, ic, x, y, h)
	c.VLine(x+ListWidth+1, y, h, theme.Line)
	dx, dw := x+ListWidth+3, w-ListWidth-3
	it := v.Items[max(0, min(v.Item, len(v.Items)-1))]
	top, middle, bottom, focus := detail(v, it, ic, dw, h)
	block(c, dx, y, dw, h, top, middle, bottom, focus)
}

func renderList(c *theme.Canvas, v View, ic theme.Icons, x, y, h int) {
	r := y
	for i, it := range v.Items {
		if r >= y+h {
			return
		}
		on := i == v.Item
		icon, ink, word := look(it)
		if on {
			bg := theme.Highlight
			if v.Focused {
				bg = theme.Selection
			}
			c.Fill(x, r, ListWidth, 2, bg)
			c.Put(x, r, "❯", theme.Focused.B())
		}
		c.Put(x+2, r, ic.Icon(icon), ink)
		c.Put(x+4, r, word, ink.B())
		c.Right(x+ListWidth-1, r, ago(v.Now, it.Since), theme.Muted)
		sub := theme.Text
		if on {
			sub = theme.Bright
		}
		c.PutMax(x+4, r+1, it.Title, sub, ListWidth-5)
		r += 2
		if it.Kind == "question" && on {
			for qi, q := range it.Questions {
				mark, markInk := questionMark(v.Draft, q, qi)
				c.Put(x+4, r, mark, markInk)
				c.PutMax(x+6, r, fmt.Sprintf("%d %s", qi+1, q.Text), theme.Text, ListWidth-7)
				r++
			}
		}
		r++
	}
	for _, s := range v.Settled {
		if r >= y+h {
			return
		}
		place(c, x+2, r, ListWidth-2, settledLine(s))
		r++
	}
}

func paragraph(c *theme.Canvas, x, y, w int, text string) int {
	for _, l := range theme.Wrap(text, w) {
		c.Put(x, y, l, theme.Muted)
		y++
	}
	return y
}

func nextLine(c *theme.Canvas, v View, x, y int) {
	if v.Next == "" {
		return
	}
	c.Spans(x, y, line{{Text: "⏎", Ink: theme.Key}, {Text: " next: ", Ink: theme.Text}, {Text: v.Next, Ink: theme.Bright.B()}, {Text: " needs you", Ink: theme.FgInk(theme.Orange)}})
}

func settledLine(s Settled) line {
	switch s.State {
	case Queued:
		return line{{Text: "◷ Queued " + s.ID, Ink: theme.FgInk(theme.Cyan)}, {Text: "  sends when the supervisor is idle", Ink: theme.Muted}}
	case Sending:
		return line{{Text: "… Sending " + s.ID, Ink: theme.Muted}}
	}
	return line{{Text: "✓ Answered " + s.ID, Ink: theme.FgInk(theme.Green)}, {Text: "  the supervisor is on it", Ink: theme.Muted}}
}

func header(it Item, ic theme.Icons, v View) line {
	icon, ink, word := look(it)
	l := line{{Text: ic.Icon(icon), Ink: ink}, {Text: word + " ", Ink: ink.B()}}
	if it.ID != "" {
		l = append(l, theme.Span{Text: it.ID + "  ", Ink: theme.Bright.B()})
	}
	if meta := ago(v.Now, it.Since); meta != "" {
		l = append(l, theme.Span{Text: it.Since.Local().Format("15:04") + " · " + meta, Ink: theme.Muted})
	}
	return l
}

func wrapped(s string, w int, ink theme.Ink) []line {
	var lines []line
	for _, l := range theme.Wrap(s, w) {
		lines = append(lines, text(l, ink))
	}
	return lines
}

const roomyPanel = 14

func detail(v View, it Item, ic theme.Icons, w, h int) ([]line, []line, []line, int) {
	field := textBox
	if h < roomyPanel {
		field = textLine
	}
	top := []line{header(it, ic, v)}
	top = append(top, wrapped(it.Body, w, theme.Text)...)
	switch {
	case TextAnswered(it):
		middle := freeText(v, TextPrompt(it.Kind), w)
		if middle == nil {
			middle = []line{blank(), hints("type", TextPrompt(it.Kind)+", then ctrl+s sends it")}
		}
		return top, middle, questionFooter(v, w), 0
	case it.Kind == "question":
		middle, focus := questionForm(v, it, w)
		return top, middle, questionFooter(v, w), focus
	case it.Kind == "gate":
		return top, gateForm(v, it, ic, w), withStatus(v, w, field("notes", v.Draft, w)), 1 + v.Draft.Artifact
	case it.Kind == "comment":
		top = append(top, text("Your reply goes to thread "+it.ThreadID+".", theme.Muted))
		return top, nil, withStatus(v, w, field("reply", v.Draft, w)), 0
	case it.Kind == "cap":
		return top, append(choices(v, "ctrl+r", "continue one more round", "raises "+it.LimitKey+" by one for this factory"), freeText(v, "instruction", w)...), withStatus(v, w), 0
	case it.Kind == "failure":
		return top, append(choices(v, "ctrl+r", "retry", "sends /factory resume"), freeText(v, "instruction", w)...), withStatus(v, w), 0
	case it.Kind == "handoff":
		return top, append(choices(v, "ctrl+k", "mark done", "closes the factory, never merges", "ctrl+s", "continue working", "sends what you typed"), freeText(v, "instruction", w)...), withStatus(v, w), 0
	}
	var middle []line
	middle = append(middle, blank())
	for _, d := range it.Detail {
		middle = append(middle, text(d, theme.Text))
	}
	var bottom []line
	if it.Action != "" {
		bottom = append(bottom, text(it.Action, theme.Key))
	}
	return top, middle, bottom, 0
}

func questionForm(v View, it Item, w int) ([]line, int) {
	lines := []line{blank()}
	focus := 0
	d := v.Draft
	for qi, q := range it.Questions {
		if qi != d.Question {
			lines = append(lines, foldedQuestion(d, q, qi, w))
			continue
		}
		if qi > 0 {
			lines = append(lines, blank())
		}
		number := theme.Bright
		if v.Focused {
			number = theme.Focused.B()
		}
		for i, ql := range theme.Wrap(q.Text, w-3) {
			lead := "   "
			if i == 0 {
				lead = fmt.Sprintf("%-3d", qi+1)
			}
			lines = append(lines, line{{Text: lead, Ink: number}, {Text: ql, Ink: theme.Bright}})
		}
		current := -1
		if qi < len(d.Choice) {
			current = d.Choice[qi]
		}
		if current < 0 {
			current = q.RecommendedIndex()
		}
		if q.TopRow() < 0 {
			mark, ink := "○ ", theme.Text
			if current < 0 {
				mark, ink = "● ", theme.Bright
			}
			if d.Cursor < 0 {
				focus = len(lines)
			}
			lines = append(lines, line{cursor(v, d.Cursor < 0), {Text: mark + q.Recommended, Ink: ink}, {Text: "  recommended", Ink: theme.Muted}})
		}
		for oi, o := range q.Options {
			mark, ink := "○ ", theme.Text
			if oi == current {
				mark, ink = "● ", theme.Bright
			}
			if oi == d.Cursor {
				focus = len(lines)
			}
			l := line{cursor(v, oi == d.Cursor), {Text: mark + o, Ink: ink}}
			if o == q.Recommended {
				l = append(l, theme.Span{Text: "  recommended", Ink: theme.Muted})
			}
			lines = append(lines, l)
		}
		onTypeRow := d.Cursor == len(q.Options)
		if onTypeRow {
			focus = len(lines)
		}
		if d.Typing(q, qi) {
			box := d
			box.Text = d.Typed[qi]
			box.Editing = d.Editing
			for i, bl := range textBox("your answer", box, w-3) {
				lines = append(lines, append(line{cursor(v, onTypeRow && i == 0)}, bl...))
			}
		} else {
			lines = append(lines, line{cursor(v, onTypeRow), {Text: "○ type an answer", Ink: theme.Muted}})
		}
		lines = append(lines, blank())
	}
	onSend := d.Question == len(it.Questions)
	if d.Question != len(it.Questions)-1 {
		lines = append(lines, blank())
	}
	send := theme.Muted
	if onSend {
		focus = len(lines)
		send = theme.Bright.B()
	}
	lines = append(lines, line{cursor(v, onSend), {Text: "send answers", Ink: send}})
	return lines, focus
}

func cursor(v View, on bool) theme.Span {
	if on && v.Focused {
		return theme.Span{Text: " ❯ ", Ink: theme.Focused.B()}
	}
	return theme.Span{Text: "   "}
}

func foldedQuestion(d Draft, q Question, qi, w int) line {
	mark, ink := questionMark(d, q, qi)
	answer := ""
	if shown := shownAnswer(d, q, qi); shown != "" {
		answer = "  → " + shown
	}
	room := max(w-5-theme.Width(answer), (w-5)/2)
	return line{{Text: fmt.Sprintf("%-3d", qi+1), Ink: theme.Text}, {Text: mark + " ", Ink: ink}, {Text: theme.Truncate(q.Text, room), Ink: theme.Text}, {Text: answer, Ink: theme.Muted}}
}

func questionMark(d Draft, q Question, qi int) (string, theme.Ink) {
	switch {
	case d.Typing(q, qi):
		return "✎", theme.FgInk(theme.Cyan)
	case !d.Answered(q, qi):
		return "○", theme.FgInk(theme.Orange)
	}
	return "✓", theme.FgInk(theme.Green)
}

func shownAnswer(d Draft, q Question, qi int) string {
	switch {
	case d.Typing(q, qi):
		return d.Typed[qi]
	case qi < len(d.Choice) && d.Choice[qi] >= 0:
		return q.Options[d.Choice[qi]]
	}
	return q.Recommended
}

func textBox(title string, d Draft, w int) []line {
	border := theme.Line
	if d.Editing {
		border = theme.Focused
	}
	inner := max(w-4, 4)
	head := "─ " + theme.Truncate(title, w-5) + " "
	lines := []line{{{Text: "╭", Ink: border}, {Text: head, Ink: border}, {Text: strings.Repeat("─", max(w-2-theme.Width(head), 0)) + "╮", Ink: border}}}
	body := theme.Wrap(d.Text, inner)
	if d.Text == "" {
		body = []string{""}
	}
	for i, bl := range body {
		if d.Editing && i == len(body)-1 {
			bl += "█"
		}
		lines = append(lines, line{{Text: "│ ", Ink: border}, {Text: padTo(bl, inner), Ink: theme.Bright}, {Text: " │", Ink: border}})
	}
	lines = append(lines, line{{Text: "╰" + strings.Repeat("─", max(w-2, 0)) + "╯", Ink: border}})
	return lines
}

func textLine(title string, d Draft, w int) []line {
	label := title + "  "
	shown, ink := strings.ReplaceAll(d.Text, "\n", " · "), theme.Bright
	if d.Editing {
		shown += "█"
	}
	if shown == "" {
		shown, ink = "type to add "+title, theme.Muted
	}
	return []line{{{Text: label, Ink: theme.Quiet}, {Text: theme.KeepTail(shown, w-theme.Width(label)), Ink: ink}}}
}

func padTo(s string, w int) string {
	s = theme.Truncate(s, w)
	return s + strings.Repeat(" ", max(w-theme.Width(s), 0))
}

func withStatus(v View, w int, blocks ...[]line) []line {
	var lines []line
	for _, b := range blocks {
		lines = append(lines, b...)
	}
	return append(lines, sendStatus(v, w)...)
}

func sendStatus(v View, w int) []line {
	lines := []line{text(strings.Repeat("─", max(w, 0)), theme.Line)}
	switch {
	case v.Send.Refused != "":
		return append(lines, warning(v.Send.Refused+" Draft kept.", w)...)
	case v.Send.Ready:
		return append(lines, text("✓ "+v.Send.Reason, theme.FgInk(theme.Green)))
	}
	return append(lines, warning(v.Send.Reason, w)...)
}

func questionFooter(v View, w int) []line {
	lines := sendStatus(v, w)
	switch {
	case v.Send.Refused != "" || v.Send.Preview == "":
		return lines
	}
	return append(lines, line{{Text: "preview  ", Ink: theme.Quiet}, {Text: strings.ReplaceAll(v.Send.Preview, "\n", " · "), Ink: theme.Text}})
}

func warning(s string, w int) []line {
	var lines []line
	for i, r := range theme.Wrap(s, w-2) {
		lead := "  "
		if i == 0 {
			lead = "! "
		}
		lines = append(lines, line{{Text: lead, Ink: theme.FgInk(theme.Yellow).B()}, {Text: r, Ink: theme.FgInk(theme.Yellow)}})
	}
	return lines
}

func gateForm(v View, it Item, ic theme.Icons, w int) []line {
	lines := []line{blank()}
	for i, a := range it.Artifacts {
		cursor := "  "
		if i == v.Draft.Artifact {
			cursor = "❯ "
		}
		l := line{{Text: cursor, Ink: theme.Focused.B()}, {Text: ic.Icon("file"), Ink: theme.Focused}, {Text: a.Title + "  ", Ink: theme.Bright}, {Text: shortURL(a.URL), Ink: theme.Muted}}
		if a.OpenThreads != nil {
			l = append(l, theme.Span{Text: fmt.Sprintf("  ✎ %d", *a.OpenThreads), Ink: theme.FgInk(theme.Cyan)})
		}
		lines = append(lines, l)
	}
	cursor := "  "
	if v.Draft.Artifact == len(it.Artifacts) {
		cursor = "❯ "
	}
	lines = append(lines, line{{Text: cursor, Ink: theme.Focused.B()}, {Text: "✓ Approve", Ink: theme.FgInk(theme.Green).B()}, {Text: "  sends /factory approve " + it.Stage + " with the notes", Ink: theme.Muted}})
	checked := checkedAt(it.Artifacts)
	if !checked.IsZero() {
		lines = append(lines, blank(), line{{Text: "Open threads  ", Ink: theme.Bright}, {Text: "from state.json, checked " + ago(v.Now, checked) + " ago", Ink: theme.Muted}})
	}
	return lines
}

func checkedAt(rows []ArtifactRow) time.Time {
	var latest time.Time
	for _, r := range rows {
		if r.CheckedAt.After(latest) {
			latest = r.CheckedAt
		}
	}
	return latest
}

func shortURL(url string) string {
	if i := strings.LastIndex(url, "/"); i >= 0 && len(url) > 40 {
		return "…" + url[i:]
	}
	return url
}

func freeText(v View, title string, w int) []line {
	if v.Draft.Text == "" && !v.Draft.Editing {
		return nil
	}
	return append([]line{blank()}, textBox(title, v.Draft, w)...)
}

func choices(v View, options ...string) []line {
	lines := []line{blank()}
	for i := 0; i < len(options); i += 3 {
		lines = append(lines, append(hints(options[i], options[i+1]), theme.Span{Text: "  " + options[i+2], Ink: theme.Muted}))
	}
	return lines
}

func renderPRs(c *theme.Canvas, v View, ic theme.Icons, x, y, w, h int) {
	c.Put(x, y, "Stack in merge order, bottom first", theme.Bright)
	if !v.Checked.IsZero() {
		c.Right(x+w-1, y, ic.Icon("gh")+"gh checked "+ago(v.Now, v.Checked)+" ago · refreshes every 60s", theme.Muted)
	}
	if len(v.PRs) == 0 {
		c.Put(x, y+2, "No PRs yet. They appear during the PR stage.", theme.Muted)
		return
	}
	c.Put(x+2, y+2, "#  PR      title", theme.Quiet)
	c.Right(x+w-1, y+2, "state   CI         checks                ", theme.Quiet)
	titleWidth := w - 2 - 11 - 42 - 1
	for i, pr := range v.PRs {
		r := y + 3 + i
		if r >= y+h-1 {
			break
		}
		if i == v.Row {
			c.Put(x, r, "❯", theme.Focused.B())
		}
		c.Put(x+2, r, fmt.Sprintf("%-2d #%-6d", i+1, pr.Number), theme.Text)
		c.PutMax(x+13, r, pr.Title, theme.Bright, titleWidth)
		state, ci, checks := "ready", ciLabel(pr, ic), checkCount(pr)
		if pr.Draft {
			state = "draft"
		}
		col := x + w - 42
		c.Put(col, r, state, theme.Quiet)
		c.Spans(col+8, r, ci)
		c.Put(col+19, r, checks, theme.Muted)
	}
	place(c, x, y+h-1, w, hints("o", "open in browser", "r", "refresh now", "tab", "next tab"))
}

func ciLabel(pr PR, ic theme.Icons) line {
	switch {
	case !pr.Loaded:
		return line{{Text: ic.Icon("pending") + "…", Ink: theme.Muted}}
	case pr.CI == "pass":
		return line{{Text: ic.Icon("pass") + "pass", Ink: theme.FgInk(theme.Green)}}
	case pr.CI == "fail":
		return line{{Text: ic.Icon("fail") + "fail", Ink: theme.FgInk(theme.Red)}}
	case pr.CI == "pending":
		return line{{Text: ic.Icon("pending") + "pending", Ink: theme.FgInk(theme.Yellow)}}
	}
	return line{{Text: ic.Icon("none") + "none", Ink: theme.Muted}}
}

func checkCount(pr PR) string {
	switch {
	case pr.Problem != "":
		return pr.Problem
	case pr.Total == 0:
		return ""
	case pr.Done == pr.Total:
		return fmt.Sprintf("%d/%d checks", pr.Done, pr.Total)
	}
	return fmt.Sprintf("%d of %d checks done", pr.Done, pr.Total)
}

func renderArtifacts(c *theme.Canvas, v View, ic theme.Icons, x, y, w, h int) {
	c.Put(x, y, "Design and plan artifacts", theme.Bright)
	c.Right(x+w-1, y, "open-thread counts come from the supervisor's comment watch", theme.Muted)
	if len(v.Artifacts) == 0 {
		c.Put(x, y+2, "No artifacts published yet.", theme.Muted)
		return
	}
	c.Put(x+5, y+2, "artifact          title                          link", theme.Quiet)
	c.Right(x+w-1, y+2, "open   checked", theme.Quiet)
	linkWidth := max(w-5-18-31-16, 10)
	for i, a := range v.Artifacts {
		r := y + 3 + i
		if r >= y+h-1 {
			break
		}
		if i == v.Row {
			c.Put(x, r, "❯", theme.Focused.B())
		}
		c.Put(x+3, r, ic.Icon("file"), theme.Focused)
		c.PutMax(x+5, r, a.Name, theme.Bright, 17)
		c.PutMax(x+23, r, a.Title, theme.Text, 30)
		c.PutMax(x+54, r, strings.TrimPrefix(a.URL, "https://"), theme.Muted, linkWidth)
		count := "–"
		if a.OpenThreads != nil {
			count = fmt.Sprintf("✎ %d", *a.OpenThreads)
		}
		c.Put(x+w-14, r, count, theme.FgInk(theme.Cyan))
		checked := checkedLabel(v.Now, a.CheckedAt)
		if a.Live {
			checked = "live"
		}
		c.Right(x+w-1, r, checked, theme.Muted)
	}
	place(c, x, y+h-1, w, hints("o", "open in browser", "tab", "next tab"))
}

func checkedLabel(now, at time.Time) string {
	if at.IsZero() {
		return "never"
	}
	return ago(now, at) + " ago"
}

func renderActivity(c *theme.Canvas, v View, ic theme.Icons, x, y, w, h int) {
	title, toggle := v.Label+", newest first", "t all factories"
	if v.All {
		title, toggle = "All factories, newest first", "t this factory only"
	}
	c.Put(x, y, title, theme.Bright)
	c.Right(x+w-1, y, toggle, theme.Muted)
	rows := h - 3
	start := max(0, min(v.Row-rows+1, len(v.Activity)-rows))
	for i := 0; i < rows && start+i < len(v.Activity); i++ {
		a := v.Activity[start+i]
		r := y + 2 + i
		if start+i == v.Row {
			c.Put(x-1, r, "❯", theme.Focused.B())
		}
		col := x
		c.Put(col, r, a.At.Local().Format("15:04"), theme.Muted)
		col += 7
		if v.All {
			c.PutMax(col, r, a.Factory, theme.Bright, 18)
			col += 19
		}
		c.PutMax(col, r, a.Stage, theme.Quiet, 13)
		col += 14
		c.Right(x+w-1, r, a.Source, theme.Muted)
		c.PutMax(col, r, a.Text, theme.Text, x+w-1-col-7)
	}
	place(c, x, y+h-1, w, hints("j/k", "scroll", "t", strings.TrimPrefix(toggle, "t "), "⏎", "jump to factory"))
}
