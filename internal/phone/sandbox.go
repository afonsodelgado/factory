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
	factory string
	session string
	expires time.Time
}

type Reviews struct {
	Review  http.Handler
	Now     func() time.Time
	Session func(*http.Request) (string, bool)
	Live    func(session string) bool

	grants grants
}

func (v *Reviews) Register(mux *http.ServeMux) {
	mux.HandleFunc("/r/", v.openReview)
	mux.HandleFunc(GrantPrefix+"{token}/{rest...}", v.sandboxed)
}

func (v *Reviews) now() time.Time {
	if v.Now == nil {
		return time.Now()
	}
	return v.Now()
}

func (v *Reviews) session(r *http.Request) (string, bool) {
	if v.Session == nil {
		return "", true
	}
	return v.Session(r)
}

func (v *Reviews) live(held grant) bool {
	return v.Live == nil || v.Live(held.session)
}

type grants struct {
	mu     sync.Mutex
	tokens map[string]grant
}

func (g *grants) mint(factory, session string, now time.Time) string {
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
		if held.factory == factory && held.session == session && held.expires.Sub(now) > grantLifetime/2 {
			return token
		}
	}
	token := base64.RawURLEncoding.EncodeToString(randomBytes(24))
	g.tokens[token] = grant{factory: factory, session: session, expires: now.Add(grantLifetime)}
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

func (v *Reviews) openReview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		fail(w, http.StatusMethodNotAllowed, "open review pages with GET")
		return
	}
	factory, ok := factoryOf(r.URL.EscapedPath())
	if !ok {
		fail(w, http.StatusNotFound, "no such review page")
		return
	}
	session, admitted := v.session(r)
	if !admitted {
		page(w, http.StatusForbidden, fromTheHubPage, nil)
		return
	}
	target := GrantPrefix + v.grants.mint(factory, session, v.now()) + r.URL.EscapedPath()
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	http.Redirect(w, r, target, http.StatusSeeOther)
}

func (v *Reviews) sandboxed(w http.ResponseWriter, r *http.Request) {
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
	held, live := v.grants.held(token, v.now())
	live = live && v.live(held)
	escaped := strings.TrimPrefix(r.URL.EscapedPath(), GrantPrefix+token)
	path, err := url.PathUnescape(escaped)
	switch {
	case !live:
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
		v.review(w, forward)
	}
}

var expiredPage = template.Must(template.New("expired").Parse(pageHead + `<h1>This review page expired</h1>
<p>Open factory and open the page again.</p>
<p><a href="/">Open factory</a></p>
</body></html>
`))

var fromTheHubPage = template.Must(template.New("from the hub").Parse(pageHead + `<h1>Open this page from the hub</h1>
<p>This address answers only a browser that came through a link from the hub on this machine. Select the factory in the hub and press <code>ctrl+o</code> on its page.</p>
</body></html>
`))

var usedLinkPage = template.Must(template.New("used").Parse(pageHead + `<h1>This link was already used</h1>
<p>Each link from the hub opens its page once. In the hub, press <code>ctrl+o</code> on the page again for a new link.</p>
<p>factory shows review pages only to this machine's browser, after a link from its hub.</p>
</body></html>
`))

func FromTheHub(w http.ResponseWriter) { page(w, http.StatusForbidden, fromTheHubPage, nil) }

func UsedLink(w http.ResponseWriter) { page(w, http.StatusGone, usedLinkPage, nil) }
