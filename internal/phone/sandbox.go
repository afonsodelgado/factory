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
	grantPrefix   = "/rt/"
	grantLifetime = 12 * time.Hour
	sandboxPolicy = "sandbox allow-scripts allow-forms allow-modals"
)

type grant struct {
	device  string
	expires time.Time
}

type grants struct {
	mu     sync.Mutex
	tokens map[string]grant
}

func (g *grants) mint(device string, now time.Time) string {
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
		if held.device == device && held.expires.Sub(now) > grantLifetime/2 {
			return token
		}
	}
	token := base64.RawURLEncoding.EncodeToString(randomBytes(24))
	g.tokens[token] = grant{device: device, expires: now.Add(grantLifetime)}
	return token
}

func (g *grants) device(token string, now time.Time) (string, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	held, ok := g.tokens[token]
	return held.device, ok && held.expires.After(now)
}

func (s *Server) openReview(w http.ResponseWriter, r *http.Request, device Device) {
	if r.Method != http.MethodGet {
		fail(w, http.StatusMethodNotAllowed, "open review pages with GET")
		return
	}
	target := grantPrefix + s.grants.mint(device.ID, s.now()) + r.URL.EscapedPath()
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
	device, held := s.grants.device(token, s.now())
	escaped := strings.TrimPrefix(r.URL.EscapedPath(), grantPrefix+token)
	path, err := url.PathUnescape(escaped)
	switch {
	case !held || !s.Store.Paired(device):
		if opensPage(r.Method, path) {
			page(w, http.StatusUnauthorized, expiredPage, nil)
			return
		}
		fail(w, http.StatusUnauthorized, "This review page expired.")
	case err != nil:
		fail(w, http.StatusBadRequest, "bad path")
	case path == "/_access":
		w.WriteHeader(http.StatusNoContent)
	default:
		forward := r.Clone(r.Context())
		forward.URL.Path, forward.URL.RawPath = path, escaped
		forward.Header.Set(review.PrefixHeader, grantPrefix+token)
		s.review(w, forward, Device{ID: device})
	}
}

var expiredPage = template.Must(template.New("expired").Parse(pageHead + `<h1>This review page expired</h1>
<p>Open factory and open the page again.</p>
<p><a href="/">Open factory</a></p>
</body></html>
`))
