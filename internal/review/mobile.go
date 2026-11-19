package review

import (
	"fmt"
	"html"
	"net/http"
	"net/url"
	"path/filepath"
)

const (
	PrefixHeader = "X-Factory-Phone-Prefix"
	NoBackHeader = "X-Factory-No-Back"
)

func (s *Server) MobileHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /_review/overlay.js", s.serveOverlay)
	mux.HandleFunc("GET /r/{key}/{path...}", func(w http.ResponseWriter, r *http.Request) {
		prefix, back := r.Header.Get(PrefixHeader), r.Header.Get(NoBackHeader) == ""
		s.servePage(func(f Factory, rel, revision string) []byte { return s.mobileTag(prefix, back, f, rel, revision) })(w, r)
	})
	mux.HandleFunc("GET /r/{key}/_api/threads", s.listThreads)
	mux.HandleFunc("POST /r/{key}/_api/threads", s.createThread)
	mux.HandleFunc("PATCH /r/{key}/_api/threads/{id}", s.updateThread)
	mux.HandleFunc("DELETE /r/{key}/_api/threads/{id}", s.deleteThread)
	mux.HandleFunc("POST /r/{key}/_api/threads/{id}/replies", s.replyThread)
	mux.HandleFunc("GET /r/{key}/_api/events", s.serveFactoryEvents)
	mux.HandleFunc("GET /r/{key}/_api/status", s.action("status"))
	mux.HandleFunc("POST /r/{key}/_api/send", s.action("send"))
	mux.HandleFunc("POST /r/{key}/_api/approve", s.action("approve"))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		mux.ServeHTTP(w, r)
	})
}

func (s *Server) mobileTag(prefix string, back bool, f Factory, rel, revision string) []byte {
	key := url.PathEscape(f.Key)
	pages := ""
	if back {
		pages = "/f/" + key + "/pages"
	}
	return fmt.Appendf(nil, `<script src="%s" data-factory-review data-mobile data-theme="dark" data-events="%s" data-access="%s" data-api="%s" data-page="%s" data-file="%s" data-label="%s" data-back="%s" data-revision="%s" defer></script>`,
		html.EscapeString(prefix+"/_review/overlay.js"), html.EscapeString(prefix+"/r/"+key+"/_api/events"), html.EscapeString(prefix+"/_access"),
		html.EscapeString(prefix+"/r/"+key+"/_api"), html.EscapeString(f.Key+"/"+rel), html.EscapeString(rel), html.EscapeString(f.Label), html.EscapeString(pages), revision)
}

const AppPrefix = "/__factory"

func (s *Server) Overlay() []byte { return s.overlay }

func AppTag(f Factory, page string, mobile bool, back string) []byte {
	api, file := AppPrefix+"/api", html.EscapeString(f.Key+"/"+page)
	if !mobile {
		return fmt.Appendf(nil, `<script src="%s/overlay.js" data-factory-review data-events="%s/events" data-api="%s" data-page="%s" data-file="%s" defer></script>`,
			AppPrefix, api, api, file, html.EscapeString(page))
	}
	return fmt.Appendf(nil, `<script src="%s/overlay.js" data-factory-review data-mobile data-theme="dark" data-events="%s/events" data-access="%s/access" data-api="%s" data-page="%s" data-file="%s" data-label="%s" data-back="%s" defer></script>`,
		AppPrefix, api, AppPrefix, api, file, html.EscapeString(page), html.EscapeString(f.Label), html.EscapeString(back))
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
