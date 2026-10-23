package theme

type Icons bool

const (
	Nerd    Icons = true
	Unicode Icons = false
)

func IconsFor(setting string) Icons {
	return Icons(setting != "unicode")
}

var glyphs = map[string][2]string{
	"app":       {"▣", ""},
	"repo":      {"▪", ""},
	"starting":  {"◌", "\U000F051F"},
	"running":   {"●", ""},
	"needs":     {"◆", "\U000F009E"},
	"ci":        {"⟳", ""},
	"agent":     {"⋯", "\U000F06A9"},
	"stalled":   {"☾", "\U000F04B2"},
	"exited":    {"○", ""},
	"gone":      {"⊘", "\U000F02A0"},
	"done":      {"✓", ""},
	"failstart": {"✕", ""},
	"question":  {"?", ""},
	"gate":      {"⚑", ""},
	"comment":   {"✎", ""},
	"cap":       {"⇥", ""},
	"failure":   {"✕", ""},
	"perm":      {"◈", ""},
	"pr":        {"⇡", ""},
	"file":      {"▤", ""},
	"hist":      {"↺", ""},
	"wake":      {"↯", ""},
	"search":    {"⌕", ""},
	"pass":      {"✓", ""},
	"fail":      {"✗", ""},
	"pending":   {"◌", ""},
	"none":      {"–", ""},
	"ext":       {"↗", ""},
	"branch":    {"⎇", ""},
	"term":      {"›", ""},
	"plus":      {"+", ""},
	"archive":   {"▭", ""},
	"pause":     {"‖", ""},
	"warn":      {"!", ""},
	"info":      {"i", ""},
	"clock":     {"◷", ""},
	"gh":        {"⎇", ""},
	"keys":      {"?", ""},
	"folder":    {"▸", ""},
}

func (i Icons) Glyph(name string) string {
	g, ok := glyphs[name]
	if !ok {
		return "?"
	}
	if i == Nerd {
		return g[1]
	}
	return g[0]
}

func (i Icons) Icon(name string) string {
	return i.Glyph(name) + " "
}
