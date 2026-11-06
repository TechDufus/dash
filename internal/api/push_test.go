package api

import (
	"bytes"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/TechDufus/dash/internal/audit"
	"github.com/TechDufus/dash/internal/auth"
	"github.com/TechDufus/dash/internal/config"
	"github.com/TechDufus/dash/internal/credentials"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/push"
	"github.com/TechDufus/dash/internal/secret"
)

var b64url = base64.RawURLEncoding

// pushService is a fake push service over TLS that records each push.
type pushService struct {
	*httptest.Server
	mu     sync.Mutex
	bodies map[string][][]byte // path -> bodies
}

func newPushService(t *testing.T) *pushService {
	t.Helper()
	f := &pushService{bodies: map[string][][]byte{}}
	f.Server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.bodies[r.URL.Path] = append(f.bodies[r.URL.Path], b)
		f.mu.Unlock()
		w.WriteHeader(http.StatusCreated)
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *pushService) received(path string) [][]byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][]byte(nil), f.bodies[path]...)
}

// pushDevice is a browser's subscription: its keys and endpoint at the fake service.
type pushDevice struct {
	key      *ecdh.PrivateKey
	auth     []byte
	path     string
	endpoint string
}

func newPushDevice(t *testing.T, f *pushService) pushDevice {
	t.Helper()
	key, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	auth := make([]byte, 16)
	_, _ = rand.Read(auth)
	path := "/wpush/v2/capability-" + strings.ToLower(rand.Text())
	return pushDevice{key: key, auth: auth, path: path, endpoint: f.URL + path}
}

func (d pushDevice) request(name string) model.PushSubscriptionRequest {
	return model.PushSubscriptionRequest{
		Endpoint: d.endpoint, Kind: model.PushKindWebPush, DeviceName: name,
		Keys:   model.PushKeys{P256dh: b64url.EncodeToString(d.key.PublicKey().Bytes()), Auth: b64url.EncodeToString(d.auth)},
		Notify: model.PushNotify{Approvals: true, Attention: model.PushAttentionCrit},
	}
}

// messages are the pushes the device received, decrypted.
func (d pushDevice) messages(t *testing.T, f *pushService) []model.PushMessage {
	t.Helper()
	var out []model.PushMessage
	for _, b := range f.received(d.path) {
		plain, err := push.Decrypt(d.key, d.auth, b)
		if err != nil {
			t.Fatalf("the device cannot decrypt a push: %v", err)
		}
		var m model.PushMessage
		if err := json.Unmarshal(plain, &m); err != nil {
			t.Fatal(err)
		}
		out = append(out, m)
	}
	return out
}

// pushEnv is a proxy-mode server with push on: a credential store in a data dir opened with a
// secret key, the sender aimed at a fake service on the loopback (allow-listed), a real audit
// log, and the token fixture's resolver.
type pushEnv struct {
	*tokenEnv
	svc *push.Service
	f   *pushService
}

func newPushEnv(t *testing.T, mods ...func(*Deps)) *pushEnv {
	t.Helper()
	f := newPushService(t)
	e := &pushEnv{f: f}
	raw := make([]byte, 32)
	_, _ = rand.Read(raw)
	k, err := secret.ParseKey(base64.StdEncoding.EncodeToString(raw))
	if err != nil {
		t.Fatal(err)
	}
	e.tokenEnv = newTokenEnv(t, append([]func(*Deps){func(d *Deps) {
		if _, err := d.Credentials.OpenPush(secret.NewKeyring(k, secret.Key{})); err != nil {
			t.Fatal(err)
		}
		e.svc = push.New(push.Options{
			Store: d.Credentials, Contact: "mailto:ops@example.com", PublicURL: testOrigin,
			Allow:   config.PushAllow{Prefixes: []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}},
			RootCAs: f.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs, Metrics: d.Metrics,
		})
		t.Cleanup(e.svc.Close)
		d.Push = e.svc
	}}, mods...)...)
	return e
}

// subscribe registers d for user and answers the status and the subscription.
func (e *pushEnv) subscribe(t *testing.T, user string, d pushDevice, name string) (int, model.PushSubscriptionInfo) {
	t.Helper()
	w := e.do(as(user, req{method: http.MethodPost, path: "/api/v1/push/subscriptions", body: d.request(name)}))
	var info model.PushSubscriptionInfo
	if w.Code < 300 {
		if err := json.Unmarshal(w.Body.Bytes(), &info); err != nil {
			t.Fatal(err)
		}
	}
	return w.Code, info
}

// code is the verification code the device received last.
func (e *pushEnv) code(t *testing.T, d pushDevice) string {
	t.Helper()
	e.svc.Wait()
	msgs := d.messages(t, e.f)
	if len(msgs) == 0 || msgs[len(msgs)-1].Type != model.PushTypeVerify {
		t.Fatalf("no verify push: %+v", msgs)
	}
	return msgs[len(msgs)-1].Code
}

func (e *pushEnv) verify(user, id, code string) *httptest.ResponseRecorder {
	return e.do(as(user, req{method: http.MethodPost, path: "/api/v1/push/subscriptions/" + id + "/verify", body: model.PushVerifyRequest{Code: code}}))
}

// pat stores a personal access token of user holding scopes (push only reaches a token through
// the dash-android grant; the store takes it as given) and returns the token.
func (e *pushEnv) pat(t *testing.T, user string, scopes []string) string {
	t.Helper()
	token, id, hash, err := credentials.NewPAT()
	if err != nil {
		t.Fatal(err)
	}
	if err := e.store.AddPAT(credentials.PAT{
		ID: id, Name: "app", User: user, Hash: hash, Audience: "api", Scopes: scopes, CreatedAt: fixedNow, ExpiresAt: fixedNow.Add(24 * time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	return token
}

// capabilities.push says whether the caller may register push devices (contract D37, D45): push
// is on, and the caller is a named person or a token with the push scope; never the anonymous
// viewer, and never a token without the scope.
func TestMeCapabilitiesPush(t *testing.T) {
	e := newPushEnv(t)
	withPush := e.pat(t, "bob", []string{"read", "push"})
	readOnly := e.pat(t, "bob", []string{"read"})
	off := newTokenEnv(t)
	for _, tc := range []struct {
		name   string
		s      *Server
		header map[string]string
		want   bool
	}{
		{"a person", e.s, nil, true},
		{"push off", off.s, nil, false},
		{"a token with push", e.s, bearer(withPush), true},
		{"a token without push", e.s, bearer(readOnly), false},
	} {
		if got := meOf(t, req{path: "/api/v1/me", header: tc.header}.do(tc.s.Handler())).Capabilities.Push; got != tc.want {
			t.Errorf("%s: capabilities.push = %v, want %v", tc.name, got, tc.want)
		}
	}
	e.s.d.Auth = auth.Fixed(auth.Principal{Kind: auth.KindAnonymous, User: "anonymous", Role: auth.RoleViewer})
	if meOf(t, req{path: "/api/v1/me"}.do(e.s.Handler())).Capabilities.Push {
		t.Error("the anonymous viewer may register push devices")
	}
	if w := (req{path: "/api/v1/push/subscriptions"}).do(e.s.Handler()); w.Code != http.StatusForbidden {
		t.Errorf("the anonymous viewer lists push devices: %d", w.Code)
	}
}

// With push off every push route answers 409 push_unavailable.
func TestPushRoutesWhenOff(t *testing.T) {
	e := newTokenEnv(t)
	for _, r := range []req{
		{path: "/api/v1/push/vapid"},
		{path: "/api/v1/push/subscriptions"},
		{method: http.MethodPost, path: "/api/v1/push/subscriptions", body: map[string]any{}},
		{method: http.MethodPost, path: "/api/v1/push/subscriptions/sub_x/verify", body: map[string]any{"code": "x"}},
		{method: http.MethodPost, path: "/api/v1/push/subscriptions/sub_x/test"},
		{method: http.MethodDelete, path: "/api/v1/push/subscriptions/sub_x"},
	} {
		wantError(t, e.do(as("alice", r)), http.StatusConflict, model.ErrCodePushUnavailable)
	}
}

// GET /push/vapid gives the applicationServerKey and the URL a native client connects to: the
// direct URL when set, the public URL otherwise.
func TestPushVAPID(t *testing.T) {
	e := newPushEnv(t)
	var v model.PushVAPID
	decodeJSON(t, e.do(as("bob", req{path: "/api/v1/push/vapid"})), &v)
	if k, err := b64url.DecodeString(v.PublicKey); err != nil || len(k) != 65 || k[0] != 4 {
		t.Errorf("publicKey = %q", v.PublicKey)
	}
	if v.ServerURL != testOrigin+"/" {
		t.Errorf("serverUrl = %q, want the public URL", v.ServerURL)
	}
	var again model.PushVAPID
	decodeJSON(t, e.do(as("bob", req{path: "/api/v1/push/vapid"})), &again)
	if again.PublicKey != v.PublicKey {
		t.Error("the key changed between two requests")
	}
	direct := newPushEnv(t, func(d *Deps) { d.Cfg.DirectURL = "https://dash-direct.example.com" })
	decodeJSON(t, direct.do(as("bob", req{path: "/api/v1/push/vapid"})), &v)
	if v.ServerURL != "https://dash-direct.example.com" {
		t.Errorf("serverUrl = %q, want the direct URL", v.ServerURL)
	}
}

// A device registers, receives its code as its only push, verifies with it, gets a test push
// (one a minute) and is removed. Registering the endpoint again replaces the row.
func TestPushSubscriptionLifecycle(t *testing.T) {
	e := newPushEnv(t)
	d := newPushDevice(t, e.f)
	status, sub := e.subscribe(t, "bob", d, "Phone")
	if status != http.StatusCreated || sub.State != model.PushStatePending || sub.EndpointHost != "127.0.0.1" || sub.VerifiedAt != nil {
		t.Fatalf("subscribe = %d %+v", status, sub)
	}
	code := e.code(t, d)
	if len(code) != model.PushCodeLen {
		t.Errorf("code %q", code)
	}
	wantError(t, e.do(as("bob", req{method: http.MethodPost, path: "/api/v1/push/subscriptions/" + sub.ID + "/test"})), http.StatusConflict, model.ErrCodeConflict)
	wantError(t, e.verify("bob", sub.ID, "00000000"), http.StatusUnprocessableEntity, model.ErrCodeInvalidInput)
	w := e.verify("bob", sub.ID, strings.ToLower(code))
	var verified model.PushSubscriptionInfo
	decodeJSON(t, w, &verified)
	if verified.State != model.PushStateVerified || verified.VerifiedAt == nil {
		t.Errorf("verified = %+v", verified)
	}
	wantError(t, e.verify("bob", sub.ID, code), http.StatusConflict, model.ErrCodeConflict)

	// The same endpoint again, renamed: the same row, still verified, and no new code.
	status, again := e.subscribe(t, "bob", d, "Pixel")
	if status != http.StatusOK || again.ID != sub.ID || again.State != model.PushStateVerified || again.DeviceName != "Pixel" {
		t.Errorf("re-registering = %d %+v", status, again)
	}

	if w := e.do(as("bob", req{method: http.MethodPost, path: "/api/v1/push/subscriptions/" + sub.ID + "/test"})); w.Code != http.StatusAccepted {
		t.Fatalf("test push = %d %s", w.Code, w.Body)
	}
	w = e.do(as("bob", req{method: http.MethodPost, path: "/api/v1/push/subscriptions/" + sub.ID + "/test"}))
	wantError(t, w, http.StatusTooManyRequests, model.ErrCodeRateLimited)
	if w.Header().Get("Retry-After") == "" {
		t.Error("no Retry-After")
	}
	e.svc.Wait()
	msgs := d.messages(t, e.f)
	if len(msgs) != 2 || msgs[0].Type != model.PushTypeVerify || msgs[1].Type != model.PushTypeTest {
		t.Errorf("the device received %+v, want its code then the test push", msgs)
	}

	var list model.PushSubscriptionList
	decodeJSON(t, e.do(as("bob", req{path: "/api/v1/push/subscriptions"})), &list)
	if len(list.Subscriptions) != 1 || list.Subscriptions[0].ID != sub.ID || list.Subscriptions[0].Notify.Attention != model.PushAttentionCrit {
		t.Errorf("list = %+v", list)
	}
	if strings.Contains(e.do(as("bob", req{path: "/api/v1/push/subscriptions"})).Body.String(), d.path) {
		t.Error("the list shows the endpoint")
	}
	if w := e.do(as("bob", req{method: http.MethodDelete, path: "/api/v1/push/subscriptions/" + sub.ID})); w.Code != http.StatusNoContent {
		t.Fatalf("delete = %d", w.Code)
	}
	decodeJSON(t, e.do(as("bob", req{path: "/api/v1/push/subscriptions"})), &list)
	if len(list.Subscriptions) != 0 {
		t.Errorf("after delete: %+v", list)
	}
}

// A user sees and changes only their own devices; another user's endpoint is refused; bad
// registrations are field errors; a token needs the push scope and skips CSRF.
func TestPushSubscriptionAccess(t *testing.T) {
	e := newPushEnv(t)
	d := newPushDevice(t, e.f)
	_, sub := e.subscribe(t, "bob", d, "Phone")
	for _, r := range []req{
		{method: http.MethodPost, path: "/api/v1/push/subscriptions/" + sub.ID + "/verify", body: model.PushVerifyRequest{Code: "x"}},
		{method: http.MethodPost, path: "/api/v1/push/subscriptions/" + sub.ID + "/test"},
		{method: http.MethodDelete, path: "/api/v1/push/subscriptions/" + sub.ID},
	} {
		wantError(t, e.do(as("carol", r)), http.StatusNotFound, model.ErrCodeNotFound)
	}
	if status, _ := e.subscribe(t, "carol", d, "Mine"); status != http.StatusConflict {
		t.Errorf("carol takes bob's endpoint: %d", status)
	}
	var list model.PushSubscriptionList
	decodeJSON(t, e.do(as("carol", req{path: "/api/v1/push/subscriptions"})), &list)
	if len(list.Subscriptions) != 0 {
		t.Errorf("carol sees %+v", list)
	}

	for name, mod := range map[string]func(*model.PushSubscriptionRequest){
		"http endpoint": func(r *model.PushSubscriptionRequest) { r.Endpoint = strings.Replace(r.Endpoint, "https:", "http:", 1) },
		"user info": func(r *model.PushSubscriptionRequest) {
			r.Endpoint = strings.Replace(r.Endpoint, "https://", "https://u:p@", 1)
		},
		"bad keys":          func(r *model.PushSubscriptionRequest) { r.Keys.P256dh = "AAAA" },
		"bad kind":          func(r *model.PushSubscriptionRequest) { r.Kind = "fcm" },
		"empty name":        func(r *model.PushSubscriptionRequest) { r.DeviceName = "" },
		"bad threshold":     func(r *model.PushSubscriptionRequest) { r.Notify.Attention = "info" },
		"fragment":          func(r *model.PushSubscriptionRequest) { r.Endpoint += "#x" },
		"endpoint too long": func(r *model.PushSubscriptionRequest) { r.Endpoint += strings.Repeat("a", 2048) },
	} {
		body := newPushDevice(t, e.f).request("x")
		mod(&body)
		w := e.do(as("carol", req{method: http.MethodPost, path: "/api/v1/push/subscriptions", body: body}))
		wantError(t, w, http.StatusBadRequest, model.ErrCodeInvalidInput)
		if strings.Contains(w.Body.String(), "capability-") {
			t.Errorf("%s: the error repeats the endpoint", name)
		}
	}

	// A token: the push scope is required, and its writes need no CSRF header.
	withPush, readOnly := e.pat(t, "dave", []string{"read", "push"}), e.pat(t, "dave", []string{"read"})
	body := newPushDevice(t, e.f).request("Android")
	body.Kind = model.PushKindUnifiedPush
	wantError(t, e.do(req{method: http.MethodPost, path: "/api/v1/push/subscriptions", body: body, raw: true, header: bearer(readOnly)}), http.StatusForbidden, model.ErrCodeInsufficientScope)
	if w := e.do(req{method: http.MethodPost, path: "/api/v1/push/subscriptions", body: body, raw: true, header: bearer(withPush)}); w.Code != http.StatusCreated {
		t.Errorf("a push token registers: %d %s", w.Code, w.Body)
	}
	// A cookie principal's write without the CSRF header is refused.
	if w := e.do(as("erin", req{method: http.MethodPost, path: "/api/v1/push/subscriptions", body: newPushDevice(t, e.f).request("x"), raw: true})); w.Code != http.StatusForbidden {
		t.Errorf("a write without CSRF: %d", w.Code)
	}
}

// A user holds at most ten devices, and registers at most ten a minute (repeats included).
func TestPushSubscriptionLimit(t *testing.T) {
	e := newPushEnv(t)
	for range model.PushSubscriptionsMax {
		if status, _ := e.subscribe(t, "bob", newPushDevice(t, e.f), "d"); status != http.StatusCreated {
			t.Fatalf("subscribe: %d", status)
		}
	}
	w := e.do(as("bob", req{method: http.MethodPost, path: "/api/v1/push/subscriptions", body: newPushDevice(t, e.f).request("d")}))
	wantError(t, w, http.StatusTooManyRequests, model.ErrCodeRateLimited)
	if w.Header().Get("Retry-After") == "" {
		t.Error("no Retry-After")
	}
	if status, _ := e.subscribe(t, "carol", newPushDevice(t, e.f), "d"); status != http.StatusCreated {
		t.Errorf("carol at bob's rate limit: %d", status)
	}
	e.s.now = func() time.Time { return fixedNow.Add(pushRegisterWindow + time.Second) }
	w = e.do(as("bob", req{method: http.MethodPost, path: "/api/v1/push/subscriptions", body: newPushDevice(t, e.f).request("d")}))
	wantError(t, w, http.StatusConflict, model.ErrCodePushLimit)
}

// Registering the same device again counts against the minute's ten as well.
func TestPushReRegistrationsAreLimited(t *testing.T) {
	e := newPushEnv(t)
	d := newPushDevice(t, e.f)
	for i := range pushRegisterLimit {
		if status, _ := e.subscribe(t, "bob", d, "d"); status != http.StatusCreated && status != http.StatusOK {
			t.Fatalf("registration %d: %d", i+1, status)
		}
	}
	wantError(t, e.do(as("bob", req{method: http.MethodPost, path: "/api/v1/push/subscriptions", body: d.request("d")})), http.StatusTooManyRequests, model.ErrCodeRateLimited)
}

// Registering a pending device again pushes the same code again (200), and that code verifies.
func TestPushReRegistrationResendsTheCode(t *testing.T) {
	e := newPushEnv(t)
	d := newPushDevice(t, e.f)
	_, sub := e.subscribe(t, "bob", d, "Phone")
	first := e.code(t, d)
	if status, again := e.subscribe(t, "bob", d, "Phone"); status != http.StatusOK || again.ID != sub.ID {
		t.Fatalf("again: %d %+v", status, again)
	}
	if second := e.code(t, d); second != first || len(d.messages(t, e.f)) != 2 {
		t.Fatalf("second code %q, first %q", second, first)
	}
	if w := e.verify("bob", sub.ID, first); w.Code != http.StatusOK {
		t.Errorf("verify = %d %s", w.Code, w.Body)
	}
}

// An endpoint the sender would never reach is refused at registration with a field error that
// does not repeat it; nothing is stored or pushed.
func TestPushUnreachableEndpointIsRefused(t *testing.T) {
	e := newPushEnv(t)
	for _, endpoint := range []string{"https://10.0.0.1/capability-a", "https://169.254.169.254/capability-b", "https://[fd00::1]:8443/capability-c"} {
		body := newPushDevice(t, e.f).request("Phone")
		body.Endpoint = endpoint
		w := e.do(as("bob", req{method: http.MethodPost, path: "/api/v1/push/subscriptions", body: body}))
		wantError(t, w, http.StatusBadRequest, model.ErrCodeInvalidInput)
		if !strings.Contains(w.Body.String(), "push.allowPrivate") || strings.Contains(w.Body.String(), "capability-") {
			t.Errorf("%s: %s", endpoint, w.Body)
		}
	}
	if n := len(e.store.PushSubscriptions("")); n != 0 {
		t.Errorf("%d subscriptions stored", n)
	}
}

// An administrator lists one user's devices or everyone's and removes another user's; a viewer
// lists only their own (403 for another's) and cannot remove, verify or test another's (404).
// An administrator still cannot verify or test another user's device.
func TestPushAdminSeesAndRemovesDevices(t *testing.T) {
	e := newPushEnv(t)
	_, bobs := e.subscribe(t, "bob", newPushDevice(t, e.f), "Phone")
	_, carols := e.subscribe(t, "carol", newPushDevice(t, e.f), "Laptop")
	list := func(user, query string) []model.PushSubscriptionInfo {
		t.Helper()
		var l model.PushSubscriptionList
		decodeJSON(t, e.do(as(user, req{path: "/api/v1/push/subscriptions" + query})), &l)
		return l.Subscriptions
	}
	if l := list("alice", "?user=*"); len(l) != 2 {
		t.Errorf("admin ?user=* = %+v", l)
	}
	if l := list("alice", "?user=bob"); len(l) != 1 || l[0].ID != bobs.ID || l[0].User != "bob" {
		t.Errorf("admin ?user=bob = %+v", l)
	}
	if l := list("alice", "?user="); len(l) != 0 {
		t.Errorf("admin ?user= (empty) = %+v, want her own (none)", l)
	}
	if l := list("bob", "?user=bob"); len(l) != 1 || l[0].User != "bob" {
		t.Errorf("bob's own = %+v", l)
	}
	wantError(t, e.do(as("carol", req{path: "/api/v1/push/subscriptions?user=bob"})), http.StatusForbidden, model.ErrCodeForbidden)
	wantError(t, e.do(as("carol", req{path: "/api/v1/push/subscriptions?user=*"})), http.StatusForbidden, model.ErrCodeForbidden)
	wantError(t, e.do(as("carol", req{method: http.MethodDelete, path: "/api/v1/push/subscriptions/" + bobs.ID})), http.StatusNotFound, model.ErrCodeNotFound)
	for _, r := range []req{
		{method: http.MethodPost, path: "/api/v1/push/subscriptions/" + bobs.ID + "/verify", body: model.PushVerifyRequest{Code: "x"}},
		{method: http.MethodPost, path: "/api/v1/push/subscriptions/" + bobs.ID + "/test"},
	} {
		wantError(t, e.do(as("alice", r)), http.StatusNotFound, model.ErrCodeNotFound)
	}
	if w := e.do(as("alice", req{method: http.MethodDelete, path: "/api/v1/push/subscriptions/" + bobs.ID})); w.Code != http.StatusNoContent {
		t.Fatalf("admin removes bob's = %d %s", w.Code, w.Body)
	}
	if l := list("alice", "?user=*"); len(l) != 1 || l[0].ID != carols.ID {
		t.Errorf("after the removal = %+v", l)
	}
	removed := 0
	for _, rec := range auditRecords(t, e.auditDir) {
		if rec.Action == auditPushUnsubscribe && rec.Actor.User == "alice" && rec.Target.Name == bobs.ID && rec.Result == "ok" {
			removed++
		}
	}
	if removed != 1 {
		t.Errorf("audit records of alice removing bob's device = %d, want 1", removed)
	}
}

// Every write is audited with the subscription ID and the endpoint's host; the endpoint is in
// no audit record and no log line. An audit log that cannot take the intent stops the write.
func TestPushAuditAndLogsNeverHoldTheEndpoint(t *testing.T) {
	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	e := newPushEnv(t)
	d := newPushDevice(t, e.f)
	_, sub := e.subscribe(t, "bob", d, "Phone")
	code := e.code(t, d)
	e.verify("bob", sub.ID, "WRONGCDE")
	e.verify("bob", sub.ID, code)
	e.do(as("bob", req{method: http.MethodPost, path: "/api/v1/push/subscriptions/" + sub.ID + "/test"}))
	e.svc.Wait()
	e.do(as("bob", req{method: http.MethodDelete, path: "/api/v1/push/subscriptions/" + sub.ID}))

	actions := map[string]int{}
	for _, rec := range auditRecords(t, e.auditDir) {
		if !strings.HasPrefix(rec.Action, "push.") {
			continue
		}
		actions[rec.Action]++
		if rec.Target != (audit.Target{Kind: "pushSubscription", Namespace: "127.0.0.1", Name: sub.ID}) {
			t.Errorf("%s target = %+v", rec.Action, rec.Target)
		}
	}
	for _, a := range []string{auditPushSubscribe, auditPushVerify, auditPushTest, auditPushUnsubscribe} {
		if actions[a] == 0 {
			t.Errorf("no %s record (%v)", a, actions)
		}
	}
	file := readAuditFile(t, e.auditDir)
	if strings.Contains(file, d.path) || strings.Contains(file, "capability-") {
		t.Error("the audit log holds the endpoint")
	}
	if strings.Contains(logs.String(), d.path) || strings.Contains(logs.String(), "capability-") {
		t.Errorf("the log holds the endpoint:\n%s", logs.String())
	}

	// The audit log is gone: nothing is stored, nothing is pushed.
	dir := filepath.Join(t.TempDir(), "audit")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	al, err := audit.Open(dir, audit.Options{MaxSize: 1})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { al.Close() })
	al.Event(audit.Record{Action: "seed"})
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	e.s.d.Audit = al
	other := newPushDevice(t, e.f)
	wantError(t, e.do(as("bob", req{method: http.MethodPost, path: "/api/v1/push/subscriptions", body: other.request("x")})), http.StatusServiceUnavailable, model.ErrCodeAuditUnavailable)
	e.svc.Wait()
	if len(e.store.PushSubscriptions("bob")) != 0 || len(e.f.received(other.path)) != 0 {
		t.Error("a registration went through without its audit record")
	}
}
