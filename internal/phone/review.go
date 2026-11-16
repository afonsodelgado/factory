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

func reviewStream(path string) bool {
	return strings.HasPrefix(path, "/r/") && strings.HasSuffix(path, "/_api/events")
}

func (s *Server) review(w http.ResponseWriter, r *http.Request, device Device) {
	if s.Review == nil {
		unavailable(w)
		return
	}
	if !reviewStream(r.URL.Path) {
		s.Review.ServeHTTP(w, r)
		return
	}
	st := s.watch(device.ID)
	defer s.unsubscribe(st)
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	go func() {
		select {
		case <-st.closed:
			cancel()
		case <-ctx.Done():
		}
	}()
	s.Review.ServeHTTP(&pairedWriter{ResponseWriter: w, paired: func() bool { return s.Store.Paired(device.ID) }, revoked: cancel}, r.WithContext(ctx))
}

var errRevoked = errors.New("this phone was unpaired")

type pairedWriter struct {
	http.ResponseWriter
	paired  func() bool
	revoked func()
}

func (p *pairedWriter) Write(b []byte) (int, error) {
	if !p.paired() {
		p.revoked()
		return 0, errRevoked
	}
	return p.ResponseWriter.Write(b)
}

func (p *pairedWriter) Unwrap() http.ResponseWriter { return p.ResponseWriter }

func page(w http.ResponseWriter, status int, t *template.Template, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	t.Execute(w, data)
}

const pageHead = `<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1,viewport-fit=cover">
<meta name="theme-color" content="#16161e"><title>factory</title>
<style>:root{color-scheme:dark}body{margin:0;padding:calc(24px + env(safe-area-inset-top)) 16px 24px;background:#16161e;color:#a9b1d6;font:15px/1.5 ui-monospace,Menlo,monospace}h1{color:#c0caf5;font-size:17px}a{display:inline-flex;align-items:center;min-height:44px;padding:0 16px;border-radius:6px;background:#7aa2f7;color:#15161e;font-weight:700;text-decoration:none}</style>
</head><body>`

var unpairedPage = template.Must(template.New("unpaired").Parse(pageHead + `<h1>This phone is not paired</h1>
<p>On the computer, open the hub, press Phone and pair again, or run <code>factory phone pair</code>. Then scan the new code with this phone.</p>
<p><a href="/">Open factory</a></p>
<script>try{indexedDB.deleteDatabase({{.}})}catch(e){}</script>
</body></html>
`))

var changingPage = template.Must(template.New("changing").Parse(pageHead + `<h1>factory is restarting</h1>
<p>The factories are moving between the hub and <code>factory serve</code>. This page tries again in a moment.</p>
<p><a href="">Try again</a></p>
<script>setTimeout(function(){location.reload()},2000)</script>
</body></html>
`))
