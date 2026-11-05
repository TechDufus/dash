package push

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/TechDufus/dash/internal/config"
	"github.com/TechDufus/dash/internal/credentials"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/netguard"
	"github.com/TechDufus/dash/internal/obs"
	"github.com/TechDufus/dash/internal/secret"
)

// Delivery rules (design P.2, contract D42).
const (
	// MaxPayload is the largest JSON payload, before encryption.
	MaxPayload = 3 << 10
	// HourlyLimit is how many pushes one subscription receives in any hour.
	HourlyLimit = 60
	// Retries is how many times a 429, a 5xx or a failed connection is tried again.
	Retries = 3
	// MaxRetryAfter caps the wait a Retry-After asks for.
	MaxRetryAfter = 60 * time.Second
	// warnEvery is how often a subscription the push service keeps refusing is logged.
	warnEvery = time.Hour
	// sendTimeout bounds one request to a push service.
	sendTimeout = 10 * time.Second
	// parallel bounds the deliveries in flight.
	parallel = 16
)

// Backoff is the wait before each retry.
var Backoff = [Retries]time.Duration{time.Second, 4 * time.Second, 16 * time.Second}

// ErrNotVerified refuses a test push to a subscription that is not verified.
var ErrNotVerified = errors.New("push: the subscription is not verified")

// Options build a Service.
type Options struct {
	Store   *credentials.Store // opened for push (OpenPush)
	Contact string             // the VAPID subject, push.contact
	Allow   config.PushAllow   // push.allowPrivate
	// PublicURL is dash's public URL, the base of a push's url.
	PublicURL string
	Metrics   *obs.Metrics
	Log       *slog.Logger // nil: slog.Default()
	// Secrets learns every endpoint dash opens, so a log line can never carry one.
	Secrets *secret.Registry
	Now     func() time.Time // nil: time.Now
	// RootCAs are the push services' trust; nil is the system's (tests set a fake service's CA).
	RootCAs *x509.CertPool
	// Sleep waits between retries; nil waits for real (tests make it instant).
	Sleep func(ctx context.Context, d time.Duration) error
}

// Service sends pushes to the subscriptions in the credential store. Deliveries run in the
// background; Close waits for them.
type Service struct {
	o      Options
	log    *slog.Logger
	client *http.Client

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	slots  chan struct{}

	mu     sync.Mutex
	vapid  *VAPID
	sent   map[string][]time.Time // subscription ID -> sends in the last hour
	warned map[string]time.Time   // subscription ID -> last WARN
}

// New builds the sender. Close it at shutdown.
func New(o Options) *Service {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Sleep == nil {
		o.Sleep = sleep
	}
	log := o.Log
	if log == nil {
		log = slog.Default()
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Service{
		o: o, log: log, ctx: ctx, cancel: cancel, slots: make(chan struct{}, parallel),
		client: netguard.NewClient(netguard.Options{
			PrivateHosts: o.Allow.Hosts, PrivatePrefixes: o.Allow.Prefixes, Timeout: sendTimeout, RootCAs: o.RootCAs,
		}),
		sent: map[string][]time.Time{}, warned: map[string]time.Time{},
	}
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// Close stops the deliveries in flight (their retries end) and waits for them.
func (s *Service) Close() {
	s.cancel()
	s.wg.Wait()
}

// Wait waits for the deliveries in flight to finish; for tests.
func (s *Service) Wait() { s.wg.Wait() }

// RunSweep deletes the subscriptions left unverified for ten minutes, every minute, until ctx
// ends (contract D41).
func (s *Service) RunSweep(ctx context.Context) {
	s.o.Store.RunPushSweep(ctx, credentials.PushSweepInterval, s.o.Now, func(swept []credentials.PushSubscription, err error) {
		for _, sub := range swept {
			s.log.Info("push: an unverified subscription was deleted after ten minutes", "subscription", sub.ID, "host", sub.EndpointHost)
		}
		if err != nil {
			s.log.Warn("push: the sweep could not write credentials.yaml", "error", err)
		}
	})
}

// signer is the VAPID signer, made at first need with the store's key (generated then if
// there is none).
func (s *Service) signer() (*VAPID, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.vapid != nil {
		return s.vapid, nil
	}
	priv, _, err := s.o.Store.PushVAPIDKey(s.o.Now())
	if err != nil {
		return nil, err
	}
	key, err := ParseVAPIDKey(priv)
	if err != nil {
		return nil, err
	}
	if s.vapid, err = NewVAPID(key, s.o.Contact, s.o.Now); err != nil {
		return nil, err
	}
	return s.vapid, nil
}

// VAPIDPublicKey is the applicationServerKey browsers subscribe with.
func (s *Service) VAPIDPublicKey() (string, error) {
	v, err := s.signer()
	if err != nil {
		return "", err
	}
	return v.PublicKey(), nil
}

// URL is the absolute URL of path on dash's public URL.
func (s *Service) URL(path string) string { return s.o.PublicURL + path }

// delivery is one push to one subscription.
type delivery struct {
	kind    string // obs.Push*
	msg     model.PushMessage
	ttl     int
	urgency string
	topic   string
}

// SendVerify pushes the verification code to a pending subscription: the only push it gets
// before verification (contract D41). It runs in the background.
func (s *Service) SendVerify(id, code string) {
	s.enqueue(id, delivery{
		kind: obs.PushVerify, ttl: 600, urgency: "high",
		msg: model.PushMessage{
			Type: model.PushTypeVerify, ID: id, Title: "dash: verify this device",
			Body: "Code " + code + ". dash fills it in when Settings -> Devices is open.", URL: s.URL("/settings/devices"), Code: code,
		},
	})
}

// SendTest pushes a test notification to a verified subscription of user (Settings -> Devices).
func (s *Service) SendTest(id string) error {
	sub, ok := s.o.Store.PushSubscriptionByID(id)
	if !ok {
		return credentials.ErrPushNotFound
	}
	if sub.State != credentials.PushVerified {
		return ErrNotVerified
	}
	s.enqueue(id, delivery{
		kind: obs.PushTest, ttl: 600, urgency: "normal",
		msg: model.PushMessage{Type: model.PushTypeTest, ID: "test-" + id, Title: "dash: test push", Body: "Push notifications reach " + sub.DeviceName + ".", URL: s.URL("/settings/devices")},
	})
	return nil
}

// Notify pushes an approval request to every verified subscription of user that takes
// approvals (design P.3: approvals are always pushed), and reports how many it queued. It is
// the hook of the approvals (P7.5).
func (s *Service) Notify(user string, m model.PushMessage) int {
	n := 0
	for _, sub := range s.o.Store.PushSubscriptions(user) {
		if sub.State != credentials.PushVerified || !sub.Approvals {
			continue
		}
		s.enqueue(sub.ID, delivery{kind: obs.PushApproval, msg: m, ttl: 600, urgency: "high"})
		n++
	}
	return n
}

// Attention pushes an attention item to subscription id; crit sets a high urgency. Topic is a
// hash of the item ID, so the push service keeps one undelivered push per item.
func (s *Service) Attention(id string, m model.PushMessage, crit bool) {
	urgency := "normal"
	if crit {
		urgency = "high"
	}
	sum := sha256.Sum256([]byte(m.ID))
	s.enqueue(id, delivery{kind: obs.PushAttention, msg: m, ttl: 3600, urgency: urgency, topic: b64.EncodeToString(sum[:16])})
}

func (s *Service) enqueue(id string, d delivery) {
	if d.msg.TS.IsZero() {
		d.msg.TS = s.o.Now().UTC()
	}
	s.wg.Go(func() {
		select {
		case s.slots <- struct{}{}:
		case <-s.ctx.Done():
			s.o.Metrics.PushSent(d.kind, obs.PushFailed)
			return
		}
		defer func() { <-s.slots }()
		s.o.Metrics.PushSent(d.kind, s.deliver(s.ctx, id, d))
	})
}

// deliver sends d to subscription id and returns the result (obs.Push*). Only a verified
// subscription receives anything but its verify push, and a pending one nothing else.
func (s *Service) deliver(ctx context.Context, id string, d delivery) string {
	t, err := s.o.Store.PushTarget(id)
	if err != nil {
		if !errors.Is(err, credentials.ErrPushNotFound) {
			s.log.Warn("push: the subscription cannot be opened", "subscription", id, "error", err)
		}
		return obs.PushGone
	}
	if want := credentials.PushVerified; d.msg.Type == model.PushTypeVerify && t.State != credentials.PushPending ||
		d.msg.Type != model.PushTypeVerify && t.State != want {
		return obs.PushUnverified
	}
	if s.o.Secrets != nil {
		s.o.Secrets.AddString(t.Endpoint)
	}
	log := s.log.With("subscription", id, "host", t.EndpointHost, "kind", d.kind)
	if !s.allowSend(id) {
		log.Warn("push: the subscription reached 60 pushes this hour; this one is dropped")
		return obs.PushLimited
	}
	body, req, err := s.request(t, d)
	if err != nil {
		log.Warn("push: the push could not be built", "error", err)
		return obs.PushFailed
	}
	for attempt := 0; ; attempt++ {
		status, retryAfter, err := s.post(ctx, t.Endpoint, body, req)
		switch {
		case err == nil && status >= 200 && status < 300:
			return obs.PushOK
		case err == nil && (status == http.StatusNotFound || status == http.StatusGone):
			if _, _, derr := s.o.Store.DeletePushSubscription(id); derr != nil {
				log.Warn("push: the expired subscription could not be deleted from credentials.yaml", "error", derr)
			}
			log.Info("push: the push service says the subscription expired; it was deleted", "status", status)
			return obs.PushGone
		case errors.Is(err, netguard.ErrRefused):
			s.warnOnce(log, id, "push: the endpoint's address is neither global nor in push.allowPrivate; nothing was sent", "error", err)
			return obs.PushRefused
		case ctx.Err() != nil:
			return obs.PushFailed
		case err == nil && status != http.StatusTooManyRequests && status < 500:
			s.warnOnce(log, id, "push: the push service refused the push; the subscription is kept", "status", status)
			return obs.PushRejected
		}
		// 429, 5xx (507 included) or no answer: try again, up to Retries times.
		if attempt == Retries {
			log.Warn("push: the push service did not take the push after retries", "status", status, "error", err)
			return obs.PushFailed
		}
		wait := Backoff[attempt]
		if retryAfter > 0 {
			wait = min(retryAfter, MaxRetryAfter)
		}
		if s.o.Sleep(ctx, wait) != nil {
			return obs.PushFailed
		}
	}
}

// pushRequest is what every attempt of one push shares.
type pushRequest struct {
	authorization string
	ttl, urgency  string
	topic         string
}

// request encrypts d for t and signs it.
func (s *Service) request(t credentials.PushTarget, d delivery) ([]byte, pushRequest, error) {
	u, err := url.Parse(t.Endpoint)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return nil, pushRequest{}, errors.New("the endpoint is not an https URL")
	}
	ua, auth, err := DecodeKeys(t.P256dh, t.Auth)
	if err != nil {
		return nil, pushRequest{}, err
	}
	payload, err := payloadOf(d.msg)
	if err != nil {
		return nil, pushRequest{}, err
	}
	body, err := Encrypt(ua, auth, payload)
	if err != nil {
		return nil, pushRequest{}, err
	}
	v, err := s.signer()
	if err != nil {
		return nil, pushRequest{}, err
	}
	authz, err := v.Authorization(t.Endpoint)
	if err != nil {
		return nil, pushRequest{}, err
	}
	return body, pushRequest{authorization: authz, ttl: strconv.Itoa(d.ttl), urgency: d.urgency, topic: d.topic}, nil
}

// payloadOf is m as JSON within MaxPayload: a body too long is cut, on a rune boundary, with
// an ellipsis.
func payloadOf(m model.PushMessage) ([]byte, error) {
	for {
		b, err := json.Marshal(m)
		if err != nil || len(b) <= MaxPayload {
			return b, err
		}
		over := len(b) - MaxPayload + len("…")
		if len(m.Body) < over {
			return nil, fmt.Errorf("push: a %d-byte payload does not fit %d bytes", len(b), MaxPayload)
		}
		cut := len(m.Body) - over
		for cut > 0 && !utf8.RuneStart(m.Body[cut]) {
			cut--
		}
		m.Body = m.Body[:cut] + "…"
	}
}

// post sends one attempt and returns the status and the wait a Retry-After asked for. An error
// never carries the endpoint: net/http's *url.Error names the URL, so only its cause is kept.
func (s *Service) post(ctx context.Context, endpoint string, body []byte, p pushRequest) (int, time.Duration, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return 0, 0, errors.New("the endpoint is not a URL")
	}
	req.Header.Set("Content-Encoding", "aes128gcm")
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("TTL", p.ttl)
	req.Header.Set("Urgency", p.urgency)
	req.Header.Set("Authorization", p.authorization)
	if p.topic != "" {
		req.Header.Set("Topic", p.topic)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		if ue, ok := errors.AsType[*url.Error](err); ok {
			err = ue.Err
		}
		return 0, 0, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	return resp.StatusCode, retryAfter(resp.Header.Get("Retry-After"), s.o.Now()), nil
}

// retryAfter reads a Retry-After header: seconds or an HTTP date; 0 when absent or past.
func retryAfter(v string, now time.Time) time.Duration {
	if v == "" {
		return 0
	}
	if n, err := strconv.Atoi(v); err == nil && n > 0 {
		return time.Duration(n) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil && t.After(now) {
		return t.Sub(now)
	}
	return 0
}

// allowSend records a send to id unless it already had HourlyLimit in the last hour.
func (s *Service) allowSend(id string) bool {
	now := s.o.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	recent := s.sent[id][:0]
	for _, t := range s.sent[id] {
		if now.Sub(t) < time.Hour {
			recent = append(recent, t)
		}
	}
	if len(recent) >= HourlyLimit {
		s.sent[id] = recent
		return false
	}
	s.sent[id] = append(recent, now)
	return true
}

// warnOnce logs msg for subscription id at most once an hour.
func (s *Service) warnOnce(log *slog.Logger, id, msg string, args ...any) {
	now := s.o.Now()
	s.mu.Lock()
	last, seen := s.warned[id]
	if seen && now.Sub(last) < warnEvery {
		s.mu.Unlock()
		return
	}
	s.warned[id] = now
	s.mu.Unlock()
	log.Warn(msg, args...)
}
