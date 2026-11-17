package node

import (
	"net"
	"net/http"
	"strings"
)

func (g Gate) allows(origin string) bool {
	enrolledHome := g.Home != nil && origin == g.Home()
	return origin != "" && (origin == g.Origin || enrolledHome)
}

func (g Gate) scopedReview(r *http.Request) bool {
	return g.OpaquePath != "" && strings.HasPrefix(r.URL.Path, g.OpaquePath)
}

func preflight(r *http.Request) bool {
	return r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != ""
}

func (g Gate) cors(w http.ResponseWriter, r *http.Request) bool {
	origins := r.Header.Values("Origin")
	if len(origins) == 0 || (g.scopedReview(r) && origins[0] == "null") {
		return false
	}
	w.Header().Add("Vary", "Origin")
	allowed := len(origins) == 1 && g.allows(origins[0])
	if !preflight(r) {
		if allowed {
			w.Header().Set("Access-Control-Allow-Origin", origins[0])
		}
		return false
	}
	if !allowed {
		w.WriteHeader(http.StatusForbidden)
		return true
	}
	h := w.Header()
	h.Set("Access-Control-Allow-Origin", origins[0])
	h.Set("Access-Control-Allow-Methods", "GET, POST")
	h.Set("Access-Control-Allow-Headers", "Content-Type, "+OperationHeader)
	h.Set("Access-Control-Max-Age", "600")
	w.WriteHeader(http.StatusNoContent)
	return true
}

func (g Gate) readRefusal(r *http.Request) string {
	if !strings.HasPrefix(r.URL.Path, "/api/") {
		return ""
	}
	origins := r.Header.Values("Origin")
	site := r.Header.Get("Sec-Fetch-Site")
	switch {
	case len(origins) > 1, len(origins) == 1 && !g.allows(origins[0]):
		return "this node answers browser reads only from its own origin and the phone app's home"
	case len(origins) == 0 && (site == "cross-site" || site == "same-site"):
		return "this node does not answer pages embedded from another site"
	}
	return ""
}

func viaSocket(r *http.Request) bool {
	addr, _ := r.Context().Value(http.LocalAddrContextKey).(net.Addr)
	_, unix := addr.(*net.UnixAddr)
	return unix
}

func fromBrowser(r *http.Request) bool {
	if len(r.Header.Values("Origin")) > 0 {
		return true
	}
	for name := range r.Header {
		if strings.HasPrefix(http.CanonicalHeaderKey(name), "Sec-Fetch-") {
			return true
		}
	}
	return false
}

func nativeOnly(serve func(http.ResponseWriter, *http.Request, string)) func(http.ResponseWriter, *http.Request, string) {
	return func(w http.ResponseWriter, r *http.Request, caller string) {
		if fromBrowser(r) {
			fail(w, http.StatusForbidden, "this route is for nodes only, never a browser page")
			return
		}
		serve(w, r, caller)
	}
}

func (n *Nodes) homeOnly(serve func(http.ResponseWriter, *http.Request, string)) func(http.ResponseWriter, *http.Request, string) {
	return func(w http.ResponseWriter, r *http.Request, caller string) {
		origins := r.Header.Values("Origin")
		home := n.Dir.Home()
		fromHome := len(origins) == 1 && home != "" && origins[0] == home
		switch {
		case viaSocket(r):
		case !fromHome:
			fail(w, http.StatusForbidden, "changing the node list, cloning or moving from a browser works only from the phone app at "+home)
			return
		case !sendsJSON(r):
			fail(w, http.StatusUnsupportedMediaType, "send JSON")
			return
		}
		serve(w, r, caller)
	}
}

func (n *Nodes) homeOrNative(serve func(http.ResponseWriter, *http.Request, string)) func(http.ResponseWriter, *http.Request, string) {
	browserRoute := n.homeOnly(serve)
	return func(w http.ResponseWriter, r *http.Request, caller string) {
		if fromBrowser(r) {
			browserRoute(w, r, caller)
			return
		}
		serve(w, r, caller)
	}
}
