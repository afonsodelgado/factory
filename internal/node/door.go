package node

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"maps"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/afonsodelgado/factory/internal/phone"
)

const (
	doorTicketLife  = 2 * time.Minute
	doorSessionLife = 12 * time.Hour
)

type LocalDoor struct {
	Port   int
	Review http.Handler
	Stacks http.HandlerFunc
	Now    func() time.Time

	mu       sync.Mutex
	tickets  map[string]doorTicket
	sessions map[string]time.Time
	pages    http.Handler
}

type doorTicket struct {
	path    string
	expires time.Time
}

type ReviewLink struct {
	Path string `json:"path"`
	URL  string `json:"url,omitempty"`
}

func (d *LocalDoor) now() time.Time {
	if d.Now == nil {
		return time.Now()
	}
	return d.Now()
}

func (d *LocalDoor) origin() string { return "http://127.0.0.1:" + strconv.Itoa(d.Port) }

func (d *LocalDoor) cookie() string { return "factory_door_" + strconv.Itoa(d.Port) }

func (d *LocalDoor) Link(path string) (string, error) {
	opensReviewPage := strings.HasPrefix(path, "/r/") || strings.HasPrefix(path, "/w/")
	if !opensReviewPage || strings.Contains(path, "..") {
		return "", errors.New("the hub opens only review pages and review stacks through the local door")
	}
	token := secret()
	d.mu.Lock()
	defer d.mu.Unlock()
	now := d.now()
	if d.tickets == nil {
		d.tickets = map[string]doorTicket{}
	}
	maps.DeleteFunc(d.tickets, func(_ string, t doorTicket) bool { return !t.expires.After(now) })
	d.tickets[token] = doorTicket{path: path, expires: now.Add(doorTicketLife)}
	return d.origin() + "/open/" + token, nil
}

func secret() string {
	b := make([]byte, 32)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func (d *LocalDoor) redeem(token string) (string, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	t, ok := d.tickets[token]
	delete(d.tickets, token)
	return t.path, ok && t.expires.After(d.now())
}

func (d *LocalDoor) open() string {
	session := secret()
	d.mu.Lock()
	defer d.mu.Unlock()
	now := d.now()
	if d.sessions == nil {
		d.sessions = map[string]time.Time{}
	}
	maps.DeleteFunc(d.sessions, func(_ string, expires time.Time) bool { return !expires.After(now) })
	d.sessions[session] = now.Add(doorSessionLife)
	return session
}

func (d *LocalDoor) Live(session string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	expires, ok := d.sessions[session]
	return ok && expires.After(d.now())
}

func (d *LocalDoor) Session(r *http.Request) (string, bool) {
	c, err := r.Cookie(d.cookie())
	if err != nil || !d.Live(c.Value) {
		return "", false
	}
	return c.Value, true
}

func (d *LocalDoor) ownHost(host string) bool {
	port := ":" + strconv.Itoa(d.Port)
	return host == "127.0.0.1"+port || host == "localhost"+port
}

func (d *LocalDoor) reviewPages() http.Handler {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.pages == nil {
		mux := http.NewServeMux()
		(&phone.Reviews{Review: d.Review, Now: d.Now, Session: d.Session, Live: d.Live}).Register(mux)
		d.pages = mux
	}
	return d.pages
}

func (d *LocalDoor) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	switch {
	case !fromLoopback(r.RemoteAddr) || !d.ownHost(r.Host):
		fail(w, http.StatusMisdirectedRequest, "this node answers only this machine's browser, at "+d.origin())
	case r.Method == http.MethodGet && strings.HasPrefix(path, "/open/"):
		d.enter(w, r, strings.TrimPrefix(path, "/open/"))
	case strings.HasPrefix(path, "/r/"), strings.HasPrefix(path, phone.GrantPrefix):
		d.reviewPages().ServeHTTP(w, r)
	case r.Method == http.MethodGet && strings.HasPrefix(path, "/w/") && d.Stacks != nil:
		d.stack(w, r)
	default:
		fail(w, http.StatusForbidden, "this node has no owner, so it answers only review pages opened from the hub on this machine")
	}
}

func (d *LocalDoor) enter(w http.ResponseWriter, r *http.Request, token string) {
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Cache-Control", "no-store")
	path, ok := d.redeem(token)
	if !ok {
		phone.UsedLink(w)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: d.cookie(), Value: d.open(), Path: "/", MaxAge: int(doorSessionLife.Seconds()), HttpOnly: true, SameSite: http.SameSiteLaxMode})
	http.Redirect(w, r, path, http.StatusSeeOther)
}

func (d *LocalDoor) stack(w http.ResponseWriter, r *http.Request) {
	if _, ok := d.Session(r); !ok {
		phone.FromTheHub(w)
		return
	}
	route, _ := strings.CutPrefix(r.URL.Path, "/w/")
	r.SetPathValue("route", route)
	d.Stacks(w, r)
}
