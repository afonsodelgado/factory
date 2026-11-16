package phone

import (
	"context"
	"errors"
	"html/template"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
)

const DatabaseName = "factory-phone"

func ReviewProxy(socket string) http.Handler {
	return &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(&url.URL{Scheme: "http", Host: "review"})
			pr.Out.Header.Del("Cookie")
		},
		Transport: &http.Transport{
			DisableKeepAlives: true,
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var dialer net.Dialer
				return dialer.DialContext(ctx, "unix", socket)
			},
		},
		FlushInterval: -1,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, _ error) {
			if errors.Is(r.Context().Err(), context.Canceled) {
				return
			}
			if opensPage(r.Method, r.URL.Path) {
				page(w, http.StatusServiceUnavailable, changingPage, nil)
				return
			}
			unavailable(w)
		},
	}
}

func opensPage(method, path string) bool {
	return method == http.MethodGet && strings.HasPrefix(path, "/r/") && !strings.Contains(path, "/_api/")
}

func (s *Server) review(w http.ResponseWriter, r *http.Request) {
	if s.Review == nil {
		unavailable(w)
		return
	}
	s.Review.ServeHTTP(w, r)
}

func page(w http.ResponseWriter, status int, t *template.Template, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	t.Execute(w, data)
}

func RefusalPage(w http.ResponseWriter, owner, login string) {
	page(w, http.StatusForbidden, refusedPage, map[string]string{"Owner": owner, "Login": login, "Database": DatabaseName})
}

const pageHead = `<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1,viewport-fit=cover">
<meta name="theme-color" content="#16161e"><title>factory</title>
<style>:root{color-scheme:dark}body{margin:0;padding:calc(24px + env(safe-area-inset-top)) 16px 24px;background:#16161e;color:#a9b1d6;font:15px/1.5 ui-monospace,Menlo,monospace}h1{color:#c0caf5;font-size:17px}b{color:#c0caf5}a{display:inline-flex;align-items:center;min-height:44px;padding:0 16px;border-radius:6px;background:#7aa2f7;color:#15161e;font-weight:700;text-decoration:none}</style>
</head><body>`

var refusedPage = template.Must(template.New("refused").Parse(pageHead + `<h1>This node answers {{.Owner}} only</h1>
<p>This device is signed in to Tailscale as <b>{{.Login}}</b>. factory nodes answer their owner and no one else, so nothing from this node is shown or kept here.</p>
<p>Switch to {{.Owner}} in the Tailscale app, then open factory again.</p>
<script>try{indexedDB.deleteDatabase({{.Database}})}catch(e){}</script>
</body></html>
`))

var changingPage = template.Must(template.New("changing").Parse(pageHead + `<h1>factory is restarting</h1>
<p>The node is starting its review pages. This page tries again in a moment.</p>
<p><a href="">Try again</a></p>
<script>setTimeout(function(){location.reload()},2000)</script>
</body></html>
`))
