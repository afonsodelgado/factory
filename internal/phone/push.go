package phone

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"strconv"
	"sync"
	"syscall"
	"time"

	webpush "github.com/SherClockHolmes/webpush-go"
)

const (
	pushTTL      = 24 * 60 * 60
	pushTimeout  = 30 * time.Second
	pushEvery    = 5 * time.Second
	firstRetry   = 5 * time.Second
	longestRetry = 30 * time.Minute
	maxAttempts  = 8
	sendingLease = 2 * time.Minute
)

var sharedAddresses = netip.MustParsePrefix("100.64.0.0/10")

type Pusher struct {
	Store  Store
	Origin string
	Client webpush.HTTPClient
	Now    func() time.Time

	once sync.Once
	wake chan struct{}
}

type Delivery struct {
	Device   string `json:"device"`
	Accepted bool   `json:"accepted"`
	Problem  string `json:"problem,omitempty"`
}

func PublicClient() *http.Client {
	dialer := &net.Dialer{Timeout: 10 * time.Second, Control: refusePrivate}
	return &http.Client{
		Timeout:       pushTimeout,
		Transport:     &http.Transport{DialContext: dialer.DialContext, TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 20 * time.Second},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

func refusePrivate(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	ip, err := netip.ParseAddr(host)
	if err != nil || !public(ip.Unmap()) {
		return fmt.Errorf("push endpoint address %s is not public", host)
	}
	return nil
}

func public(ip netip.Addr) bool {
	return ip.IsGlobalUnicast() && !ip.IsPrivate() && !sharedAddresses.Contains(ip)
}

func (p *Pusher) now() time.Time {
	if p.Now == nil {
		return time.Now()
	}
	return p.Now()
}

func (p *Pusher) woken() chan struct{} {
	p.once.Do(func() { p.wake = make(chan struct{}, 1) })
	return p.wake
}

func (p *Pusher) Wake() {
	select {
	case p.woken() <- struct{}{}:
	default:
	}
}

func (p *Pusher) Run(ctx context.Context) {
	ticker := time.NewTicker(pushEvery)
	defer ticker.Stop()
	for {
		p.Deliver(ctx)
		select {
		case <-ctx.Done():
			return
		case <-p.woken():
		case <-ticker.C:
		}
	}
}

func (p *Pusher) Deliver(ctx context.Context) {
	keys, err := p.Store.vapidPair()
	if err != nil {
		return
	}
	jobs, err := p.Store.takeDue(p.now())
	if err != nil {
		return
	}
	for _, j := range jobs {
		status, retry, problem := p.send(ctx, keys, Subscription{Endpoint: j.Endpoint}, j)
		p.Store.settle(j, status, retry, problem, p.now())
	}
}

func (p *Pusher) Test(ctx context.Context) ([]Delivery, error) {
	keys, err := p.Store.vapidPair()
	if err != nil {
		return nil, err
	}
	subs, err := p.Store.Subscriptions()
	if err != nil {
		return nil, err
	}
	payload := Payload{Tag: "test", Title: "factory · test", Body: "Notifications work on this phone.", URL: "/"}
	results := []Delivery{}
	for _, sub := range subs {
		j := job{Device: sub.Device, Endpoint: sub.Endpoint, Payload: string(mustJSON(payload))}
		status, _, problem := p.send(ctx, keys, sub, j)
		results = append(results, Delivery{Device: sub.Device, Accepted: accepted(status), Problem: problem})
		p.Store.recordDelivery(sub.Device, sub.Endpoint, p.now(), problem)
		if gone(status) {
			p.Store.Unsubscribe(sub.Device)
		}
	}
	return results, nil
}

func (p *Pusher) send(ctx context.Context, keys vapidKeys, sub Subscription, j job) (int, time.Duration, string) {
	if sub.P256dh == "" {
		found, ok := p.Store.SubscriptionOf(j.Device)
		if !ok || found.Endpoint != j.Endpoint {
			return http.StatusGone, 0, "the phone unsubscribed"
		}
		sub = found
	}
	ctx, cancel := context.WithTimeout(ctx, pushTimeout)
	defer cancel()
	resp, err := webpush.SendNotificationWithContext(ctx, []byte(j.Payload), &webpush.Subscription{Endpoint: sub.Endpoint, Keys: webpush.Keys{Auth: sub.Auth, P256dh: sub.P256dh}}, &webpush.Options{
		HTTPClient:      p.Client,
		Subscriber:      p.Origin,
		VAPIDPublicKey:  keys.public,
		VAPIDPrivateKey: keys.private,
		TTL:             pushTTL,
		Topic:           j.Tag,
		Urgency:         webpush.UrgencyHigh,
	})
	if err != nil {
		return 0, 0, "the push service could not be reached: " + err.Error()
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	if accepted(resp.StatusCode) {
		return resp.StatusCode, 0, ""
	}
	return resp.StatusCode, retryDelay(resp.Header.Get("Retry-After"), p.now()), fmt.Sprintf("the push service answered %d", resp.StatusCode)
}

func accepted(status int) bool { return status >= 200 && status < 300 }

func gone(status int) bool { return status == http.StatusNotFound || status == http.StatusGone }

func transient(status int) bool {
	return status == 0 || status == http.StatusTooManyRequests || status >= 500
}

func retryDelay(header string, now time.Time) time.Duration {
	if seconds, err := strconv.Atoi(header); err == nil && seconds >= 0 {
		return time.Duration(seconds) * time.Second
	}
	if at, err := http.ParseTime(header); err == nil && at.After(now) {
		return at.Sub(now)
	}
	return 0
}

func backoff(attempts int) time.Duration {
	return min(firstRetry<<(attempts-1), longestRetry)
}

func (s Store) takeDue(now time.Time) ([]job, error) {
	var due []job
	err := update(s, pushFile, func(l *ledger) error {
		for i := range l.Jobs {
			j := &l.Jobs[i]
			if j.Due.After(now) {
				continue
			}
			j.Sending, j.Due = true, now.Add(sendingLease)
			due = append(due, *j)
		}
		return nil
	})
	return due, err
}

func (s Store) settle(sent job, status int, retry time.Duration, problem string, now time.Time) {
	if gone(status) {
		s.Unsubscribe(sent.Device)
		return
	}
	final := accepted(status) || !transient(status)
	update(s, pushFile, func(l *ledger) error {
		i := slices.IndexFunc(l.Jobs, func(j job) bool { return j.ID == sent.ID })
		if i < 0 {
			return nil
		}
		j := &l.Jobs[i]
		j.Attempts++
		final = final || j.Attempts >= maxAttempts
		if final {
			l.Jobs = slices.Delete(l.Jobs, i, i+1)
			return nil
		}
		j.Sending, j.Due = false, now.Add(max(retry, backoff(j.Attempts)))
		return nil
	})
	if final {
		s.recordDelivery(sent.Device, sent.Endpoint, now, problem)
	}
}
