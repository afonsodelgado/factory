package phone

import (
	"errors"
	"io/fs"
	"net/http"
	"regexp"
	"strings"
)

var (
	ErrNoApp   = errors.New("the phone app is not part of this build; run make build, then try again")
	shellRoute = regexp.MustCompile(`^/(setup|phone(/(add(/[^/]+)?|nodes/[^/]+))?|n/[^/]+/[^/]+|f/[^/]+(/(pages|message|i/[^/]+))?)?$`)
)

func App(bundle fs.FS) (fs.FS, error) {
	app, err := fs.Sub(bundle, "pwa/dist")
	if err != nil {
		return nil, err
	}
	if _, err := fs.Stat(app, "index.html"); err != nil {
		return nil, ErrNoApp
	}
	return app, nil
}

func Shell(bundle fs.FS) http.Handler {
	app, err := App(bundle)
	if err != nil {
		marker, _ := fs.ReadFile(bundle, "pwa/unavailable.html")
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(http.StatusServiceUnavailable)
			w.Write(marker)
		})
	}
	files := http.FileServerFS(app)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		switch {
		case r.Method != http.MethodGet && r.Method != http.MethodHead:
			fail(w, http.StatusMethodNotAllowed, "the app's files are read-only")
		case shellRoute.MatchString(path):
			w.Header().Set("Cache-Control", "no-cache")
			http.ServeFileFS(w, r, app, "index.html")
		case path == "/index.html" || strings.HasSuffix(path, "/"):
			http.NotFound(w, r)
		case strings.HasPrefix(path, "/assets/"):
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			files.ServeHTTP(w, r)
		default:
			w.Header().Set("Cache-Control", "no-cache")
			files.ServeHTTP(w, r)
		}
	})
}
