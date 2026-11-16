package phone

import (
	"encoding/base64"
	"html/template"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/afonsodelgado/factory/internal/review"
)

const (
	GrantPrefix   = "/rt/"
	grantLifetime = 12 * time.Hour
	sandboxPolicy = "sandbox allow-scripts allow-forms allow-modals"
	sandboxOrigin = "null"
)

type grant struct {
	device  string
	factory string
	expires time.Time
}

type grants struct {
	mu     sync.Mutex
	tokens map[string]grant
}

func (g *grants) mint(device, factory string, now time.Time) string {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.tokens == nil {
		g.tokens = map[string]grant{}
	}
	for token, held := range g.tokens {
		if !held.expires.After(now) {
			delete(g.tokens, token)
		}
	}
	for token, held := range g.tokens {
		if held.device == device && held.factory == factory && held.expires.Sub(now) > grantLifetime/2 {
			return token
		}
	}
	token := base64.RawURLEncoding.EncodeToString(randomBytes(24))
	g.tokens[token] = grant{device: device, factory: factory, expires: now.Add(grantLifetime)}
	return token
}

func (g *grants) held(token string, now time.Time) (grant, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	held, ok := g.tokens[token]
	return held, ok && held.expires.After(now)
}

func (g grant) opens(escapedPath string) bool {
	switch escapedPath {
	case "/_access", "/_review/overlay.js":
		return true
	}
	factory, ok := factoryOf(escapedPath)
	return ok && factory == g.factory
}

func factoryOf(escapedPath string) (string, bool) {
	segments := strings.SplitN(escapedPath, "/", 4)
	if len(segments) < 4 || segments[0] != "" || segments[1] != "r" {
		return "", false
	}
	factory, err := url.PathUnescape(segments[2])
	return factory, err == nil && factory != ""
}

func (s *Server) openReview(w http.ResponseWriter, r *http.Request, device Device) {
	if r.Method != http.MethodGet {
		fail(w, http.StatusMethodNotAllowed, "open review pages with GET")
		return
	}
	factory, ok := factoryOf(r.URL.EscapedPath())
	if !ok {
		fail(w, http.StatusNotFound, "no such review page")
		return
	}
	target := GrantPrefix + s.grants.mint(device.ID, factory, s.now()) + r.URL.EscapedPath()
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	http.Redirect(w, r, target, http.StatusSeeOther)
}

func (s *Server) sandboxed(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("Access-Control-Allow-Origin", "null")
	h.Set("Access-Control-Expose-Headers", "X-Factory-Revision")
	h.Set("Content-Security-Policy", sandboxPolicy)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	if r.Method == http.MethodOptions {
		h.Set("Access-Control-Allow-Methods", "GET, POST, PATCH, DELETE")
		h.Set("Access-Control-Allow-Headers", "Content-Type")
		h.Set("Access-Control-Max-Age", "600")
		w.WriteHeader(http.StatusNoContent)
		return
	}
	token := r.PathValue("token")
	held, live := s.grants.held(token, s.now())
	escaped := strings.TrimPrefix(r.URL.EscapedPath(), GrantPrefix+token)
	path, err := url.PathUnescape(escaped)
	switch {
	case !live || !s.Store.Paired(held.device):
		if opensPage(r.Method, path) {
			page(w, http.StatusUnauthorized, expiredPage, nil)
			return
		}
		fail(w, http.StatusUnauthorized, "This review page expired.")
	case err != nil:
		fail(w, http.StatusBadRequest, "bad path")
	case !held.opens(escaped):
		fail(w, http.StatusForbidden, "This review link opens only its own factory's pages.")
	case path == "/_access":
		w.WriteHeader(http.StatusNoContent)
	default:
		forward := r.Clone(r.Context())
		forward.URL.Path, forward.URL.RawPath = path, escaped
		forward.Header.Set(review.PrefixHeader, GrantPrefix+token)
		s.review(w, forward, Device{ID: held.device})
	}
}

var expiredPage = template.Must(template.New("expired").Parse(pageHead + `<h1>This review page expired</h1>
<p>Open factory and open the page again.</p>
<p><a href="/">Open factory</a></p>
</body></html>
`))
