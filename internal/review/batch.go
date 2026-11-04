package review

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func BatchID(factoryID string, batch int) string {
	return fmt.Sprintf("review-%s-%d", factoryID, batch)
}

func BatchPath(factoryDir string, batch int) string {
	return filepath.Join(factoryDir, "review", fmt.Sprintf("batch-%d.md", batch))
}

func WriteBatch(factoryDir, id, page string, threads []Thread, console []string, batch int) (string, string, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "%s comments: %s\n\n", id, page)
	if !Live(page) {
		fmt.Fprintf(&b, "Page: %s\n\n", filepath.Join(factoryDir, filepath.FromSlash(page)))
	}
	b.WriteString("Act on each thread: change what it is about, or answer the question without changing anything. Then answer every thread with `factory review reply --thread <id> [--resolve] <text>`. Resolve it when you made the change; leave it open when you answered a question or need the developer.\n")
	for _, t := range threads {
		writeThread(&b, factoryDir, t)
	}
	if len(console) > 0 {
		b.WriteString("\n## Browser errors since the last send\n\n")
		for _, line := range console {
			fmt.Fprintf(&b, "- %s\n", oneLine(line, 300))
		}
	}
	path := BatchPath(factoryDir, batch)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", "", err
	}
	return path, b.String(), os.WriteFile(path, []byte(b.String()), 0o644)
}

func writeThread(b *strings.Builder, factoryDir string, t Thread) {
	where := "the whole page"
	if t.Anchor.Selector != "" {
		where = "`" + t.Anchor.Selector + "`"
	}
	fmt.Fprintf(b, "\n## %s · %s · %s\n\n", t.ID, t.Intent, where)
	if t.Anchor.URL != "" && Live(t.Page) {
		fmt.Fprintf(b, "- URL: %s\n", t.Anchor.URL)
	}
	if len(t.Anchor.Components) > 0 {
		fmt.Fprintf(b, "- Components: %s\n", strings.Join(t.Anchor.Components, " < "))
	}
	if t.Anchor.Text != "" {
		fmt.Fprintf(b, "- Text: %q\n", t.Anchor.Text)
	}
	if t.Shot != "" {
		fmt.Fprintf(b, "- Screenshot: %s\n", filepath.Join(factoryDir, "review", filepath.FromSlash(t.Shot)))
	}
	if t.Anchor.HTML != "" {
		fmt.Fprintf(b, "- HTML: `%s`\n", oneLine(t.Anchor.HTML, 600))
	}
	b.WriteString("\n")
	for _, m := range t.Messages {
		fmt.Fprintf(b, "**%s:** %s\n\n", m.By, strings.TrimSpace(m.Text))
	}
}

func oneLine(s string, limit int) string {
	s = strings.Join(strings.Fields(s), " ")
	s = strings.ReplaceAll(s, "`", "'")
	if len(s) > limit {
		s = s[:limit] + "…"
	}
	return s
}
