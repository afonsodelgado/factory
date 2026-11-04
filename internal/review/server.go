package review

import (
	"bytes"
	"crypto/subtle"
	"fmt"
	"html"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

type Factory struct {
	Key   string
	Label string
	Dir   string
}

type Server struct {
	token   string
	overlay []byte
	ln      net.Listener
	http    *http.Server
	watch   *fsnotify.Watcher

	mu        sync.Mutex
	factories map[string]Factory
	watched   map[string]bool
	clients   map[chan event]bool
	stores    map[string]*Store
	now       func() time.Time
	act       func(Action) (any, error)
	browser   *Browser
}

type event struct {
	name string
	data string
}

func Listen(port int, token string, overlay []byte) (*Server, error) {
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil && port != 0 {
		ln, err = net.Listen("tcp", "127.0.0.1:0")
	}
	if err != nil {
		return nil, err
	}
	watch, err := fsnotify.NewWatcher()
	if err != nil {
		ln.Close()
		return nil, err
	}
	s := &Server{
		token:     token,
		overlay:   overlay,
		ln:        ln,
		watch:     watch,
		factories: map[string]Factory{},
		watched:   map[string]bool{},
		clients:   map[chan event]bool{},
		stores:    map[string]*Store{},
		now:       time.Now,
	}
	s.http = &http.Server{Handler: s.routes(), ReadHeaderTimeout: 10 * time.Second}
	go s.http.Serve(ln)
	go s.forwardChanges()
	return s, nil
}

func (s *Server) Close() error {
	s.watch.Close()
	return s.http.Close()
}

func (s *Server) Origin() string {
	return "http://" + s.ln.Addr().String()
}

func (s *Server) Home() string {
	return s.Origin() + "/r/" + s.token + "/"
}

func (s *Server) SetFactories(factories []Factory) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.factories = map[string]Factory{}
	listed := map[string]bool{}
	for _, f := range factories {
		if f.Key != "" && f.Dir != "" {
			s.factories[f.Key] = f
			listed[f.Dir] = true
		}
	}
	for dir := range s.stores {
		if !listed[dir] {
			delete(s.stores, dir)
		}
	}
}

func (s *Server) PageURL(fileURL string) (string, bool) {
	u, err := url.Parse(fileURL)
	if err != nil || u.Scheme != "file" {
		return "", false
	}
	f, rel, ok := s.locate(u.Path)
	if !ok {
		return "", false
	}
	return s.pageURL(f.Key, rel), true
}

func (s *Server) factoryOfPage(raw string) (Factory, bool) {
	u, err := url.Parse(raw)
	if err != nil || "http://"+u.Host != s.Origin() {
		return Factory{}, false
	}
	return s.factoryOfAPI(u.Path)
}

func (s *Server) factoryOfAPI(path string) (Factory, bool) {
	rest, ok := strings.CutPrefix(path, "/r/"+s.token+"/")
	if !ok {
		return Factory{}, false
	}
	key, _, _ := strings.Cut(rest, "/")
	key, err := url.PathUnescape(key)
	if err != nil {
		return Factory{}, false
	}
	return s.factory(key)
}

func (s *Server) locate(path string) (Factory, string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, f := range s.factories {
		rel, err := filepath.Rel(f.Dir, path)
		if err == nil && filepath.IsLocal(rel) {
			return f, filepath.ToSlash(rel), true
		}
	}
	return Factory{}, "", false
}

func (s *Server) pageURL(key, rel string) string {
	return s.Home() + url.PathEscape(key) + "/" + escapePath(filepath.ToSlash(rel))
}

func escapePath(p string) string {
	parts := strings.Split(p, "/")
	for i, part := range parts {
		parts[i] = url.PathEscape(part)
	}
	return strings.Join(parts, "/")
}

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /_review/overlay.js", s.serveOverlay)
	mux.HandleFunc("GET /favicon.ico", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	mux.HandleFunc("GET /r/{token}/{$}", s.withToken(s.serveIndex))
	mux.HandleFunc("GET /r/{token}/events", s.withToken(s.serveEvents))
	mux.HandleFunc("GET /r/{token}/{key}/{path...}", s.withToken(s.serveFile))
	mux.HandleFunc("GET /r/{token}/{key}/_api/threads", s.withToken(s.listThreads))
	mux.HandleFunc("POST /r/{token}/{key}/_api/threads", s.withToken(s.mutation(s.createThread)))
	mux.HandleFunc("PATCH /r/{token}/{key}/_api/threads/{id}", s.withToken(s.mutation(s.updateThread)))
	mux.HandleFunc("DELETE /r/{token}/{key}/_api/threads/{id}", s.withToken(s.mutation(s.deleteThread)))
	mux.HandleFunc("POST /r/{token}/{key}/_api/threads/{id}/replies", s.withToken(s.mutation(s.replyThread)))
	mux.HandleFunc("GET /r/{token}/{key}/_api/status", s.withToken(s.action("status")))
	mux.HandleFunc("POST /r/{token}/{key}/_api/send", s.withToken(s.mutation(s.action("send"))))
	mux.HandleFunc("POST /r/{token}/{key}/_api/approve", s.withToken(s.mutation(s.action("approve"))))
	return s.sameHost(mux)
}

func (s *Server) sameHost(next http.Handler) http.Handler {
	_, port, _ := net.SplitHostPort(s.ln.Addr().String())
	allowed := map[string]bool{"127.0.0.1:" + port: true, "localhost:" + port: true}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !allowed[r.Host] {
			http.Error(w, "unknown host", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) withToken(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if subtle.ConstantTimeCompare([]byte(r.PathValue("token")), []byte(s.token)) != 1 {
			http.NotFound(w, r)
			return
		}
		next(w, r)
	}
}

func (s *Server) serveOverlay(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Write(s.overlay)
}

func (s *Server) factory(key string) (Factory, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, ok := s.factories[key]
	return f, ok
}

func (s *Server) serveFile(w http.ResponseWriter, r *http.Request) {
	f, ok := s.factory(r.PathValue("key"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	rel := filepath.FromSlash(r.PathValue("path"))
	path, ok := inside(f.Dir, rel)
	if !ok {
		http.NotFound(w, r)
		return
	}
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		http.NotFound(w, r)
		return
	}
	s.watchDir(filepath.Dir(filepath.Join(f.Dir, rel)))
	if !isHTML(path) {
		http.ServeFile(w, r, path)
		return
	}
	page, err := os.ReadFile(path)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Write(inject(page, s.scriptTag(f.Key, filepath.ToSlash(rel))))
}

func inside(dir, rel string) (string, bool) {
	if !filepath.IsLocal(rel) {
		return "", false
	}
	root, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return "", false
	}
	path, err := filepath.EvalSymlinks(filepath.Join(dir, rel))
	if err != nil {
		return "", false
	}
	within, err := filepath.Rel(root, path)
	if err != nil || !filepath.IsLocal(within) {
		return "", false
	}
	return path, true
}

func isHTML(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	return ext == ".html" || ext == ".htm"
}

func (s *Server) scriptTag(key, rel string) []byte {
	base := "/r/" + s.token
	return fmt.Appendf(nil, `<script src="/_review/overlay.js" data-factory-review data-events="%s" data-api="%s" data-page="%s" data-file="%s" defer></script>`,
		html.EscapeString(base+"/events"), html.EscapeString(base+"/"+url.PathEscape(key)+"/_api"), html.EscapeString(key+"/"+rel), html.EscapeString(rel))
}

func inject(page, tag []byte) []byte {
	at := bytes.LastIndex(bytes.ToLower(page), []byte("</body>"))
	if at < 0 {
		return append(page, tag...)
	}
	out := make([]byte, 0, len(page)+len(tag))
	out = append(out, page[:at]...)
	out = append(out, tag...)
	return append(out, page[at:]...)
}

func (s *Server) serveIndex(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	factories := make([]Factory, 0, len(s.factories))
	for _, f := range s.factories {
		factories = append(factories, f)
	}
	s.mu.Unlock()
	sort.Slice(factories, func(i, j int) bool { return factories[i].Label < factories[j].Label })
	var b strings.Builder
	b.WriteString(indexHead)
	for _, f := range factories {
		pages := pagesIn(f.Dir)
		if len(pages) == 0 {
			continue
		}
		fmt.Fprintf(&b, "<section><h2>%s</h2><ul>", html.EscapeString(f.Label))
		for _, rel := range pages {
			fmt.Fprintf(&b, `<li><a href="%s">%s</a></li>`, html.EscapeString(s.pageURL(f.Key, rel)), html.EscapeString(filepath.ToSlash(rel)))
		}
		b.WriteString("</ul></section>")
	}
	b.WriteString("</main></body></html>")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Write([]byte(b.String()))
}

func pagesIn(dir string) []string {
	var pages []string
	for _, pattern := range []string{"*.html", "*/*.html"} {
		matches, _ := filepath.Glob(filepath.Join(dir, pattern))
		for _, m := range matches {
			if rel, err := filepath.Rel(dir, m); err == nil && !strings.HasPrefix(rel, "stages"+string(filepath.Separator)) {
				pages = append(pages, rel)
			}
		}
	}
	sort.Strings(pages)
	return pages
}

const indexHead = `<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>Factory review</title>
<style>
:root{color-scheme:light dark;--bg:#fafaf9;--fg:#1c1917;--muted:#78716c;--line:#e7e5e4;--link:#2563eb}
@media (prefers-color-scheme:dark){:root{--bg:#0c0a09;--fg:#f5f5f4;--muted:#a8a29e;--line:#292524;--link:#60a5fa}}
body{margin:0;background:var(--bg);color:var(--fg);font:15px/1.5 system-ui,sans-serif}
main{max-width:720px;margin:0 auto;padding:32px 16px}
h1{font-size:20px;margin:0 0 24px}h2{font-size:15px;margin:24px 0 8px;color:var(--muted);font-weight:600}
ul{list-style:none;margin:0;padding:0;border-top:1px solid var(--line)}
li{border-bottom:1px solid var(--line)}a{display:block;padding:10px 0;color:var(--link);text-decoration:none}a:hover{text-decoration:underline}
</style></head><body><main><h1>Factory review</h1>`

func (s *Server) watchDir(dir string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.watched[dir] {
		return
	}
	if s.watch.Add(dir) == nil {
		s.watched[dir] = true
	}
}

func (s *Server) forwardChanges() {
	for {
		select {
		case e, ok := <-s.watch.Events:
			if !ok {
				return
			}
			if e.Has(fsnotify.Write) || e.Has(fsnotify.Create) || e.Has(fsnotify.Rename) {
				s.changed(e.Name)
			}
		case _, ok := <-s.watch.Errors:
			if !ok {
				return
			}
		}
	}
}

func (s *Server) changed(path string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, f := range s.factories {
		rel, err := filepath.Rel(f.Dir, path)
		if err != nil || !filepath.IsLocal(rel) {
			continue
		}
		s.broadcastLocked(event{"change", f.Key + "/" + filepath.ToSlash(rel)})
	}
}

func (s *Server) broadcast(e event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.broadcastLocked(e)
}

func (s *Server) broadcastLocked(e event) {
	if s.browser != nil {
		s.browser.push(e)
	}
	for c := range s.clients {
		select {
		case c <- e:
		default:
		}
	}
}

func (s *Server) serveEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	c := make(chan event, 16)
	s.mu.Lock()
	s.clients[c] = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.clients, c)
		s.mu.Unlock()
	}()
	fmt.Fprint(w, ": connected\n\n")
	flusher.Flush()
	keepAlive := time.NewTicker(25 * time.Second)
	defer keepAlive.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case e := <-c:
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", e.name, e.data)
			flusher.Flush()
		case <-keepAlive.C:
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		}
	}
}
