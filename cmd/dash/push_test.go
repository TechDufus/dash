package main

import (
	"crypto/rand"
	"encoding/base64"
	"testing"
	"time"

	"github.com/TechDufus/dash/internal/credentials"
	"github.com/TechDufus/dash/internal/secret"
)

// randomKey is a fresh DASH_SECRET_KEY.
func randomKey(t *testing.T) secret.Key {
	t.Helper()
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		t.Fatal(err)
	}
	k, err := secret.ParseKey(base64.StdEncoding.EncodeToString(raw))
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// At start, push data sealed with DASH_SECRET_KEY_PREVIOUS is re-sealed (an INFO), and a key
// rotated without the previous one is a WARN naming how many devices must subscribe again
// (contract D39); push stays on either way.
func TestPushKeysAtStart(t *testing.T) {
	dir, a, b := t.TempDir(), randomKey(t), randomKey(t)
	start := func(keys *secret.Keyring) (processAuth, *logCapture) {
		t.Helper()
		res := localResolved(dir, keys, nil)
		res.Config.PushContact = "mailto:ops@example.com"
		logs := &logCapture{}
		pa, _, err := startLocal(t, res, hashOf(t), logs)
		if err != nil {
			t.Fatal(err)
		}
		if !pa.push {
			t.Fatalf("push is off:\n%s", &logs.buf)
		}
		return pa, logs
	}
	pa, _ := start(secret.NewKeyring(a, secret.Key{}))
	if _, _, err := pa.store.PushVAPIDKey(time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, _, err := pa.store.AddPushSubscription(credentials.PushInput{
		User: "admin", Kind: credentials.PushKindWebPush, DeviceName: "Laptop", Attention: "crit",
		Endpoint: "https://push.example.net/a", EndpointHost: "push.example.net", P256dh: "k", Auth: "a",
	}, "ABCDEFGH", time.Now()); err != nil {
		t.Fatal(err)
	}
	pa.close()

	pa, logs := start(secret.NewKeyring(b, a))
	if got := logs.with(t, "INFO", "push: re-sealed with the current DASH_SECRET_KEY"); len(got) != 1 || got[0]["values"] != float64(5) { // the VAPID key, the endpoint, its keys and the pending code
		t.Errorf("re-seal INFO = %v\n%s", got, &logs.buf)
	}
	pa.close()

	pa, logs = start(secret.NewKeyring(randomKey(t), secret.Key{}))
	warn := logs.with(t, "WARN", "")
	if len(warn) != 1 || warn[0]["vapidKeyLost"] != true || warn[0]["staleDevices"] != float64(1) {
		t.Errorf("a key rotated without the previous one: WARN = %v\n%s", warn, &logs.buf)
	}
	pa.close()
}
