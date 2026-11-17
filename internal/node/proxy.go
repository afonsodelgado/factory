package node

import (
	"bytes"
	"cmp"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/http/httputil"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/afonsodelgado/factory/internal/host"
	"github.com/afonsodelgado/factory/internal/phone"
	"github.com/afonsodelgado/factory/internal/review"
)

var loopbackNames = []string{"localhost", "127.0.0.1", "::1"}

func (s *Stacks) proxy(run *liveStack) http.Handler {
	lease := run.record.StackLease
	upstream, _ := upstreamOf(run.recipe.URL)
	public := &url.URL{Scheme: "https", Host: s.Host + ":" + strconv.Itoa(run.record.HTTPS)}
	page := review.Factory{Key: lease.Route, Label: lease.Label}
	back := s.Origin + "/f/" + url.PathEscape(lease.Route) + "/pages"
	tag := func(r *http.Request) []byte {
		return review.AppTag(page, lease.Page, strings.Contains(r.UserAgent(), "Mobi"), back)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+review.AppPrefix+"/overlay.js", s.serveOverlay)
	mux.HandleFunc("GET "+review.AppPrefix+"/access", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	mux.HandleFunc("GET "+review.AppPrefix+"/sign-in", s.signIn(run, upstream, public))
	mux.HandleFunc(review.AppPrefix+"/api/{rest...}", s.bridge(lease.Route, public.String()))
	mux.Handle("/", appProxy(upstream, public, tag))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.admit(w, r, public.Host) {
			mux.ServeHTTP(w, r.WithContext(s.Gate.untilExposed(r.Context())))
		}
	})
}

func (s *Stacks) admit(w http.ResponseWriter, r *http.Request, appHost string) bool {
	login, status, problem := s.Gate.identity(r)
	switch {
	case status == http.StatusForbidden && login != "":
		s.Gate.refuseAccount(w, r, login, problem)
	case problem != "":
		fail(w, status, problem)
	case s.Gate.exposed(w, r):
	case r.Host != appHost:
		fail(w, http.StatusMisdirectedRequest, "this review stack answers only at https://"+appHost)
	default:
		return true
	}
	return false
}

func appProxy(upstream, public *url.URL, tag func(*http.Request) []byte) http.Handler {
	return &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(upstream)
			for name := range pr.Out.Header {
				if internalHeader(name) {
					pr.Out.Header.Del(name)
				}
			}
			pr.Out.Header.Del("Accept-Encoding")
			pr.Out.Header.Set("X-Forwarded-Host", public.Host)
			pr.Out.Header.Set("X-Forwarded-Proto", public.Scheme)
		},
		ModifyResponse: func(resp *http.Response) error {
			relocate(resp, upstream, public)
			return injectOverlay(resp, tag(resp.Request))
		},
	}
}

func internalHeader(name string) bool {
	name = http.CanonicalHeaderKey(name)
	return strings.HasPrefix(name, "Tailscale-") || strings.HasPrefix(name, "X-Factory-")
}

func relocate(resp *http.Response, upstream, public *url.URL) {
	to, err := url.Parse(resp.Header.Get("Location"))
	pointsUpstream := err == nil && to.Scheme == "http" && to.Port() == upstream.Port() && slices.Contains(loopbackNames, to.Hostname())
	if pointsUpstream {
		to.Scheme, to.Host = public.Scheme, public.Host
		resp.Header.Set("Location", to.String())
	}
}

func injectOverlay(resp *http.Response, tag []byte) error {
	media, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if media != "text/html" || resp.Header.Get("Content-Encoding") != "" || resp.Request.Method == http.MethodHead {
		return nil
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		return err
	}
	page := review.Inject(body, tag)
	resp.Body = io.NopCloser(bytes.NewReader(page))
	resp.ContentLength = int64(len(page))
	resp.Header.Set("Content-Length", strconv.Itoa(len(page)))
	return nil
}

func (s *Stacks) serveOverlay(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Write(s.Review.Overlay())
}

func (s *Stacks) bridge(route, origin string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		origins := r.Header.Values("Origin")
		fromItsPage := len(origins) == 1 && origins[0] == origin
		switch {
		case slices.Contains(strings.Split(r.PathValue("rest"), "/"), ".."):
			fail(w, http.StatusBadRequest, "this path leaves the review stack's comments")
			return
		case mutates(r) && !fromItsPage, len(origins) > 0 && !fromItsPage:
			fail(w, http.StatusForbidden, "comments on this review stack come only from its own page at "+origin)
			return
		case mutates(r) && r.Method != http.MethodDelete && !sendsJSON(r):
			fail(w, http.StatusUnsupportedMediaType, "send JSON")
			return
		}
		forward := r.Clone(r.Context())
		forward.URL.Path, forward.URL.RawPath = "/r/"+route+"/_api/"+r.PathValue("rest"), ""
		s.mobile().ServeHTTP(w, forward)
	}
}

func (s *Stacks) mobile() http.Handler {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.bridged == nil {
		s.bridged = s.Review.MobileHandler()
	}
	return s.bridged
}

func (s *Stacks) signIn(run *liveStack, upstream, public *url.URL) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Referrer-Policy", "no-referrer")
		if !s.redeem(r.URL.Query().Get("ticket"), run.record.Factory) {
			phone.Notice(w, http.StatusGone, "This sign-in link was used already", "Each review stack link signs this browser in once. Open the review stack again from the factory's Pages tab.")
			return
		}
		cookies, err := signIn(r.Context(), run.recipe)
		if err != nil {
			phone.Notice(w, http.StatusBadGateway, "The review stack could not sign you in ("+host.FailSignIn+")", err.Error())
			return
		}
		if err := holdable(cookies, upstream.Hostname(), public.Host); err != nil {
			phone.Notice(w, http.StatusConflict, "The review stack cannot sign this browser in ("+host.FailPhoneSignIn+")", err.Error())
			return
		}
		for _, c := range cookies {
			http.SetCookie(w, &http.Cookie{Name: c.Name, Value: c.Value, Path: cmp.Or(c.Path, "/"), Secure: true, SameSite: http.SameSiteLaxMode})
		}
		app, _ := url.Parse(run.recipe.URL)
		http.Redirect(w, r, app.RequestURI(), http.StatusSeeOther)
	}
}

func holdable(cookies []review.Cookie, appHost, publicHost string) error {
	for _, c := range cookies {
		domain := strings.TrimPrefix(c.Domain, ".")
		if domain != "" && domain != appHost && !slices.Contains(loopbackNames, domain) {
			return fmt.Errorf("qa.sign_in signs in on %s (cookie %s), and a browser at https://%s cannot hold a cookie for another site; make qa.sign_in print cookies for the app's own host", c.Domain, c.Name, publicHost)
		}
	}
	return nil
}
