package push

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/TechDufus/dash/internal/config"
	"github.com/TechDufus/dash/internal/credentials"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/netguard"
	"github.com/TechDufus/dash/internal/obs"
	"github.com/TechDufus/dash/internal/secret"
)

// fakeService is a push service over TLS: it records each request and answers with the next
// status of its script (the last one repeats).
type fakeService struct {
	*httptest.Server
	mu       sync.Mutex
	statuses []int
	header   http.Header // added to every answer
	got      []*recorded
}

type recorded struct {
	path   string
	header http.Header
	body   []byte
}

func newFakeService(t *testing.T, statuses ...int) *fakeService {
	t.Helper()
	f := &fakeService{statuses: statuses, header: http.Header{}}
	f.Server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.got = append(f.got, &recorded{path: r.URL.Path, header: r.Header.Clone(), body: body})
		status := http.StatusCreated
		if len(f.statuses) > 0 {
			status = f.statuses[0]
			if len(f.statuses) > 1 {
				f.statuses = f.statuses[1:]
			}
		}
		for k, v := range f.header {
			w.Header()[k] = v
		}
		f.mu.Unlock()
		w.WriteHeader(status)
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeService) requests() []*recorded {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*recorded(nil), f.got...)
}

// device is a subscriber: its keys, and its subscription in the store.
type device struct {
	key      *ecdh.PrivateKey
	auth     []byte
	id       string
	endpoint string
}

// open decrypts a recorded push as the device does.
func (d device) open(t *testing.T, r *recorded) model.PushMessage {
	t.Helper()
	plain, err := Decrypt(d.key, d.auth, r.body)
	if err != nil {
		t.Fatalf("the device cannot decrypt the push: %v", err)
	}
	var m model.PushMessage
	if err := json.Unmarshal(plain, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// env is a Service on an in-memory store with a fake clock, instant retries and a log.
type env struct {
	t       *testing.T
	store   *credentials.Store
	svc     *Service
	metrics *obs.Metrics
	logs    *bytes.Buffer
	mu      sync.Mutex
	now     time.Time
	waits   []time.Duration
}

func newEnv(t *testing.T, f *fakeService, allow config.PushAllow) *env {
	t.Helper()
	store, err := credentials.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	raw := make([]byte, 32)
	_, _ = rand.Read(raw)
	k, err := secret.ParseKey(base64.StdEncoding.EncodeToString(raw))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.OpenPush(secret.NewKeyring(k, secret.Key{})); err != nil {
		t.Fatal(err)
	}
	e := &env{t: t, store: store, metrics: obs.New("test", nil, nil), logs: &bytes.Buffer{}, now: time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)}
	pool := f.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs
	e.svc = New(Options{
		Store: store, Contact: "mailto:ops@example.com", Allow: allow, PublicURL: "https://dash.example.com",
		Metrics: e.metrics, Log: slog.New(slog.NewTextHandler(syncWriter{e}, &slog.HandlerOptions{Level: slog.LevelDebug})),
		Secrets: secret.NewRegistry(), Now: e.clock, RootCAs: pool,
		Sleep: func(_ context.Context, d time.Duration) error {
			e.mu.Lock()
			e.waits = append(e.waits, d)
			e.mu.Unlock()
			return nil
		},
	})
	t.Cleanup(e.svc.Close)
	return e
}

type syncWriter struct{ e *env }

func (w syncWriter) Write(p []byte) (int, error) {
	w.e.mu.Lock()
	defer w.e.mu.Unlock()
	return w.e.logs.Write(p)
}

func (e *env) clock() time.Time {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.now
}

func (e *env) advance(d time.Duration) {
	e.mu.Lock()
	e.now = e.now.Add(d)
	e.mu.Unlock()
}

func (e *env) log() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.logs.String()
}

// loopback admits the fake service, which listens on 127.0.0.1.
var loopback = config.PushAllow{Prefixes: []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}}

// subscribe registers a device of user at the fake service, verified unless pending.
func (e *env) subscribe(f *fakeService, user string, pending bool, mod func(*credentials.PushInput)) device {
	e.t.Helper()
	key, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		e.t.Fatal(err)
	}
	auth := make([]byte, 16)
	_, _ = rand.Read(auth)
	d := device{key: key, auth: auth, endpoint: f.URL + "/push/secret-capability-" + strings.ToLower(rand.Text())}
	in := credentials.PushInput{
		User: user, Kind: credentials.PushKindWebPush, DeviceName: "Phone", Approvals: true, Attention: "warn",
		Endpoint: d.endpoint, EndpointHost: "127.0.0.1", P256dh: b64.EncodeToString(key.PublicKey().Bytes()), Auth: b64.EncodeToString(auth),
	}
	if mod != nil {
		mod(&in)
	}
	sub, _, err := e.store.AddPushSubscription(in, "ABCDEFGH", e.clock())
	if err != nil {
		e.t.Fatal(err)
	}
	d.id = sub.ID
	if !pending {
		if _, err := e.store.VerifyPushSubscription(user, sub.ID, "ABCDEFGH", e.clock()); err != nil {
			e.t.Fatal(err)
		}
	}
	return d
}

// sent is dash_push_sent_total{kind,result}.
func (e *env) sent(kind, result string) float64 {
	e.t.Helper()
	w := httptest.NewRecorder()
	e.metrics.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	prefix := `dash_push_sent_total{kind="` + kind + `",result="` + result + `"} `
	for _, line := range strings.Split(w.Body.String(), "\n") {
		if v, ok := strings.CutPrefix(line, prefix); ok {
			var n float64
			if err := json.Unmarshal([]byte(v), &n); err != nil {
				e.t.Fatal(err)
			}
			return n
		}
	}
	e.t.Fatalf("no series %s", prefix)
	return 0
}

// A pending subscription receives its verify push and nothing else; a verified one never gets a
// verify push again.
func TestUnverifiedSubscriptionReceivesNothing(t *testing.T) {
	f := newFakeService(t)
	e := newEnv(t, f, loopback)
	d := e.subscribe(f, "alice", true, nil)
	if err := e.svc.SendTest(d.id); !errors.Is(err, ErrNotVerified) {
		t.Errorf("SendTest to a pending subscription: %v", err)
	}
	if n := e.svc.Notify("alice", model.PushMessage{Type: model.PushTypeApproval, ID: "appr_1", Title: "t", Body: "b", URL: "https://dash.example.com/approve/appr_1"}); n != 0 {
		t.Errorf("Notify queued %d pushes to a pending subscription", n)
	}
	e.svc.Attention(d.id, model.PushMessage{Type: model.PushTypeAttention, ID: "item", Title: "t", Body: "b", URL: "https://dash.example.com/"}, true)
	e.svc.Wait()
	if got := len(f.requests()); got != 0 {
		t.Fatalf("a pending subscription received %d pushes", got)
	}
	if e.sent(obs.PushAttention, obs.PushUnverified) != 1 {
		t.Error("the refused attention push is not counted as unverified")
	}

	e.svc.SendVerify(d.id, "K7M2-PQ9X")
	e.svc.Wait()
	reqs := f.requests()
	if len(reqs) != 1 {
		t.Fatalf("verify: %d requests, want 1", len(reqs))
	}
	if m := d.open(t, reqs[0]); m.Type != model.PushTypeVerify || m.Code != "K7M2-PQ9X" || m.ID != d.id {
		t.Errorf("verify push = %+v", m)
	}
	if _, err := e.store.VerifyPushSubscription("alice", d.id, "ABCDEFGH", e.clock()); err != nil {
		t.Fatal(err)
	}
	e.svc.SendVerify(d.id, "AAAAAAAA")
	e.svc.Wait()
	if len(f.requests()) != 1 {
		t.Error("a verified subscription received a verify push")
	}
}

// A device the dash app registered with its OAuth grant receives pushes while that grant is
// active, and nothing once it expires, before any prune has turned the row stale.
func TestGrantDeviceNeedsAnActiveGrantAtSendTime(t *testing.T) {
	f := newFakeService(t)
	e := newEnv(t, f, loopback)
	id, err := credentials.NewGrantID()
	if err != nil {
		t.Fatal(err)
	}
	_, hash, err := credentials.NewRefreshToken(id)
	if err != nil {
		t.Fatal(err)
	}
	now := e.clock()
	if err := e.store.AddGrant(credentials.Grant{ID: id, User: "alice", ClientID: "dash-android", ClientKind: "builtin", Audience: "api",
		Scopes: []string{"read", "push"}, RefreshHash: hash, CreatedAt: now, ExpiresAt: now.Add(credentials.GrantLifetime), RefreshedAt: now}); err != nil {
		t.Fatal(err)
	}
	d := e.subscribe(f, "alice", false, func(in *credentials.PushInput) { in.GrantID = id })
	msg := model.PushMessage{Type: model.PushTypeAttention, ID: "item", Title: "t", Body: "b", URL: "https://dash.example.com/"}
	e.svc.Attention(d.id, msg, true)
	e.svc.Wait()
	if got := len(f.requests()); got != 1 {
		t.Fatalf("with the grant active: %d pushes, want 1", got)
	}
	e.advance(credentials.GrantIdle + time.Minute)
	if sub, _ := e.store.PushSubscriptionByID(d.id); sub.State != credentials.PushVerified {
		t.Fatalf("the row is %s before any prune, want verified", sub.State)
	}
	e.svc.Attention(d.id, msg, true)
	e.svc.Wait()
	if got := len(f.requests()); got != 1 {
		t.Errorf("after the grant expired: %d pushes, want still 1", got)
	}
	if e.sent(obs.PushAttention, obs.PushUnverified) != 1 {
		t.Error("the refused push is not counted as unverified")
	}
}

// One approval request sends one push per verified device that takes approvals; the request
// carries the RFC 8030 headers and a VAPID signature for the endpoint's origin, and only the
// device decrypts it.
func TestApprovalSendsOnePushPerVerifiedDevice(t *testing.T) {
	f := newFakeService(t)
	e := newEnv(t, f, loopback)
	phone := e.subscribe(f, "alice", false, nil)
	laptop := e.subscribe(f, "alice", false, nil)
	e.subscribe(f, "alice", true, nil)                                                       // pending
	e.subscribe(f, "alice", false, func(in *credentials.PushInput) { in.Approvals = false }) // no approvals
	e.subscribe(f, "bob", false, nil)                                                        // someone else
	msg := model.PushMessage{Type: model.PushTypeApproval, ID: "appr_1", Title: "Restart api", Body: "An agent asks", URL: "https://dash.example.com/approve/appr_1"}
	if n := e.svc.Notify("alice", msg); n != 2 {
		t.Errorf("Notify queued %d, want 2", n)
	}
	e.svc.Wait()
	reqs := f.requests()
	if len(reqs) != 2 {
		t.Fatalf("%d pushes, want one per verified device that takes approvals", len(reqs))
	}
	byPath := map[string]device{}
	for _, d := range []device{phone, laptop} {
		byPath[strings.TrimPrefix(d.endpoint, f.URL)] = d
	}
	for _, r := range reqs {
		d, ok := byPath[r.path]
		if !ok {
			t.Fatalf("a push went to %s", r.path)
		}
		delete(byPath, r.path)
		if m := d.open(t, r); m.Type != model.PushTypeApproval || m.ID != "appr_1" || m.URL != msg.URL || m.TS.IsZero() {
			t.Errorf("payload = %+v", m)
		}
		for h, want := range map[string]string{"Content-Encoding": "aes128gcm", "Ttl": "600", "Urgency": "high"} {
			if got := r.header.Get(h); got != want {
				t.Errorf("%s = %q, want %q", h, got, want)
			}
		}
		claims, _ := verifyVAPID(t, r.header.Get("Authorization"))
		if claims["aud"] != f.URL || claims["sub"] != "mailto:ops@example.com" {
			t.Errorf("VAPID claims = %v", claims)
		}
		if r.header.Get("X-Unifiedpush") != "" || r.header.Get("Topic") != "" {
			t.Error("an approval push carries X-UnifiedPush or a Topic")
		}
	}
	if e.sent(obs.PushApproval, obs.PushOK) != 2 {
		t.Error("the pushes are not counted")
	}
}

// Attention pushes last an hour, are urgent only when critical, and carry a 22-character Topic
// derived from the item.
func TestAttentionHeaders(t *testing.T) {
	f := newFakeService(t)
	e := newEnv(t, f, loopback)
	d := e.subscribe(f, "alice", false, nil)
	e.svc.Attention(d.id, model.PushMessage{Type: model.PushTypeAttention, ID: "kubernetes:node/n1", Title: "t", Body: "b", URL: "https://dash.example.com/"}, false)
	e.svc.Wait()
	r := f.requests()[0]
	if r.header.Get("Ttl") != "3600" || r.header.Get("Urgency") != "normal" || len(r.header.Get("Topic")) != 22 {
		t.Errorf("headers = %v", r.header)
	}
	if _, err := b64.DecodeString(r.header.Get("Topic")); err != nil {
		t.Errorf("Topic is not base64url: %v", err)
	}
}

// An endpoint that resolves to a private or loopback address is refused at dial time unless
// push.allowPrivate admits it, by address or by host name.
func TestPrivateEndpointIsRefusedUnlessAllowed(t *testing.T) {
	f := newFakeService(t)
	e := newEnv(t, f, config.PushAllow{})
	d := e.subscribe(f, "alice", true, nil)
	e.svc.SendVerify(d.id, "ABCDEFGH")
	e.svc.Wait()
	if len(f.requests()) != 0 {
		t.Fatal("a loopback endpoint was reached without push.allowPrivate")
	}
	if e.sent(obs.PushVerify, obs.PushRefused) != 1 {
		t.Error("the refusal is not counted")
	}
	if _, ok := e.store.PushSubscriptionByID(d.id); !ok {
		t.Error("a refused subscription was deleted")
	}

	for name, allow := range map[string]config.PushAllow{"by address": loopback, "by host": {Hosts: []string{"localhost"}}} {
		g := newFakeService(t)
		e := newEnv(t, g, allow)
		d := e.subscribe(g, "alice", true, func(in *credentials.PushInput) {
			if name == "by host" {
				in.Endpoint = strings.Replace(in.Endpoint, "127.0.0.1", "localhost", 1)
			}
		})
		if name == "by host" {
			// The fake's certificate names 127.0.0.1 and example.com, not localhost: what counts is
			// that the connection was allowed, which a TLS failure after the dial proves.
			e.svc.SendVerify(d.id, "ABCDEFGH")
			e.svc.Wait()
			if e.sent(obs.PushVerify, obs.PushRefused) != 0 {
				t.Errorf("%s: refused", name)
			}
			continue
		}
		e.svc.SendVerify(d.id, "ABCDEFGH")
		e.svc.Wait()
		if len(g.requests()) != 1 {
			t.Errorf("%s: %d requests, want 1", name, len(g.requests()))
		}
	}
}

// A 404 or a 410 deletes the subscription; another 4xx keeps it, with one WARN an hour.
func TestGoneDeletesTheSubscription(t *testing.T) {
	for _, status := range []int{http.StatusGone, http.StatusNotFound} {
		f := newFakeService(t, status)
		e := newEnv(t, f, loopback)
		d := e.subscribe(f, "alice", false, nil)
		if err := e.svc.SendTest(d.id); err != nil {
			t.Fatal(err)
		}
		e.svc.Wait()
		if _, ok := e.store.PushSubscriptionByID(d.id); ok {
			t.Errorf("%d: the subscription survived", status)
		}
		if e.sent(obs.PushTest, obs.PushGone) != 1 || len(e.waits) != 0 {
			t.Errorf("%d: not counted as gone, or retried %v", status, e.waits)
		}
	}

	f := newFakeService(t, http.StatusBadRequest)
	e := newEnv(t, f, loopback)
	d := e.subscribe(f, "alice", false, nil)
	for range 2 {
		_ = e.svc.SendTest(d.id)
		e.svc.Wait()
	}
	if _, ok := e.store.PushSubscriptionByID(d.id); !ok {
		t.Error("a 400 deleted the subscription")
	}
	if n := strings.Count(e.log(), "the push service refused the push"); n != 1 {
		t.Errorf("%d WARNs in the hour, want 1", n)
	}
	e.advance(time.Hour)
	_ = e.svc.SendTest(d.id)
	e.svc.Wait()
	if n := strings.Count(e.log(), "the push service refused the push"); n != 2 {
		t.Errorf("%d WARNs after an hour, want 2", n)
	}
	if e.sent(obs.PushTest, obs.PushRejected) != 3 || len(e.waits) != 0 {
		t.Errorf("rejected = %v, waits %v", e.sent(obs.PushTest, obs.PushRejected), e.waits)
	}
}

// 429 and 5xx are retried three times after 1, 4 and 16 seconds; Retry-After replaces the wait,
// up to 60 seconds.
func TestRetries(t *testing.T) {
	for _, tc := range []struct {
		name       string
		statuses   []int
		retryAfter string
		waits      []time.Duration
		result     string
	}{
		{"recovers", []int{503, 429, 201}, "", []time.Duration{time.Second, 4 * time.Second}, obs.PushOK},
		{"gives up", []int{500}, "", []time.Duration{time.Second, 4 * time.Second, 16 * time.Second}, obs.PushFailed},
		{"507", []int{507, 201}, "", []time.Duration{time.Second}, obs.PushOK},
		{"retry-after", []int{429, 201}, "7", []time.Duration{7 * time.Second}, obs.PushOK},
		{"retry-after capped", []int{429, 201}, "3600", []time.Duration{time.Minute}, obs.PushOK},
	} {
		f := newFakeService(t, tc.statuses...)
		if tc.retryAfter != "" {
			f.header.Set("Retry-After", tc.retryAfter)
		}
		e := newEnv(t, f, loopback)
		d := e.subscribe(f, "alice", false, nil)
		_ = e.svc.SendTest(d.id)
		e.svc.Wait()
		if !slicesEqual(e.waits, tc.waits) || e.sent(obs.PushTest, tc.result) != 1 {
			t.Errorf("%s: waits %v, want %v; %s counted %v", tc.name, e.waits, tc.waits, tc.result, e.sent(obs.PushTest, tc.result))
		}
		if want := len(tc.waits) + 1; len(f.requests()) != want {
			t.Errorf("%s: %d attempts, want %d", tc.name, len(f.requests()), want)
		}
	}
}

func slicesEqual(a, b []time.Duration) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// A subscription receives at most 60 pushes in any hour; the next one is dropped and counted.
func TestHourlyLimitPerSubscription(t *testing.T) {
	f := newFakeService(t)
	e := newEnv(t, f, loopback)
	d := e.subscribe(f, "alice", false, nil)
	other := e.subscribe(f, "alice", false, nil)
	for range HourlyLimit {
		_ = e.svc.SendTest(d.id)
		e.svc.Wait()
		e.advance(time.Second)
	}
	_ = e.svc.SendTest(d.id)
	_ = e.svc.SendTest(other.id)
	e.svc.Wait()
	if got := len(f.requests()); got != HourlyLimit+1 {
		t.Errorf("%d pushes, want %d to the full subscription and one to the other", got, HourlyLimit+1)
	}
	if e.sent(obs.PushTest, obs.PushLimited) != 1 {
		t.Error("the dropped push is not counted as limited")
	}
	e.advance(time.Hour - time.Duration(HourlyLimit)*time.Second + time.Second)
	_ = e.svc.SendTest(d.id)
	e.svc.Wait()
	if got := len(f.requests()); got != HourlyLimit+2 {
		t.Errorf("an hour after the first push: %d pushes, want one more", got)
	}
}

// The endpoint (a capability URL) is in no log line, whatever happens to the push.
func TestEndpointNeverLogged(t *testing.T) {
	for _, tc := range []struct {
		name  string
		allow config.PushAllow
		down  bool
		codes []int
	}{
		{"refused", config.PushAllow{}, false, nil},
		{"gone", loopback, false, []int{410}},
		{"rejected", loopback, false, []int{403}},
		{"failing", loopback, false, []int{502}},
		{"unreachable", loopback, true, nil},
	} {
		f := newFakeService(t, tc.codes...)
		e := newEnv(t, f, tc.allow)
		d := e.subscribe(f, "alice", false, nil)
		if tc.down {
			f.Close()
		}
		_ = e.svc.SendTest(d.id)
		e.svc.Wait()
		logs := e.log()
		if logs == "" {
			t.Errorf("%s: nothing was logged", tc.name)
		}
		path := strings.TrimPrefix(d.endpoint, f.URL)
		if strings.Contains(logs, path) || strings.Contains(logs, "secret-capability") {
			t.Errorf("%s: the endpoint is in the log:\n%s", tc.name, logs)
		}
	}
}

// A payload over 3 KiB has its body cut to fit, on a character boundary.
func TestPayloadIsCutTo3KiB(t *testing.T) {
	m := model.PushMessage{Type: model.PushTypeAttention, ID: "x", Title: "t", Body: strings.Repeat("é", 4000), URL: "https://dash.example.com/"}
	b, err := payloadOf(m)
	if err != nil || len(b) > MaxPayload {
		t.Fatalf("payload of %d bytes, %v", len(b), err)
	}
	var back model.PushMessage
	if err := json.Unmarshal(b, &back); err != nil || !strings.HasSuffix(back.Body, "…") || !strings.HasPrefix(back.Body, "éé") {
		t.Errorf("body = %q…, %v", back.Body[:10], err)
	}
}

func attentionMsg(id string) model.PushMessage {
	return model.PushMessage{Type: model.PushTypeAttention, ID: id, Title: "t", URL: "https://dash.example.com/"}
}

// A user whose ten devices answer 429 with Retry-After: 60 holds no slot while their retries
// wait, so another user's test push still goes out at once.
func TestOneUsersRetriesStallNoOneElse(t *testing.T) {
	slow := newFakeService(t, http.StatusTooManyRequests)
	slow.header.Set("Retry-After", "60")
	fast := newFakeService(t)
	e := newEnv(t, fast, loopback)
	release := make(chan struct{})
	e.svc.o.Sleep = func(ctx context.Context, _ time.Duration) error {
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	var devs []device
	for range credentials.MaxPushSubscriptions {
		devs = append(devs, e.subscribe(slow, "alice", false, nil))
	}
	for _, item := range []string{"a", "b", "c"} {
		for _, d := range devs {
			e.svc.Attention(d.id, attentionMsg(item), false)
		}
	}
	waitUntil(t, func() bool { return len(slow.requests()) >= parallel })
	bob := e.subscribe(fast, "bob", false, nil)
	if err := e.svc.SendTest(bob.id); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, func() bool { return len(fast.requests()) == 1 })
	close(release)
	e.svc.Wait()
	if got := e.sent(obs.PushTest, obs.PushOK); got != 1 {
		t.Errorf("bob's test push: ok = %v", got)
	}
}

// At most perHost pushes are in flight to one host. Past MaxQueue waiting pushes the oldest
// that is not an approval is dropped and counted; the queued approval still goes out first.
func TestQueueIsBoundedAndKeepsApprovals(t *testing.T) {
	gate := make(chan struct{})
	var mu sync.Mutex
	inFlight, most := 0, 0
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		mu.Lock()
		inFlight++
		most = max(most, inFlight)
		mu.Unlock()
		<-gate
		mu.Lock()
		inFlight--
		mu.Unlock()
		w.WriteHeader(http.StatusCreated)
	}))
	t.Cleanup(srv.Close)
	f := &fakeService{Server: srv, header: http.Header{}}
	e := newEnv(t, f, loopback)
	d := e.subscribe(f, "alice", false, nil)
	for range perHost + 2 {
		e.svc.Attention(d.id, attentionMsg("first"), false)
	}
	waitUntil(t, func() bool { mu.Lock(); defer mu.Unlock(); return inFlight == perHost })
	e.svc.Notify("alice", model.PushMessage{Type: model.PushTypeApproval, ID: "approval", Title: "approve?", URL: "https://dash.example.com/"})
	for range MaxQueue + 5 {
		e.svc.Attention(d.id, attentionMsg("later"), false)
	}
	// Queued: 2 + 1 + 1029 = 1032, so 8 of the attention pushes were dropped.
	if got := e.sent(obs.PushAttention, obs.PushDropped); got != 8 {
		t.Errorf("dropped = %v, want 8", got)
	}
	close(gate)
	e.svc.Wait()
	if got := e.sent(obs.PushApproval, obs.PushOK); got != 1 {
		t.Errorf("approval ok = %v, want 1", got)
	}
	mu.Lock()
	defer mu.Unlock()
	if most != perHost {
		t.Errorf("at most %d in flight to one host, want %d", most, perHost)
	}
}

// A redirect and a certificate the trust does not take are rejections: one attempt, no retry,
// and the subscription is kept.
func TestRedirectAndBadCertificateAreRejected(t *testing.T) {
	f := newFakeService(t, http.StatusFound)
	f.header.Set("Location", "https://push.example.net/elsewhere")
	e := newEnv(t, f, loopback)
	d := e.subscribe(f, "alice", false, nil)
	if err := e.svc.SendTest(d.id); err != nil {
		t.Fatal(err)
	}
	e.svc.Wait()
	if n := len(f.requests()); n != 1 || e.sent(obs.PushTest, obs.PushRejected) != 1 || len(e.waits) != 0 {
		t.Errorf("redirect: %d requests, rejected %v, waits %v", n, e.sent(obs.PushTest, obs.PushRejected), e.waits)
	}

	ok := newFakeService(t)
	e2 := newEnv(t, ok, loopback)
	e2.svc.client = netguard.NewClient(netguard.Options{PrivatePrefixes: loopback.Prefixes, Timeout: sendTimeout, RootCAs: x509.NewCertPool()})
	d2 := e2.subscribe(ok, "alice", false, nil)
	if err := e2.svc.SendTest(d2.id); err != nil {
		t.Fatal(err)
	}
	e2.svc.Wait()
	if got := e2.sent(obs.PushTest, obs.PushRejected); got != 1 || len(e2.waits) != 0 {
		t.Errorf("bad certificate: rejected %v, waits %v", got, e2.waits)
	}
	if _, kept := e2.store.PushSubscriptionByID(d2.id); !kept {
		t.Error("the subscription was deleted")
	}
	if !strings.Contains(e2.log(), "failed TLS verification") {
		t.Errorf("no WARN: %s", e2.log())
	}
}

// The failure WARN after the retries of a 429 carries the status and no empty error.
func TestRetryWarnHasNoEmptyError(t *testing.T) {
	f := newFakeService(t, http.StatusTooManyRequests)
	e := newEnv(t, f, loopback)
	d := e.subscribe(f, "alice", false, nil)
	if err := e.svc.SendTest(d.id); err != nil {
		t.Fatal(err)
	}
	e.svc.Wait()
	if l := e.log(); !strings.Contains(l, "status=429") || strings.Contains(l, "error=<nil>") {
		t.Errorf("log = %s", l)
	}
}

// The per-subscription bookkeeping forgets a subscription an hour after its last send, and at
// once when the push service says it is gone.
func TestSendBookkeepingIsPruned(t *testing.T) {
	f := newFakeService(t, http.StatusCreated, http.StatusGone, http.StatusCreated)
	e := newEnv(t, f, loopback)
	idle := e.subscribe(f, "alice", false, nil)
	gone := e.subscribe(f, "alice", false, nil)
	other := e.subscribe(f, "alice", false, nil)
	send := func(d device) {
		if err := e.svc.SendTest(d.id); err != nil {
			t.Fatal(err)
		}
		e.svc.Wait()
	}
	keys := func() (sent, warned []string) {
		e.svc.mu.Lock()
		defer e.svc.mu.Unlock()
		for id := range e.svc.sent {
			sent = append(sent, id)
		}
		for id := range e.svc.warned {
			warned = append(warned, id)
		}
		return sent, warned
	}
	send(idle)
	send(gone)
	if sent, _ := keys(); len(sent) != 1 || sent[0] != idle.id {
		t.Errorf("after the 410: sent keys %v, want only the idle one", sent)
	}
	e.advance(time.Hour)
	send(other)
	if sent, warned := keys(); len(sent) != 1 || sent[0] != other.id || len(warned) != 0 {
		t.Errorf("an hour later: sent keys %v, warned %v, want only the new one", sent, warned)
	}
}

// A push after Close is counted as failed and starts nothing.
func TestEnqueueAfterClose(t *testing.T) {
	f := newFakeService(t)
	e := newEnv(t, f, loopback)
	d := e.subscribe(f, "alice", false, nil)
	e.svc.Close()
	if err := e.svc.SendTest(d.id); err != nil {
		t.Fatal(err)
	}
	e.svc.Wait()
	if len(f.requests()) != 0 || e.sent(obs.PushTest, obs.PushFailed) != 1 {
		t.Errorf("%d requests, failed %v", len(f.requests()), e.sent(obs.PushTest, obs.PushFailed))
	}
}

// CheckEndpoint refuses a host the sender would never reach: a refused address, or a name whose
// addresses are all refused. A name with one reachable address, a name in push.allowPrivate and
// a failed lookup pass.
func TestCheckEndpoint(t *testing.T) {
	f := newFakeService(t)
	e := newEnv(t, f, config.PushAllow{Hosts: []string{"ntfy.example.com"}, Prefixes: loopback.Prefixes})
	dns := map[string][]netip.Addr{
		"private.example.com": {netip.MustParseAddr("10.0.0.5"), netip.MustParseAddr("169.254.1.1")},
		"mixed.example.com":   {netip.MustParseAddr("10.0.0.5"), netip.MustParseAddr("1.1.1.1")},
		"ntfy.example.com":    {netip.MustParseAddr("10.0.0.5")},
	}
	e.svc.o.Resolve = func(_ context.Context, host string) ([]netip.Addr, error) {
		if a, ok := dns[host]; ok {
			return a, nil
		}
		return nil, errors.New("no such host")
	}
	for host, refused := range map[string]bool{
		"10.0.0.1":            true,
		"169.254.169.254":     true,
		"[::1]":               true,
		"127.0.0.1":           false,
		"private.example.com": true,
		"mixed.example.com":   false,
		"ntfy.example.com":    false,
		"unknown.example.com": false,
	} {
		if err := e.svc.CheckEndpoint(t.Context(), host); (err != nil) != refused || err != nil && !errors.Is(err, ErrEndpointRefused) {
			t.Errorf("%s: %v, want refused %v", host, err, refused)
		}
	}
}

// Every endpoint dash opens is registered with the secret scrubber, so an error that wraps one
// (a log line, an audit error) comes out without it.
func TestEndpointIsRegisteredWithTheScrubber(t *testing.T) {
	f := newFakeService(t)
	e := newEnv(t, f, loopback)
	d := e.subscribe(f, "alice", false, nil)
	if err := e.svc.SendTest(d.id); err != nil {
		t.Fatal(err)
	}
	e.svc.Wait()
	wrapped := errors.Join(errors.New("push"), errors.New(`Post "`+d.endpoint+`": connection reset`)).Error()
	if got := e.svc.o.Secrets.Scrub(wrapped); strings.Contains(got, "secret-capability-") {
		t.Errorf("scrubbed = %q", got)
	}
}

// A stored endpoint that is not https is never posted to.
func TestDeliverRefusesPlainHTTP(t *testing.T) {
	tls := newFakeService(t)
	plain := &fakeService{Server: httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("a plain http request reached %s", r.URL.Path)
		w.WriteHeader(http.StatusCreated)
	})), header: http.Header{}}
	t.Cleanup(plain.Close)
	e := newEnv(t, tls, loopback)
	d := e.subscribe(plain, "alice", false, nil)
	if err := e.svc.SendTest(d.id); err != nil {
		t.Fatal(err)
	}
	e.svc.Wait()
	if got := e.sent(obs.PushTest, obs.PushFailed); got != 1 {
		t.Errorf("failed = %v, want 1", got)
	}
}

// gatedService is a push service that answers nothing until open is called, and counts the
// requests in flight.
type gatedService struct {
	*fakeService
	gate           chan struct{}
	mu             sync.Mutex
	inFlight, most int
}

func newGatedService(t *testing.T) *gatedService {
	t.Helper()
	g := &gatedService{gate: make(chan struct{})}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		g.mu.Lock()
		g.inFlight++
		g.most = max(g.most, g.inFlight)
		g.mu.Unlock()
		<-g.gate
		g.mu.Lock()
		g.inFlight--
		g.mu.Unlock()
		w.WriteHeader(http.StatusCreated)
	}))
	g.fakeService = &fakeService{Server: srv, header: http.Header{}}
	t.Cleanup(srv.Close)
	t.Cleanup(g.open)
	return g
}

func (g *gatedService) open() {
	select {
	case <-g.gate:
	default:
		close(g.gate)
	}
}

func (g *gatedService) counts() (inFlight, most int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.inFlight, g.most
}

// onHost registers the device under push-service host h (the fake listens on one address).
func onHost(h string) func(*credentials.PushInput) {
	return func(in *credentials.PushInput) { in.EndpointHost = h }
}

// Two users whose eight devices each hang hold at most 4 slots each, so a third user's push
// still goes out at once.
func TestPerUserCapKeepsOthersMoving(t *testing.T) {
	g := newGatedService(t)
	fast := newFakeService(t)
	e := newEnv(t, fast, loopback)
	for _, user := range []string{"alice", "bob"} {
		for i := range 8 {
			d := e.subscribe(g.fakeService, user, false, onHost(fmt.Sprintf("%s-%d.example.net", user, i)))
			e.svc.Attention(d.id, attentionMsg("x"), false)
		}
	}
	waitUntil(t, func() bool { n, _ := g.counts(); return n == 8 })
	carol := e.subscribe(fast, "carol", false, onHost("fast.example.net"))
	if err := e.svc.SendTest(carol.id); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for len(fast.requests()) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("carol's push waited behind alice's and bob's")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if _, most := g.counts(); most != 8 {
		t.Errorf("at most %d in flight for two users, want 8 (4 each)", most)
	}
	g.open()
	e.svc.Wait()
}

// Two users on one push-service host share its 4 slots.
func TestPerHostCapIsFour(t *testing.T) {
	g := newGatedService(t)
	e := newEnv(t, g.fakeService, loopback)
	for _, user := range []string{"alice", "bob"} {
		for range 4 {
			d := e.subscribe(g.fakeService, user, false, onHost("one.example.net"))
			e.svc.Attention(d.id, attentionMsg("x"), false)
		}
	}
	waitUntil(t, func() bool { n, _ := g.counts(); return n == 4 })
	time.Sleep(50 * time.Millisecond) // room for a fifth, which must not come
	if _, most := g.counts(); most != 4 {
		t.Errorf("at most %d in flight to one host, want 4", most)
	}
	g.open()
	e.svc.Wait()
}

// At most 16 pushes are in flight, whatever the hosts and users.
func TestGlobalCapIsSixteen(t *testing.T) {
	g := newGatedService(t)
	e := newEnv(t, g.fakeService, loopback)
	for u := range 5 {
		for i := range 4 {
			d := e.subscribe(g.fakeService, fmt.Sprintf("user%d", u), false, onHost(fmt.Sprintf("h%d-%d.example.net", u, i)))
			e.svc.Attention(d.id, attentionMsg("x"), false)
		}
	}
	waitUntil(t, func() bool { n, _ := g.counts(); return n == 16 })
	time.Sleep(50 * time.Millisecond)
	if _, most := g.counts(); most != 16 {
		t.Errorf("at most %d in flight, want 16", most)
	}
	g.open()
	e.svc.Wait()
}

// A queue full of approvals still takes a new approval; a new attention push is the one dropped.
func TestFullQueueOfApprovalsKeepsApprovals(t *testing.T) {
	g := newGatedService(t)
	e := newEnv(t, g.fakeService, loopback)
	d := e.subscribe(g.fakeService, "alice", false, nil)
	for range 4 {
		e.svc.Attention(d.id, attentionMsg("x"), false)
	}
	waitUntil(t, func() bool { n, _ := g.counts(); return n == 4 })
	approval := model.PushMessage{Type: model.PushTypeApproval, ID: "a", Title: "approve?", URL: "https://dash.example.com/"}
	for range MaxQueue + 1 {
		e.svc.Notify("alice", approval)
	}
	e.svc.Attention(d.id, attentionMsg("late"), false)
	e.svc.mu.Lock()
	queued := len(e.svc.queue)
	e.svc.mu.Unlock()
	if queued != MaxQueue+1 || e.sent(obs.PushApproval, obs.PushDropped) != 0 || e.sent(obs.PushAttention, obs.PushDropped) != 1 {
		t.Errorf("queued %d, approvals dropped %v, attention dropped %v; want %d, 0, 1",
			queued, e.sent(obs.PushApproval, obs.PushDropped), e.sent(obs.PushAttention, obs.PushDropped), MaxQueue+1)
	}
	g.open()
	e.svc.Wait()
}

// Close closes the sender's idle connections: the push service sees them end.
func TestCloseClosesIdleConnections(t *testing.T) {
	var mu sync.Mutex
	open := 0
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusCreated)
	}))
	srv.Config.ConnState = func(_ net.Conn, st http.ConnState) {
		mu.Lock()
		defer mu.Unlock()
		switch st {
		case http.StateNew:
			open++
		case http.StateClosed, http.StateHijacked:
			open--
		}
	}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	f := &fakeService{Server: srv, header: http.Header{}}
	e := newEnv(t, f, loopback)
	d := e.subscribe(f, "alice", false, nil)
	if err := e.svc.SendTest(d.id); err != nil {
		t.Fatal(err)
	}
	e.svc.Wait()
	mu.Lock()
	before := open
	mu.Unlock()
	if before == 0 {
		t.Fatal("no connection stayed open after the push")
	}
	e.svc.Close()
	waitUntil(t, func() bool { mu.Lock(); defer mu.Unlock(); return open == 0 })
}
