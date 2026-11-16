package review

import (
	"fmt"
	"html"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"
)

const PrefixHeader = "X-Factory-Phone-Prefix"

func MobileSocket(stateDir string) string { return filepath.Join(stateDir, "phone", "review.sock") }

func (s *Server) ServeMobile(path string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return err
	}
	os.Remove(path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		return err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		ln.Close()
		return err
	}
	mobile := &http.Server{Handler: s.MobileHandler(), ReadHeaderTimeout: 10 * time.Second}
	s.mu.Lock()
	s.mobile = mobile
	s.mu.Unlock()
	go mobile.Serve(ln)
	return nil
}

func (s *Server) ServesMobile() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.mobile != nil
}

func (s *Server) MobileHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /_review/overlay.js", s.serveOverlay)
	mux.HandleFunc("GET /events/review", s.serveEvents)
	mux.HandleFunc("GET /r/{key}/{path...}", func(w http.ResponseWriter, r *http.Request) {
		prefix := r.Header.Get(PrefixHeader)
		s.servePage(func(f Factory, rel, revision string) []byte { return s.mobileTag(prefix, f, rel, revision) })(w, r)
	})
	mux.HandleFunc("GET /r/{key}/_api/threads", s.listThreads)
	mux.HandleFunc("POST /r/{key}/_api/threads", s.createThread)
	mux.HandleFunc("PATCH /r/{key}/_api/threads/{id}", s.updateThread)
	mux.HandleFunc("DELETE /r/{key}/_api/threads/{id}", s.deleteThread)
	mux.HandleFunc("POST /r/{key}/_api/threads/{id}/replies", s.replyThread)
	mux.HandleFunc("GET /r/{key}/_api/status", s.action("status"))
	mux.HandleFunc("POST /r/{key}/_api/send", s.action("send"))
	mux.HandleFunc("POST /r/{key}/_api/approve", s.action("approve"))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		mux.ServeHTTP(w, r)
	})
}

func (s *Server) mobileTag(prefix string, f Factory, rel, revision string) []byte {
	key := url.PathEscape(f.Key)
	return fmt.Appendf(nil, `<script src="%s" data-factory-review data-mobile data-theme="dark" data-events="%s" data-access="%s" data-api="%s" data-page="%s" data-file="%s" data-label="%s" data-back="%s" data-revision="%s" defer></script>`,
		html.EscapeString(prefix+"/_review/overlay.js"), html.EscapeString(prefix+"/events/review"), html.EscapeString(prefix+"/_access"),
		html.EscapeString(prefix+"/r/"+key+"/_api"), html.EscapeString(f.Key+"/"+rel), html.EscapeString(rel), html.EscapeString(f.Label), html.EscapeString("/f/"+key+"/pages"), revision)
}

func MobilePath(f Factory, fileURL string) (string, bool) {
	u, err := url.Parse(fileURL)
	if err != nil || u.Scheme != "file" {
		return "", false
	}
	rel, err := filepath.Rel(f.Dir, u.Path)
	if err != nil || !filepath.IsLocal(rel) || !isHTML(rel) {
		return "", false
	}
	return "/r/" + url.PathEscape(f.Key) + "/" + escapePath(filepath.ToSlash(rel)), true
}
