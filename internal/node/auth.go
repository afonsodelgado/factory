package node

import (
	"mime"
	"net/http"
	"net/netip"
	"slices"
	"strings"
	"unicode"
)

const LoginHeader = "Tailscale-User-Login"

type Gate struct {
	Owner      string
	Hosts      []string
	Origin     string
	OpaquePath string
}

func (g Gate) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if status, problem := g.refusal(r); problem != "" {
			fail(w, status, problem)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (g Gate) Principal(*http.Request) (string, bool) { return g.Owner, true }

func (g Gate) refusal(r *http.Request) (int, string) {
	if status, problem := g.identityRefusal(r); problem != "" {
		return status, problem
	}
	if !slices.Contains(g.Hosts, r.Host) {
		return http.StatusMisdirectedRequest, "unknown host " + r.Host
	}
	if !mutates(r) {
		return 0, ""
	}
	if !g.trustedOrigin(r) {
		return http.StatusForbidden, "this request must come from the node's own origin"
	}
	if r.ContentLength != 0 && !sendsJSON(r) {
		return http.StatusUnsupportedMediaType, "send JSON"
	}
	return 0, ""
}

func (g Gate) identityRefusal(r *http.Request) (int, string) {
	if !fromLoopback(r.RemoteAddr) {
		return http.StatusForbidden, "the node answers only Tailscale Serve on this machine"
	}
	logins := r.Header.Values(LoginHeader)
	if len(logins) != 1 {
		return http.StatusUnauthorized, "this request carries no single Tailscale login; open the node through its tailnet address with a user-owned device"
	}
	login, ok := decodeLogin(logins[0])
	if !ok {
		return http.StatusUnauthorized, "this request's Tailscale login is malformed"
	}
	if g.Owner == "" || !strings.EqualFold(login, g.Owner) {
		return http.StatusForbidden, "this node belongs to " + g.Owner + ", not " + login
	}
	return 0, ""
}

func fromLoopback(remote string) bool {
	peer, err := netip.ParseAddrPort(remote)
	return err == nil && peer.Addr().Unmap().IsLoopback()
}

func decodeLogin(raw string) (string, bool) {
	login, err := new(mime.WordDecoder).DecodeHeader(raw)
	isPrintable := func(r rune) bool { return unicode.IsPrint(r) && !unicode.IsSpace(r) }
	undecoded := strings.HasPrefix(login, "=?")
	if err != nil || login == "" || undecoded || strings.IndexFunc(login, func(r rune) bool { return !isPrintable(r) }) >= 0 {
		return "", false
	}
	return login, true
}

func (g Gate) trustedOrigin(r *http.Request) bool {
	origins := r.Header.Values("Origin")
	switch {
	case len(origins) == 0:
		return true
	case len(origins) > 1:
		return false
	case origins[0] == g.Origin && g.Origin != "":
		return true
	}
	scopedReview := g.OpaquePath != "" && strings.HasPrefix(r.URL.Path, g.OpaquePath)
	return scopedReview && origins[0] == "null"
}

func sendsJSON(r *http.Request) bool {
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	return err == nil && media == "application/json"
}

func mutates(r *http.Request) bool {
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return false
	}
	return true
}
