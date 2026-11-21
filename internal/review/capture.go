package review

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
)

const (
	ReferenceDir = "design/reference"
	TokensFile   = "tokens.json"
)

type Viewport struct {
	W int `json:"w"`
	H int `json:"h"`
}

func (v Viewport) String() string { return fmt.Sprintf("%dx%d", v.W, v.H) }

func ParseViewport(s string) (Viewport, error) {
	var v Viewport
	if _, err := fmt.Sscanf(s, "%dx%d", &v.W, &v.H); err != nil || v.W <= 0 || v.H <= 0 || v.String() != s {
		return Viewport{}, fmt.Errorf("viewport %q is not WIDTHxHEIGHT, such as 1440x900", s)
	}
	return v, nil
}

var DefaultViewports = []Viewport{{1440, 900}, {1024, 768}, {390, 844}}

type CaptureRequest struct {
	Dir       string     `json:"dir"`
	Source    string     `json:"source"`
	Entry     string     `json:"entry,omitempty"`
	URL       string     `json:"url,omitempty"`
	Screens   []string   `json:"screens"`
	Viewports []Viewport `json:"viewports"`
}

type CaptureResult struct {
	SHA256 string   `json:"sha256"`
	Frozen string   `json:"frozen"`
	Files  []string `json:"files"`
}

type Box struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
	W float64 `json:"w"`
	H float64 `json:"h"`
}

type ElementTokens struct {
	Box   Box               `json:"box"`
	Style map[string]string `json:"style"`
}

type ScreenTokens struct {
	Source   string                   `json:"source"`
	PNG      string                   `json:"png"`
	Box      Box                      `json:"box"`
	Elements map[string]ElementTokens `json:"elements"`
}

type TokenSource struct {
	Kind   string `json:"kind"`
	Ref    string `json:"ref"`
	SHA256 string `json:"sha256"`
}

type SourceTokens struct {
	Kind   string            `json:"kind"`
	SHA256 string            `json:"sha256"`
	Root   map[string]string `json:"root"`
	Fonts  []string          `json:"fonts"`
}

type Tokens struct {
	Source  TokenSource                        `json:"source"`
	Root    map[string]string                  `json:"root"`
	Fonts   []string                           `json:"fonts"`
	Sources map[string]SourceTokens            `json:"sources"`
	Screens map[string]map[string]ScreenTokens `json:"screens"`
}

type reading struct {
	Root    map[string]string `json:"root"`
	Fonts   []string          `json:"fonts"`
	Missing []string          `json:"missing"`
	Screens map[string]struct {
		Box      Box                      `json:"box"`
		Elements map[string]ElementTokens `json:"elements"`
	} `json:"screens"`
}

func Capture(ctx context.Context, chrome string, noSandbox bool, r CaptureRequest) (CaptureResult, error) {
	for _, id := range r.Screens {
		if filepath.Base(id) != id || !filepath.IsLocal(id) {
			return CaptureResult{}, fmt.Errorf("screen %q is not a plain id; name each section's id, such as list or state-empty", id)
		}
	}
	refDir := filepath.Join(r.Dir, filepath.FromSlash(ReferenceDir))
	tokens, err := readTokens(filepath.Join(refDir, TokensFile))
	if err != nil {
		return CaptureResult{}, err
	}
	if err := os.MkdirAll(refDir, 0o755); err != nil {
		return CaptureResult{}, err
	}
	staging, err := os.MkdirTemp(refDir, ".capture-")
	if err != nil {
		return CaptureResult{}, err
	}
	defer os.RemoveAll(staging)
	staged, err := freeze(r, filepath.Join(staging, "source"))
	if err != nil {
		return CaptureResult{}, err
	}
	sum, err := fileSHA256(staged)
	if err != nil {
		return CaptureResult{}, err
	}
	target, kind := r.URL, Prototype
	if r.Source != Prototype {
		target, kind = (&url.URL{Scheme: "file", Path: staged}).String(), "design"
	}
	profile, err := os.MkdirTemp("", "factory-capture-")
	if err != nil {
		return CaptureResult{}, err
	}
	h, err := LaunchHeadless(profile, chrome, noSandbox)
	if err != nil {
		os.RemoveAll(profile)
		return CaptureResult{}, err
	}
	defer h.Close()
	inStaging, _ := filepath.Rel(staging, staged)
	frozen := filepath.Join(refDir, inStaging)
	rel, _ := filepath.Rel(r.Dir, frozen)
	tokens.Source = TokenSource{Kind: kind, Ref: filepath.ToSlash(rel), SHA256: sum}
	for _, v := range r.Viewports {
		if err := h.captureViewport(ctx, target, v, r.Screens, staging, &tokens); err != nil {
			return CaptureResult{}, fmt.Errorf("rendering %s at %s: %w", r.Source, v, err)
		}
	}
	tokens.Sources[tokens.Source.Ref] = SourceTokens{Kind: kind, SHA256: sum, Root: tokens.Root, Fonts: tokens.Fonts}
	files, err := promote(staging, refDir)
	if err != nil {
		return CaptureResult{}, err
	}
	data, err := json.MarshalIndent(tokens, "", "  ")
	if err != nil {
		return CaptureResult{}, err
	}
	tokensPath := filepath.Join(refDir, TokensFile)
	return CaptureResult{SHA256: sum, Frozen: frozen, Files: append(files, tokensPath)}, os.WriteFile(tokensPath, data, 0o644)
}

func promote(staging, refDir string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(staging, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(staging, path)
		to := filepath.Join(refDir, rel)
		if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
			return err
		}
		files = append(files, to)
		return os.Rename(path, to)
	})
	return files, err
}

func (h *Headless) captureViewport(ctx context.Context, target string, v Viewport, screens []string, dir string, tokens *Tokens) error {
	run, cancel := context.WithCancel(h.ctx)
	defer cancel()
	defer context.AfterFunc(ctx, cancel)()
	if err := chromedp.Do(run, chromedp.EmulateViewport(int64(v.W), int64(v.H)), chromedp.Navigate(target)); err != nil {
		return err
	}
	read, err := chromedp.Run(run, chromedp.Evaluate[reading](measureScript(screens), chromedp.EvalAwaitPromise))
	if err != nil {
		return err
	}
	if len(read.Missing) > 0 {
		return fmt.Errorf("no element with id %s", strings.Join(read.Missing, ", "))
	}
	tokens.Root, tokens.Fonts = read.Root, read.Fonts
	for _, id := range screens {
		screen := read.Screens[id]
		beyond := true
		clip := &page.Viewport{X: screen.Box.X, Y: screen.Box.Y, Width: screen.Box.W, Height: screen.Box.H, Scale: 1}
		shot, err := chromedp.Call(run, page.CaptureScreenshot, page.CaptureScreenshotParams{Format: page.CaptureScreenshotFormatPng, Clip: clip, CaptureBeyondViewport: &beyond})
		if err != nil {
			return err
		}
		name := id + "@" + v.String() + ".png"
		if err := os.WriteFile(filepath.Join(dir, name), shot.Data, 0o644); err != nil {
			return err
		}
		if tokens.Screens[id] == nil {
			tokens.Screens[id] = map[string]ScreenTokens{}
		}
		tokens.Screens[id][v.String()] = ScreenTokens{Source: tokens.Source.Ref, PNG: name, Box: screen.Box, Elements: screen.Elements}
	}
	return nil
}

func measureScript(screens []string) string {
	ids, _ := json.Marshal(screens)
	return `(async () => {
  await document.fonts.ready;
  window.__factoryReviewHide && window.__factoryReviewHide(true);
  const keys = ["fontFamily", "fontSize", "fontWeight", "lineHeight", "color", "backgroundColor", "padding", "gap", "borderRadius", "width", "height"];
  const style = (el) => { const s = getComputedStyle(el); return Object.fromEntries(keys.map((k) => [k, s[k]])); };
  const root = {};
  const computed = getComputedStyle(document.documentElement);
  for (const sheet of document.styleSheets) {
    let rules = [];
    try { rules = [...sheet.cssRules]; } catch {}
    for (const rule of rules) {
      if (rule.selectorText !== ":root") continue;
      for (const name of rule.style) if (name.startsWith("--")) root[name] = computed.getPropertyValue(name).trim();
    }
  }
  const fonts = [...new Set([...document.fonts].filter((f) => f.status === "loaded").map((f) => f.family.replace(/^["']|["']$/g, "")))];
  const screens = {}, missing = [];
  for (const id of ` + string(ids) + `) {
    const el = document.getElementById(id);
    if (!el) { missing.push("#" + id); continue; }
    const r = el.getBoundingClientRect();
    const elements = { [id]: { box: { x: 0, y: 0, w: r.width, h: r.height }, style: style(el) } };
    for (const d of el.querySelectorAll("[data-ref]")) {
      const b = d.getBoundingClientRect();
      elements[d.dataset.ref] = { box: { x: b.x - r.x, y: b.y - r.y, w: b.width, h: b.height }, style: style(d) };
    }
    screens[id] = { box: { x: r.x + scrollX, y: r.y + scrollY, w: r.width, h: r.height }, elements };
  }
  return { root, fonts, missing, screens };
})()`
}

func readTokens(path string) (Tokens, error) {
	t := Tokens{Sources: map[string]SourceTokens{}, Screens: map[string]map[string]ScreenTokens{}}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return t, nil
	}
	if err == nil {
		err = json.Unmarshal(data, &t)
	}
	if err != nil {
		return Tokens{}, fmt.Errorf("%s does not read as tokens, so capturing would drop its screens; fix or remove it: %w", path, err)
	}
	if t.Screens == nil {
		t.Screens = map[string]map[string]ScreenTokens{}
	}
	if t.Sources == nil {
		t.Sources = map[string]SourceTokens{}
	}
	return t, nil
}

func freeze(r CaptureRequest, sourceDir string) (string, error) {
	if r.Source != Prototype {
		from, ok := inside(r.Dir, filepath.FromSlash(r.Source))
		if !ok {
			return "", fmt.Errorf("%s is not a file in the factory directory %s", r.Source, r.Dir)
		}
		to := filepath.Join(sourceDir, filepath.Base(from))
		return to, copyInto(from, to)
	}
	if r.Entry == "" || r.URL == "" {
		return "", errors.New("a prototype capture needs its entry module and the review stack's page")
	}
	root := filepath.Dir(r.Entry)
	for _, file := range reachable(r.Entry, root) {
		rel, _ := filepath.Rel(root, file)
		if err := copyInto(file, filepath.Join(sourceDir, rel)); err != nil {
			return "", err
		}
	}
	return filepath.Join(sourceDir, filepath.Base(r.Entry)), nil
}

var relativeImport = regexp.MustCompile(`(?:from\s+|import\s*\(?\s*)["'](\.{1,2}/[^"']+)["']`)

var moduleSuffixes = []string{"", ".tsx", ".ts", ".jsx", ".js", ".css", "/index.tsx", "/index.ts", "/index.jsx", "/index.js"}

func reachable(entry, root string) []string {
	seen := []string{}
	queue := []string{entry}
	for len(queue) > 0 {
		file := queue[0]
		queue = queue[1:]
		if slices.Contains(seen, file) {
			continue
		}
		seen = append(seen, file)
		data, err := os.ReadFile(file)
		if err != nil {
			continue
		}
		for _, m := range relativeImport.FindAllStringSubmatch(string(data), -1) {
			if found, ok := resolveModule(filepath.Join(filepath.Dir(file), m[1]), root); ok {
				queue = append(queue, found)
			}
		}
	}
	return seen
}

func resolveModule(base, root string) (string, bool) {
	if rel, err := filepath.Rel(root, base); err != nil || !filepath.IsLocal(rel) {
		return "", false
	}
	for _, suffix := range moduleSuffixes {
		if info, err := os.Stat(base + suffix); err == nil && info.Mode().IsRegular() {
			return base + suffix, true
		}
	}
	return "", false
}

func copyInto(from, to string) error {
	data, err := os.ReadFile(from)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
		return err
	}
	return os.WriteFile(to, data, 0o644)
}

func fileSHA256(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}
