package phone

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"
)

func TestTheShellServesOnlyAppRoutesAndItsOwnFiles(t *testing.T) {
	built := fstest.MapFS{
		"pwa/unavailable.html":  {Data: []byte("no app")},
		"pwa/dist/index.html":   {Data: []byte("<div id=root></div>")},
		"pwa/dist/sw.js":        {Data: []byte("self")},
		"pwa/dist/assets/a.js":  {Data: []byte("app")},
		"pwa/dist/build.json":   {Data: []byte(`{"version":"v"}`)},
		"pwa/dist/assets/b.css": {Data: []byte("css")},
	}
	shell := Shell(built)
	for path, want := range map[string]int{
		"/": 200, "/f/shop-csv": 200, "/f/shop-csv/i/k1": 200, "/n/k1": 200, "/setup": 200,
		"/sw.js": 200, "/assets/a.js": 200,
		"/f/shop-csv/unknown/deep": 404, "/assets/": 404, "/index.html": 404, "/secret": 404,
	} {
		rec := httptest.NewRecorder()
		shell.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != want {
			t.Errorf("%s answered %d; want %d", path, rec.Code, want)
		}
	}

	bare := fstest.MapFS{"pwa/unavailable.html": {Data: []byte("no app")}}
	rec := httptest.NewRecorder()
	Shell(bare).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("a Go-only build answered %d; want 503 with the run make build page", rec.Code)
	}
	if _, err := App(bare); err != ErrNoApp {
		t.Errorf("App on a Go-only build: %v; want ErrNoApp so Phone cannot be enabled", err)
	}
}
