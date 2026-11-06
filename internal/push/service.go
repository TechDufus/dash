package push

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
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
	// parallel bounds the deliveries in flight, perHost those to one push service's host; a
	// delivery waiting out a retry holds neither.
	parallel = 16
	perHost  = 4
	// MaxQueue bounds the deliveries waiting for a slot. Past it the oldest attention, test or
	// verify push is dropped (result dropped); an approval never is.
	MaxQueue = 1024
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
	wg     sync.WaitGroup // one per delivery, from enqueue to its result

	mu       sync.Mutex
	closed   bool
	queue    []*job         // waiting for a slot, oldest first
	active   int            // deliveries holding a slot
	hosts    map[string]int // host -> deliveries holding a slot
	dropWarn time.Time      // the last WARN about a full queue
	vapid    *VAPID
	sent     map[string][]time.Time // subscription ID -> sends in the last hour
	warned   map[string]time.Time   // subscription ID -> last WARN
	pruned   time.Time              // when sent and warned last lost their idle keys
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
		o: o, log: log, ctx: ctx, cancel: cancel,
		client: netguard.NewClient(netguard.Options{
			PrivateHosts: o.Allow.Hosts, PrivatePrefixes: o.Allow.Prefixes, Timeout: sendTimeout, RootCAs: o.RootCAs,
		}),
		hosts: map[string]int{}, sent: map[string][]time.Time{}, warned: map[string]time.Time{},
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

// Close stops the deliveries (the queued ones and the retries end as failed) and waits for them.
func (s *Service) Close() {
	s.cancel()
	s.mu.Lock()
	s.closed = true
	queued := s.queue
	s.queue = nil
	s.mu.Unlock()
	for _, j := range queued {
		s.finish(j, obs.PushFailed)
	}
	s.wg.Wait()
	s.client.CloseIdleConnections()
}

// Wait waits for every delivery queued or in flight to finish; for tests.
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

// job is one delivery on its way: queued, in flight or waiting out a retry.
type job struct {
	id, host string // host: the push service's, for perHost
	d        delivery
	attempt  int
	// Set by the first attempt.
	prepared bool
	t        credentials.PushTarget
	log      *slog.Logger
	body     []byte
	req      pushRequest
}

// enqueue queues d for subscription id. A full queue drops its oldest push that is not an
// approval (or d itself when every queued one is an approval and d is not).
func (s *Service) enqueue(id string, d delivery) {
	if d.msg.TS.IsZero() {
		d.msg.TS = s.o.Now().UTC()
	}
	j := &job{id: id, d: d}
	if sub, ok := s.o.Store.PushSubscriptionByID(id); ok {
		j.host = sub.EndpointHost
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		s.o.Metrics.PushSent(d.kind, obs.PushFailed)
		return
	}
	var dropped *job
	if len(s.queue) >= MaxQueue {
		if i := slices.IndexFunc(s.queue, func(q *job) bool { return q.d.kind != obs.PushApproval }); i >= 0 {
			dropped = s.queue[i]
			s.queue = slices.Delete(s.queue, i, i+1)
		} else if d.kind != obs.PushApproval {
			dropped = j
		}
	}
	if dropped != j {
		s.wg.Add(1)
		s.queue = append(s.queue, j)
		s.dispatch()
	}
	warn := dropped != nil && s.o.Now().Sub(s.dropWarn) >= warnEvery
	if warn {
		s.dropWarn = s.o.Now()
	}
	s.mu.Unlock()
	if dropped != nil {
		s.o.Metrics.PushSent(dropped.d.kind, obs.PushDropped)
		if dropped != j {
			s.wg.Done()
		}
	}
	if warn {
		s.log.Warn("push: more than 1024 pushes are waiting for the push services; the oldest are dropped", "kind", dropped.d.kind)
	}
}

// dispatch starts the queued deliveries that have a slot, oldest first: at most parallel in
// flight, perHost to one host. s.mu is held.
func (s *Service) dispatch() {
	for i := 0; i < len(s.queue) && s.active < parallel; {
		j := s.queue[i]
		if s.hosts[j.host] >= perHost {
			i++
			continue
		}
		s.queue = slices.Delete(s.queue, i, i+1)
		s.active++
		s.hosts[j.host]++
		go s.run(j)
	}
}

// run makes one attempt of j in its slot. A retry gives the slot back while it waits, then
// queues j again ahead of the others.
func (s *Service) run(j *job) {
	result, wait, retry := s.attempt(j)
	s.mu.Lock()
	s.active--
	if s.hosts[j.host]--; s.hosts[j.host] <= 0 {
		delete(s.hosts, j.host)
	}
	s.dispatch()
	s.mu.Unlock()
	if !retry {
		s.finish(j, result)
		return
	}
	if s.o.Sleep(s.ctx, wait) != nil {
		s.finish(j, obs.PushFailed)
		return
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		s.finish(j, obs.PushFailed)
		return
	}
	s.queue = slices.Insert(s.queue, 0, j)
	s.dispatch()
	s.mu.Unlock()
}

func (s *Service) finish(j *job, result string) {
	s.o.Metrics.PushSent(j.d.kind, result)
	s.wg.Done()
}

// prepare readies j's first attempt, or returns its result (obs.Push*). Only a verified
// subscription receives anything but its verify push, and a pending one nothing else.
func (s *Service) prepare(j *job) (result string, ok bool) {
	t, err := s.o.Store.PushTarget(j.id)
	if err != nil {
		if !errors.Is(err, credentials.ErrPushNotFound) {
			s.log.Warn("push: the subscription cannot be opened", "subscription", j.id, "error", err)
		}
		return obs.PushGone, false
	}
	if want := credentials.PushVerified; j.d.msg.Type == model.PushTypeVerify && t.State != credentials.PushPending ||
		j.d.msg.Type != model.PushTypeVerify && t.State != want {
		return obs.PushUnverified, false
	}
	if s.o.Secrets != nil {
		s.o.Secrets.AddString(t.Endpoint)
	}
	log := s.log.With("subscription", j.id, "host", t.EndpointHost, "kind", j.d.kind)
	if !s.allowSend(j.id) {
		log.Warn("push: the subscription reached 60 pushes this hour; this one is dropped")
		return obs.PushLimited, false
	}
	body, req, err := s.request(t, j.d)
	if err != nil {
		log.Warn("push: the push could not be built", "error", err)
		return obs.PushFailed, false
	}
	j.prepared, j.t, j.log, j.body, j.req = true, t, log, body, req
	return "", true
}

// attempt posts j once. It returns the result, or retry with the wait before the next attempt.
func (s *Service) attempt(j *job) (result string, wait time.Duration, retry bool) {
	if !j.prepared {
		if result, ok := s.prepare(j); !ok {
			return result, 0, false
		}
	}
	log := j.log
	status, retryAfter, err := s.post(s.ctx, j.t.Endpoint, j.body, j.req)
	switch {
	case err == nil && status >= 200 && status < 300:
		return obs.PushOK, 0, false
	case err == nil && (status == http.StatusNotFound || status == http.StatusGone):
		if _, _, derr := s.o.Store.DeletePushSubscription(j.id); derr != nil {
			log.Warn("push: the expired subscription could not be deleted from credentials.yaml", "error", derr)
		}
		s.mu.Lock()
		delete(s.sent, j.id)
		delete(s.warned, j.id)
		s.mu.Unlock()
		log.Info("push: the push service says the subscription expired; it was deleted", "status", status)
		return obs.PushGone, 0, false
	case errors.Is(err, netguard.ErrRefused):
		s.warnOnce(log, j.id, "push: the endpoint's address is neither global nor in push.allowPrivate; nothing was sent", "error", err)
		return obs.PushRefused, 0, false
	case s.ctx.Err() != nil:
		return obs.PushFailed, 0, false
	case errors.Is(err, netguard.ErrRedirect) || certError(err):
		s.warnOnce(log, j.id, "push: the push service redirected or failed TLS verification; nothing was sent, the subscription is kept", "error", err)
		return obs.PushRejected, 0, false
	case err == nil && status != http.StatusTooManyRequests && status < 500:
		s.warnOnce(log, j.id, "push: the push service refused the push; the subscription is kept", "status", status)
		return obs.PushRejected, 0, false
	}
	// 429, 5xx (507 included) or no answer: try again, up to Retries times.
	if j.attempt == Retries {
		if err != nil {
			log.Warn("push: the push service did not answer after retries", "error", err)
		} else {
			log.Warn("push: the push service did not take the push after retries", "status", status)
		}
		return obs.PushFailed, 0, false
	}
	wait = Backoff[j.attempt]
	if retryAfter > 0 {
		wait = min(retryAfter, MaxRetryAfter)
	}
	j.attempt++
	return "", wait, true
}

// certError reports whether err is a TLS certificate failure, which a retry does not fix.
func certError(err error) bool {
	var verification *tls.CertificateVerificationError
	var authority x509.UnknownAuthorityError
	var hostname x509.HostnameError
	var invalid x509.CertificateInvalidError
	return errors.As(err, &verification) || errors.As(err, &authority) || errors.As(err, &hostname) || errors.As(err, &invalid)
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
	s.prune(now)
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

// prune drops, at most once a minute, the subscriptions with no send in the last hour from
// sent and those with no WARN in the last warnEvery from warned. s.mu is held.
func (s *Service) prune(now time.Time) {
	if now.Sub(s.pruned) < time.Minute {
		return
	}
	s.pruned = now
	for id, times := range s.sent {
		if len(times) == 0 || now.Sub(times[len(times)-1]) >= time.Hour {
			delete(s.sent, id)
		}
	}
	for id, last := range s.warned {
		if now.Sub(last) >= warnEvery {
			delete(s.warned, id)
		}
	}
}

// warnOnce logs msg for subscription id at most once an hour.
func (s *Service) warnOnce(log *slog.Logger, id, msg string, args ...any) {
	now := s.o.Now()
	s.mu.Lock()
	s.prune(now)
	last, seen := s.warned[id]
	if seen && now.Sub(last) < warnEvery {
		s.mu.Unlock()
		return
	}
	s.warned[id] = now
	s.mu.Unlock()
	log.Warn(msg, args...)
}
