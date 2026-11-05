package main

import (
	"bytes"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/TechDufus/dash/internal/credentials"
	"github.com/TechDufus/dash/internal/localauth"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/push"
	"github.com/TechDufus/dash/internal/secret"
)

// The server side of the push acceptance (contract G7-R4): dash in local mode with a data dir,
// push.allowPrivate [127.0.0.1/32] and a fake push service over https whose CA it trusts. A
// device registers as the UnifiedPush connector does, verifies with the code the fake service
// decrypts, receives a test push it decrypts with its own keys, and a 410 deletes a subscription.
// The endpoint is in neither the log nor credentials.yaml.
//
// It runs in a child process: the push client trusts the fake service through SSL_CERT_FILE,
// which Go reads once per process, before any other test has loaded the system roots.
func TestPushEndToEnd(t *testing.T) {
	if os.Getenv("DASH_TEST_PUSH_E2E_CHILD") == "1" {
		pushEndToEnd(t)
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestPushEndToEnd$", "-test.v")
	cmd.Env = append(os.Environ(), "DASH_TEST_PUSH_E2E_CHILD=1")
	out, err := cmd.CombinedOutput()
	if err != nil || !bytes.Contains(out, []byte("--- PASS: TestPushEndToEnd")) {
		t.Fatalf("the end-to-end run failed: %v\n%s", err, out)
	}
}

// e2eService is the fake push service: it records each push and answers status.
type e2eService struct {
	*httptest.Server
	mu     sync.Mutex
	status int
	got    map[string][][]byte
}

func (f *e2eService) bodies(path string) [][]byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.got[path]
}

func (f *e2eService) answer(status int) {
	f.mu.Lock()
	f.status = status
	f.mu.Unlock()
}

func pushEndToEnd(t *testing.T) {
	f := &e2eService{status: http.StatusCreated, got: map[string][][]byte{}}
	f.Server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.got[r.URL.Path] = append(f.got[r.URL.Path], b)
		status := f.status
		f.mu.Unlock()
		w.WriteHeader(status)
	}))
	defer f.Close()
	caFile := filepath.Join(t.TempDir(), "push-ca.pem")
	if err := os.WriteFile(caFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: f.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SSL_CERT_FILE", caFile)

	var logs bytes.Buffer
	var logMu sync.Mutex
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(lockedWriter{&logMu, &logs}, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(prev)

	const password = "correct horse battery staple"
	hash, err := localauth.HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	dataDir := t.TempDir()
	startEnv(t, dataDir,
		"KUBECONFIG=", "KUBERNETES_SERVICE_HOST=", "KUBERNETES_SERVICE_PORT=",
		"DASH_AUTH_MODE=local", "DASH_AUTH_LOCAL_ADMIN_USER=admin", "DASH_ADMIN_PASSWORD_HASH="+hash,
		"DASH_SECRET_KEY="+base64.StdEncoding.EncodeToString(key),
		`DASH_PUSH_ALLOW_PRIVATE=["127.0.0.1/32"]`,
		"DASH_SERVER_LISTEN="+anyPort, "DASH_SERVER_HEALTH_LISTEN="+anyPort)

	type addrs struct{ api, health string }
	bound := make(chan addrs, 1)
	done := make(chan error, 1)
	go func() {
		done <- run(secret.NewRegistry(), func(api, health net.Addr) { bound <- addrs{api.String(), health.String()} })
	}()
	var a addrs
	select {
	case a = <-bound:
	case err := <-done:
		t.Fatalf("run returned before binding: %v", err)
	}
	defer func() {
		_ = syscall.Kill(os.Getpid(), syscall.SIGTERM)
		select {
		case <-done:
		case <-time.After(15 * time.Second):
			t.Error("run did not return after SIGTERM")
		}
	}()
	waitFor(t, func() bool { c, _ := fetch(a.health, "/readyz", false); return c == http.StatusOK })

	client := &http.Client{Timeout: 10 * time.Second}
	var cookies []*http.Cookie
	call := func(method, path string, body any) (int, []byte) {
		t.Helper()
		var rd io.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rd = bytes.NewReader(b)
		}
		req, err := http.NewRequest(method, "http://"+a.api+path, rd)
		if err != nil {
			t.Fatal(err)
		}
		req.Host = "dash.example.com"
		req.Header.Set("Content-Type", "application/json")
		if method != http.MethodGet {
			req.Header.Set("Origin", "https://dash.example.com")
			req.Header.Set("X-Dash-Csrf", "1")
		}
		for _, c := range cookies {
			req.AddCookie(c)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if c := resp.Cookies(); len(c) > 0 {
			cookies = c
		}
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, b
	}
	if code, b := call(http.MethodPost, "/login", map[string]string{"username": "admin", "password": password}); code != http.StatusOK && code != http.StatusNoContent {
		t.Fatalf("login = %d %s", code, b)
	}
	var me model.Me
	if code, b := call(http.MethodGet, "/api/v1/me", nil); code != http.StatusOK || json.Unmarshal(b, &me) != nil || !me.Capabilities.Push {
		t.Fatalf("/me = %d %s, want capabilities.push", code, b)
	}

	// A device, as the UnifiedPush connector registers it.
	newDevice := func() (*ecdh.PrivateKey, []byte, string, model.PushSubscriptionRequest) {
		k, err := ecdh.P256().GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		auth := make([]byte, 16)
		_, _ = rand.Read(auth)
		path := "/upAbCdEf-" + strings.ToLower(rand.Text())
		return k, auth, path, model.PushSubscriptionRequest{
			Endpoint: f.URL + path, Kind: model.PushKindUnifiedPush, DeviceName: "Android",
			Keys:   model.PushKeys{P256dh: base64.RawURLEncoding.EncodeToString(k.PublicKey().Bytes()), Auth: base64.RawURLEncoding.EncodeToString(auth)},
			Notify: model.PushNotify{Approvals: true, Attention: model.PushAttentionCrit},
		}
	}
	// nth is the n-th push the fake service received at path, decrypted with the device's keys.
	nth := func(k *ecdh.PrivateKey, auth []byte, path string, n int) model.PushMessage {
		t.Helper()
		waitFor(t, func() bool { return len(f.bodies(path)) >= n })
		plain, err := push.Decrypt(k, auth, f.bodies(path)[n-1])
		if err != nil {
			t.Fatalf("the device cannot decrypt push %d: %v", n, err)
		}
		var m model.PushMessage
		if err := json.Unmarshal(plain, &m); err != nil {
			t.Fatal(err)
		}
		return m
	}

	k, auth, path, body := newDevice()
	var sub model.PushSubscriptionInfo
	if code, b := call(http.MethodPost, "/api/v1/push/subscriptions", body); code != http.StatusCreated || json.Unmarshal(b, &sub) != nil {
		t.Fatalf("subscribe = %d %s", code, b)
	}
	verify := nth(k, auth, path, 1)
	if verify.Type != model.PushTypeVerify || len(verify.Code) != model.PushCodeLen {
		t.Fatalf("the first push = %+v, want the code", verify)
	}
	if code, b := call(http.MethodPost, "/api/v1/push/subscriptions/"+sub.ID+"/verify", model.PushVerifyRequest{Code: verify.Code}); code != http.StatusOK {
		t.Fatalf("verify = %d %s", code, b)
	}
	if code, b := call(http.MethodPost, "/api/v1/push/subscriptions/"+sub.ID+"/test", nil); code != http.StatusAccepted {
		t.Fatalf("test push = %d %s", code, b)
	}
	if m := nth(k, auth, path, 2); m.Type != model.PushTypeTest || !strings.HasPrefix(m.URL, "https://dash.example.com/") {
		t.Errorf("the test push = %+v", m)
	}

	// The push service answers 410 from now on: the next device's verify push deletes it.
	f.answer(http.StatusGone)
	_, _, gonePath, goneBody := newDevice()
	var gone model.PushSubscriptionInfo
	if code, b := call(http.MethodPost, "/api/v1/push/subscriptions", goneBody); code != http.StatusCreated || json.Unmarshal(b, &gone) != nil {
		t.Fatalf("second subscribe = %d %s", code, b)
	}
	waitFor(t, func() bool { return len(f.bodies(gonePath)) == 1 })
	waitFor(t, func() bool {
		var list model.PushSubscriptionList
		_, b := call(http.MethodGet, "/api/v1/push/subscriptions", nil)
		return json.Unmarshal(b, &list) == nil && len(list.Subscriptions) == 1 && list.Subscriptions[0].ID == sub.ID
	})

	file, err := os.ReadFile(filepath.Join(dataDir, credentials.FileName))
	if err != nil {
		t.Fatal(err)
	}
	logMu.Lock()
	logged := logs.String()
	logMu.Unlock()
	for _, p := range []string{path, gonePath} {
		if bytes.Contains(file, []byte(p)) {
			t.Error("credentials.yaml holds an endpoint in clear")
		}
		if strings.Contains(logged, p) {
			t.Errorf("the log holds an endpoint:\n%s", logged)
		}
	}
	if !strings.Contains(logged, "the push service says the subscription expired") {
		t.Error("the 410 was not logged")
	}
}

type lockedWriter struct {
	mu *sync.Mutex
	w  io.Writer
}

func (l lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}
