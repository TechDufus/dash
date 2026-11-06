package credentials

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/TechDufus/dash/internal/secret"
)

var pushT0 = time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)

func pushKey(t *testing.T) secret.Key {
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

// openPush opens the store of dir and readies it for push with keys.
func openPush(t *testing.T, dir string, keys *secret.Keyring) (*Store, PushOpen) {
	t.Helper()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	res, err := s.OpenPush(keys)
	if err != nil {
		t.Fatal(err)
	}
	return s, res
}

func pushIn(user, endpoint string) PushInput {
	return PushInput{
		User: user, Kind: PushKindWebPush, DeviceName: "Laptop", Approvals: true, Attention: "crit",
		Endpoint: endpoint, EndpointHost: "push.example.net", P256dh: "p256dh-of-" + endpoint, Auth: "auth-of-" + endpoint,
	}
}

func addPush(t *testing.T, s *Store, in PushInput) (PushSubscription, string) {
	t.Helper()
	code, err := NewPushCode()
	if err != nil {
		t.Fatal(err)
	}
	sub, _, err := s.AddPushSubscription(in, code, pushT0)
	if err != nil {
		t.Fatal(err)
	}
	return sub, code
}

func fileOf(t *testing.T, dir string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, FileName))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// The endpoint, the keys and the VAPID private key are sealed in the file; the code is kept as a
// hash. The file stays at schemaVersion 1 until a VAPID key or a subscription is stored, and the
// key survives a restart.
func TestPushDataIsSealed(t *testing.T) {
	dir, keys := t.TempDir(), secret.NewKeyring(pushKey(t), secret.Key{})
	s, _ := openPush(t, dir, keys)
	if f := fileOf(t, dir); !strings.Contains(f, "schemaVersion: 1") || strings.Contains(f, "push:") {
		t.Fatalf("after OpenPush the file is not a plain version 1:\n%s", f)
	}
	priv, pub, err := s.PushVAPIDKey(pushT0)
	if err != nil || len(priv) != 32 || len(pub) != 65 {
		t.Fatalf("PushVAPIDKey = %d, %d, %v", len(priv), len(pub), err)
	}
	if p2, _, _ := s.PushVAPIDKey(pushT0.Add(time.Hour)); !bytes.Equal(p2, priv) {
		t.Error("a second call made another key")
	}
	const endpoint = "https://push.example.net/canary-endpoint-7f3a"
	sub, code := addPush(t, s, pushIn("alice", endpoint))
	f := fileOf(t, dir)
	if !strings.Contains(f, "schemaVersion: 2") {
		t.Errorf("the file with push data is not schemaVersion 2")
	}
	for name, v := range map[string]string{
		"endpoint": endpoint, "p256dh": "p256dh-of-", "auth": "auth-of-", "code": code,
		"private key": base64.RawURLEncoding.EncodeToString(priv), "endpoint path": "canary-endpoint",
	} {
		if strings.Contains(f, v) {
			t.Errorf("the file holds the %s in clear", name)
		}
	}
	if !strings.Contains(f, "push.example.net") {
		t.Error("the file lacks the endpoint host")
	}
	if !validPushID(sub.ID) || sub.State != PushPending || sub.EndpointHost != "push.example.net" {
		t.Errorf("subscription = %+v", sub)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s2, res := openPush(t, dir, keys)
	if res != (PushOpen{}) {
		t.Errorf("reopening with the same key = %+v, want nothing to do", res)
	}
	if p2, pub2, err := s2.PushVAPIDKey(pushT0); err != nil || !bytes.Equal(p2, priv) || !bytes.Equal(pub2, pub) {
		t.Errorf("the VAPID key did not survive a restart: %v", err)
	}
	tgt, err := s2.PushTarget(sub.ID)
	if err != nil || tgt.Endpoint != endpoint || tgt.P256dh != "p256dh-of-"+endpoint || tgt.Auth != "auth-of-"+endpoint || tgt.User != "alice" {
		t.Errorf("PushTarget = %+v, %v", tgt, err)
	}
}

// Without OpenPush (no DASH_SECRET_KEY) nothing is sealed or stored.
func TestPushNeedsTheKeyring(t *testing.T) {
	s, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.OpenPush(nil); !errors.Is(err, ErrPushNoKey) {
		t.Errorf("OpenPush(nil) = %v", err)
	}
	if _, _, err := s.PushVAPIDKey(pushT0); !errors.Is(err, ErrPushNoKey) {
		t.Errorf("PushVAPIDKey = %v", err)
	}
	if _, _, err := s.AddPushSubscription(pushIn("alice", "https://push.example.net/a"), "ABCDEFGH", pushT0); !errors.Is(err, ErrPushNoKey) {
		t.Errorf("AddPushSubscription = %v", err)
	}
}

// With DASH_SECRET_KEY_PREVIOUS, opening re-seals every push value under the new key, so the
// previous key can then be dropped.
func TestPushResealsOnOpen(t *testing.T) {
	dir, a, b := t.TempDir(), pushKey(t), pushKey(t)
	s, _ := openPush(t, dir, secret.NewKeyring(a, secret.Key{}))
	priv, _, err := s.PushVAPIDKey(pushT0)
	if err != nil {
		t.Fatal(err)
	}
	sub, _ := addPush(t, s, pushIn("alice", "https://push.example.net/a"))
	_ = s.Close()

	s, res := openPush(t, dir, secret.NewKeyring(b, a))
	if res != (PushOpen{Resealed: 5}) {
		t.Errorf("rotation = %+v, want the key and four fields (the pending code included) resealed", res)
	}
	_ = s.Close()
	s, res = openPush(t, dir, secret.NewKeyring(b, secret.Key{}))
	if res != (PushOpen{}) {
		t.Errorf("after the rotation = %+v", res)
	}
	if p, _, err := s.PushVAPIDKey(pushT0); err != nil || !bytes.Equal(p, priv) {
		t.Errorf("the VAPID key changed: %v", err)
	}
	if tgt, err := s.PushTarget(sub.ID); err != nil || tgt.Endpoint != "https://push.example.net/a" || tgt.State != PushPending {
		t.Errorf("PushTarget = %+v, %v", tgt, err)
	}
}

// A VAPID key that no longer opens is replaced at first need, and every web-push subscription,
// bound to it, turns stale; a UnifiedPush one keeps working. A key rotated without the previous
// one leaves every subscription stale, and registering the endpoint again replaces the row.
func TestPushLostKeyMarksSubscriptionsStale(t *testing.T) {
	dir, a := t.TempDir(), pushKey(t)
	keys := secret.NewKeyring(a, secret.Key{})
	s, _ := openPush(t, dir, keys)
	_, pub, err := s.PushVAPIDKey(pushT0)
	if err != nil {
		t.Fatal(err)
	}
	web, webCode := addPush(t, s, pushIn("alice", "https://push.example.net/web"))
	up := pushIn("alice", "https://ntfy.example.com/upabc")
	up.Kind = PushKindUnifiedPush
	unified, upCode := addPush(t, s, up)
	for id, code := range map[string]string{web.ID: webCode, unified.ID: upCode} {
		if _, err := s.VerifyPushSubscription("alice", id, code, pushT0); err != nil {
			t.Fatal(err)
		}
	}
	// The VAPID key alone stops opening: sealed under another key.
	foreign, err := secret.NewKeyring(pushKey(t), secret.Key{}).Encrypt(pushVAPIDPath, "x")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.update(false, func(d *document) bool { d.Push.VAPID.PrivateKey = foreign; return true }); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()

	s, res := openPush(t, dir, keys)
	if res != (PushOpen{Lost: true, Stale: 1}) {
		t.Errorf("lost key = %+v, want lost and one stale", res)
	}
	if w, _ := s.PushSubscriptionByID(web.ID); w.State != PushStale {
		t.Errorf("web push state = %s, want stale", w.State)
	}
	if u, _ := s.PushSubscriptionByID(unified.ID); u.State != PushVerified {
		t.Errorf("UnifiedPush state = %s, want verified", u.State)
	}
	if _, pub2, err := s.PushVAPIDKey(pushT0); err != nil || bytes.Equal(pub2, pub) {
		t.Errorf("no new VAPID key: %v", err)
	}
	_ = s.Close()

	// DASH_SECRET_KEY rotated without the previous key: nothing opens any more.
	s, res = openPush(t, dir, secret.NewKeyring(pushKey(t), secret.Key{}))
	if res != (PushOpen{Lost: true, Stale: 1}) {
		t.Errorf("rotation without the previous key = %+v", res)
	}
	if subs := s.PushSubscriptions("alice"); len(subs) != 2 || subs[0].State != PushStale || subs[1].State != PushStale {
		t.Errorf("subscriptions = %+v, want both stale", subs)
	}
	again, sendCode, err := s.AddPushSubscription(pushIn("alice", "https://push.example.net/web"), "ABCDEFGH", pushT0)
	if err != nil || again.ID != web.ID || sendCode != "ABCDEFGH" || again.State != PushPending {
		t.Errorf("re-registering = %+v %q %v, want the same ID pending", again, sendCode, err)
	}
	if tgt, err := s.PushTarget(web.ID); err != nil || tgt.Endpoint != "https://push.example.net/web" {
		t.Errorf("PushTarget after re-registering: %v", err)
	}
}

// The same endpoint for the same user replaces the row (verified while the keys are the same);
// another user's endpoint is refused; a user holds at most ten subscriptions.
func TestPushReplaceAndLimits(t *testing.T) {
	s, _ := openPush(t, t.TempDir(), secret.NewKeyring(pushKey(t), secret.Key{}))
	first, code := addPush(t, s, pushIn("alice", "https://push.example.net/1"))
	if _, err := s.VerifyPushSubscription("alice", first.ID, code, pushT0); err != nil {
		t.Fatal(err)
	}
	renamed := pushIn("alice", "https://push.example.net/1")
	renamed.DeviceName, renamed.Attention, renamed.Approvals = "Phone", "warn", false
	sub, sendCode, err := s.AddPushSubscription(renamed, "IGNORED0", pushT0.Add(time.Minute))
	if err != nil || sendCode != "" || sub.ID != first.ID || sub.State != PushVerified || sub.DeviceName != "Phone" || sub.Attention != "warn" || sub.Approvals {
		t.Errorf("same keys = %+v %q %v, want the same row verified with the new settings", sub, sendCode, err)
	}
	if !sub.CreatedAt.Equal(first.CreatedAt) || sub.VerifiedAt == nil {
		t.Errorf("the row lost its creation or verification time: %+v", sub)
	}
	rekeyed := pushIn("alice", "https://push.example.net/1")
	rekeyed.Auth = "another-auth"
	if sub, sendCode, err := s.AddPushSubscription(rekeyed, "ABCDEFGH", pushT0); err != nil || sendCode != "ABCDEFGH" || sub.ID != first.ID || sub.State != PushPending {
		t.Errorf("new keys = %+v %q %v, want the same row pending", sub, sendCode, err)
	}
	if _, _, err := s.AddPushSubscription(pushIn("bob", "https://push.example.net/1"), "ABCDEFGH", pushT0); !errors.Is(err, ErrPushEndpointTaken) {
		t.Errorf("bob takes alice's endpoint: %v", err)
	}
	for i := 2; i <= MaxPushSubscriptions; i++ {
		addPush(t, s, pushIn("alice", fmt.Sprintf("https://push.example.net/%d", i)))
	}
	if _, _, err := s.AddPushSubscription(pushIn("alice", "https://push.example.net/11"), "ABCDEFGH", pushT0); !errors.Is(err, ErrTooManyPushSubscriptions) {
		t.Errorf("the eleventh: %v", err)
	}
	if _, _, err := s.AddPushSubscription(pushIn("alice", "https://push.example.net/3"), "ABCDEFGH", pushT0); err != nil {
		t.Errorf("replacing at the limit: %v", err)
	}
	if _, _, err := s.AddPushSubscription(pushIn("bob", "https://push.example.net/bob"), "ABCDEFGH", pushT0); err != nil {
		t.Errorf("another user at alice's limit: %v", err)
	}
	if n := len(s.PushSubscriptions("alice")); n != MaxPushSubscriptions {
		t.Errorf("alice holds %d", n)
	}
}

// The code is compared as Crockford base32 reads it; four wrong codes leave the subscription,
// the fifth deletes it. Only the owner verifies, and only a pending subscription.
func TestPushVerification(t *testing.T) {
	s, _ := openPush(t, t.TempDir(), secret.NewKeyring(pushKey(t), secret.Key{}))
	sub, _, err := s.AddPushSubscription(pushIn("alice", "https://push.example.net/a"), "0A1B-2C3D", pushT0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.VerifyPushSubscription("bob", sub.ID, "0A1B2C3D", pushT0); !errors.Is(err, ErrPushNotFound) {
		t.Errorf("bob verifies alice's: %v", err)
	}
	for i := 1; i < PushVerifyAttempts; i++ {
		if _, err := s.VerifyPushSubscription("alice", sub.ID, "ZZZZZZZZ", pushT0); !errors.Is(err, ErrPushWrongCode) {
			t.Fatalf("wrong code %d: %v", i, err)
		}
	}
	got, err := s.VerifyPushSubscription("alice", sub.ID, "oa1b 2c3d", pushT0.Add(time.Minute))
	if err != nil || got.State != PushVerified || got.VerifiedAt == nil || !got.VerifiedAt.Equal(pushT0.Add(time.Minute)) {
		t.Fatalf("the right code after four wrong ones = %+v, %v", got, err)
	}
	if _, err := s.VerifyPushSubscription("alice", sub.ID, "0A1B2C3D", pushT0); !errors.Is(err, ErrPushNotPending) {
		t.Errorf("verifying twice: %v", err)
	}

	doomed, _, err := s.AddPushSubscription(pushIn("alice", "https://push.example.net/b"), "ABCDEFGH", pushT0)
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= PushVerifyAttempts; i++ {
		_, err = s.VerifyPushSubscription("alice", doomed.ID, "ABCDEFGX", pushT0)
	}
	if !errors.Is(err, ErrPushCodeExhausted) {
		t.Errorf("the fifth wrong code: %v", err)
	}
	if _, ok := s.PushSubscriptionByID(doomed.ID); ok {
		t.Error("five wrong codes left the subscription")
	}
	if _, err := s.VerifyPushSubscription("alice", doomed.ID, "ABCDEFGH", pushT0); !errors.Is(err, ErrPushNotFound) {
		t.Errorf("the right code after deletion: %v", err)
	}
}

// NewPushCode is eight Crockford base32 characters.
func TestNewPushCode(t *testing.T) {
	seen := map[string]bool{}
	for range 100 {
		c, err := NewPushCode()
		if err != nil || len(c) != PushCodeLen || strings.Trim(c, crockford) != "" {
			t.Fatalf("code %q, %v", c, err)
		}
		seen[c] = true
	}
	if len(seen) < 99 {
		t.Errorf("%d distinct codes of 100", len(seen))
	}
}

// The sweep deletes a subscription pending for ten minutes and keeps verified ones; it runs on
// its interval.
func TestPushSweep(t *testing.T) {
	s, _ := openPush(t, t.TempDir(), secret.NewKeyring(pushKey(t), secret.Key{}))
	pending, _ := addPush(t, s, pushIn("alice", "https://push.example.net/p"))
	verified, code := addPush(t, s, pushIn("alice", "https://push.example.net/v"))
	if _, err := s.VerifyPushSubscription("alice", verified.ID, code, pushT0); err != nil {
		t.Fatal(err)
	}
	if swept, err := s.SweepPushSubscriptions(pushT0.Add(PushVerifyWindow - time.Second)); err != nil || len(swept) != 0 {
		t.Errorf("before ten minutes: %v %v", swept, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan []PushSubscription, 1)
	go s.RunPushSweep(ctx, time.Millisecond, func() time.Time { return pushT0.Add(PushVerifyWindow) }, func(swept []PushSubscription, err error) {
		if err == nil {
			done <- swept
		}
	})
	select {
	case swept := <-done:
		if len(swept) != 1 || swept[0].ID != pending.ID {
			t.Errorf("swept %+v, want the pending one", swept)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the sweep did not run")
	}
	if _, ok := s.PushSubscriptionByID(pending.ID); ok {
		t.Error("the pending subscription survived the sweep")
	}
	if _, ok := s.PushSubscriptionByID(verified.ID); !ok {
		t.Error("the sweep deleted a verified subscription")
	}
	if PushSweepInterval != time.Minute {
		t.Errorf("PushSweepInterval = %v, want a minute", PushSweepInterval)
	}
}

// A user's epoch bump (a logout) turns that user's subscriptions stale and leaves the others; a
// global bump turns every one stale; registering again on the new epochs makes the row pending,
// and verifying it keeps it verified through the next restart.
func TestPushEpochBumpsMakeSubscriptionsStale(t *testing.T) {
	dir := t.TempDir()
	keys := secret.NewKeyring(pushKey(t), secret.Key{})
	s, _ := openPush(t, dir, keys)
	alice, aliceCode := addPush(t, s, pushIn("alice", "https://push.example.net/a"))
	bob, bobCode := addPush(t, s, pushIn("bob", "https://push.example.net/b"))
	carol, _ := addPush(t, s, pushIn("carol", "https://push.example.net/c")) // pending
	for user, v := range map[string][2]string{"alice": {alice.ID, aliceCode}, "bob": {bob.ID, bobCode}} {
		if _, err := s.VerifyPushSubscription(user, v[0], v[1], pushT0); err != nil {
			t.Fatal(err)
		}
	}
	state := func(id string) string {
		sub, _ := s.PushSubscriptionByID(id)
		return sub.State
	}
	must(t, s.BumpEpoch("alice"))
	if state(alice.ID) != PushStale || state(bob.ID) != PushVerified || state(carol.ID) != PushPending {
		t.Errorf("after alice's bump: %s %s %s, want stale verified pending", state(alice.ID), state(bob.ID), state(carol.ID))
	}
	again, code := addPush(t, s, pushIn("alice", "https://push.example.net/a"))
	if again.ID != alice.ID || again.State != PushPending {
		t.Fatalf("re-registering = %+v, want the same row pending", again)
	}
	// Another user's bump leaves alice's new row alone: it carries her current epoch.
	must(t, s.BumpEpoch("bob"))
	if state(alice.ID) != PushPending || state(bob.ID) != PushStale {
		t.Errorf("after bob's bump: alice %s, bob %s, want pending and stale", state(alice.ID), state(bob.ID))
	}
	if _, err := s.VerifyPushSubscription("alice", alice.ID, code, pushT0); err != nil {
		t.Fatal(err)
	}
	must(t, s.Close())
	s, _ = openPush(t, dir, keys)
	if state(alice.ID) != PushVerified {
		t.Errorf("alice after re-verifying and a restart: %s, want verified", state(alice.ID))
	}
	must(t, s.BumpGlobal())
	for _, id := range []string{alice.ID, bob.ID, carol.ID} {
		if state(id) != PushStale {
			t.Errorf("%s after the global bump: %s, want stale", id, state(id))
		}
	}
}

// A subscription the dash app registered with its OAuth grant follows that grant, not the
// user's sessions: a plain logout (and a global bump alone) keeps it, while revoking the grant,
// "Log out everywhere" (which revokes the user's grants) and the prune of an expired grant each
// turn it stale in the same write.
func TestPushGrantSubscriptionsFollowTheirGrant(t *testing.T) {
	dir := t.TempDir()
	keys := secret.NewKeyring(pushKey(t), secret.Key{})
	s, _ := openPush(t, dir, keys)
	state := func(id string) string {
		sub, _ := s.PushSubscriptionByID(id)
		return sub.State
	}
	device := func(endpoint, grantID string) string {
		t.Helper()
		in := pushIn("alice", endpoint)
		in.GrantID = grantID
		sub, code := addPush(t, s, in)
		if _, err := s.VerifyPushSubscription("alice", sub.ID, code, pushT0); err != nil {
			t.Fatal(err)
		}
		return sub.ID
	}
	grant := func() Grant {
		t.Helper()
		_, g, err := newGrant(t, s, "alice", pushT0)
		must(t, err)
		_, _, err = s.ExchangeGrant(g.ID, pushT0)
		must(t, err)
		return g
	}

	revoked, logout, expired := grant(), grant(), grant()
	browser := device("https://push.example.net/browser", "")
	app := device("https://push.example.net/app", revoked.ID)
	if sub, _ := s.PushSubscriptionByID(app); sub.GrantID != revoked.ID {
		t.Fatalf("the app's row names grant %q, want %q", sub.GrantID, revoked.ID)
	}
	must(t, s.BumpEpoch("alice"))
	must(t, s.BumpGlobal())
	if state(browser) != PushStale || state(app) != PushVerified {
		t.Errorf("after a logout: browser %s, app %s, want stale and verified", state(browser), state(app))
	}
	must(t, s.Close())
	s, _ = openPush(t, dir, keys)
	if state(app) != PushVerified {
		t.Errorf("the app after a restart: %s, want verified", state(app))
	}
	if _, ok, err := s.RevokeGrant(revoked.ID); !ok || err != nil {
		t.Fatalf("RevokeGrant: %v %v", ok, err)
	}
	if state(app) != PushStale {
		t.Errorf("the app after its grant's revocation: %s, want stale", state(app))
	}

	// "Log out everywhere": the user's epoch moves on and every grant of theirs is revoked.
	app = device("https://push.example.net/app2", logout.ID)
	other := device("https://push.example.net/app3", expired.ID)
	must(t, s.BumpEpoch("alice"))
	_, err := s.RevokeGrants("alice")
	must(t, err)
	if state(app) != PushStale || state(other) != PushStale {
		t.Errorf("after Log out everywhere: %s %s, want stale", state(app), state(other))
	}

	// An expired grant is pruned, and its device turns stale in that write; an active one keeps
	// its device.
	_, live, err := newGrant(t, s, "alice", pushT0.Add(GrantIdle))
	must(t, err)
	_, _, err = s.ExchangeGrant(live.ID, pushT0.Add(GrantIdle))
	must(t, err)
	gone := grant()
	keep, lapse := device("https://push.example.net/app4", live.ID), device("https://push.example.net/app5", gone.ID)
	n, err := s.PruneExpired(pushT0.Add(GrantIdle + time.Hour))
	must(t, err)
	if n != 1 || state(keep) != PushVerified || state(lapse) != PushStale {
		t.Errorf("prune deleted %d; live %s, expired %s, want 1, verified and stale", n, state(keep), state(lapse))
	}
	must(t, s.Close())
	s, _ = openPush(t, dir, keys)
	if state(keep) != PushVerified || state(lapse) != PushStale {
		t.Errorf("after a restart: live %s, expired %s, want verified and stale", state(keep), state(lapse))
	}
}

// Deleting the last subscription and holding no key returns the file to schemaVersion 1.
func TestPushDeleteReturnsToVersion1(t *testing.T) {
	dir := t.TempDir()
	s, _ := openPush(t, dir, secret.NewKeyring(pushKey(t), secret.Key{}))
	sub, _ := addPush(t, s, pushIn("alice", "https://push.example.net/a"))
	got, ok, err := s.DeletePushSubscription(sub.ID)
	if err != nil || !ok || got.ID != sub.ID {
		t.Fatalf("DeletePushSubscription = %+v %v %v", got, ok, err)
	}
	if f := fileOf(t, dir); !strings.Contains(f, "schemaVersion: 1") || strings.Contains(f, "push:") {
		t.Errorf("after deleting the only subscription:\n%s", f)
	}
	if _, ok, _ := s.DeletePushSubscription(sub.ID); ok {
		t.Error("deleted twice")
	}
}

// Registering a pending device again pushes its current code once more and keeps its wrong
// attempts and its ten minutes: repeating the registration resets nothing.
func TestPushReRegistrationKeepsThePendingCode(t *testing.T) {
	dir := t.TempDir()
	keys := secret.NewKeyring(pushKey(t), secret.Key{})
	s, _ := openPush(t, dir, keys)
	in := pushIn("alice", "https://push.example.net/a")
	sub, sendCode, err := s.AddPushSubscription(in, "AAAAAAAA", pushT0)
	if err != nil || sendCode != "AAAAAAAA" {
		t.Fatalf("first registration: %q %v", sendCode, err)
	}
	if _, err := s.VerifyPushSubscription("alice", sub.ID, "ZZZZZZZZ", pushT0); !errors.Is(err, ErrPushWrongCode) {
		t.Fatal(err)
	}
	must(t, s.Close())
	s, _ = openPush(t, dir, keys) // the code survives a restart, sealed
	if f := fileOf(t, dir); strings.Contains(f, "AAAAAAAA") {
		t.Error("the code is in the file in clear")
	}
	again, sendCode, err := s.AddPushSubscription(in, "BBBBBBBB", pushT0.Add(9*time.Minute))
	if err != nil || again.ID != sub.ID || sendCode != "AAAAAAAA" {
		t.Fatalf("registering again: %+v %q %v, want the same row and its code", again, sendCode, err)
	}
	if _, err := s.VerifyPushSubscription("alice", sub.ID, "BBBBBBBB", pushT0); !errors.Is(err, ErrPushWrongCode) {
		t.Errorf("the new code verified: %v", err)
	}
	for range PushVerifyAttempts - 3 {
		if _, err := s.VerifyPushSubscription("alice", sub.ID, "ZZZZZZZZ", pushT0); !errors.Is(err, ErrPushWrongCode) {
			t.Fatal(err)
		}
	}
	if _, err := s.VerifyPushSubscription("alice", sub.ID, "ZZZZZZZZ", pushT0); !errors.Is(err, ErrPushCodeExhausted) {
		t.Errorf("the fifth wrong code across both registrations: %v", err)
	}

	// The ten minutes run from the first registration.
	other, _, err := s.AddPushSubscription(pushIn("alice", "https://push.example.net/b"), "CCCCCCCC", pushT0)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.AddPushSubscription(pushIn("alice", "https://push.example.net/b"), "DDDDDDDD", pushT0.Add(9*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if swept, err := s.SweepPushSubscriptions(pushT0.Add(PushVerifyWindow + time.Second)); err != nil || len(swept) != 1 || swept[0].ID != other.ID {
		t.Errorf("sweep = %+v %v, want the re-registered pending row", swept, err)
	}
}

// Two spellings of one endpoint are one endpoint.
func TestPushEndpointIsNormalized(t *testing.T) {
	s, _ := openPush(t, t.TempDir(), secret.NewKeyring(pushKey(t), secret.Key{}))
	first, _ := addPush(t, s, pushIn("alice", "https://push.example.net/a/c"))
	for _, spelling := range []string{
		"HTTPS://Push.Example.NET/a/c",
		"https://push.example.net./a/c",
		"https://push.example.net:443/a/c",
		"https://push.example.net/a/./b/../c",
	} {
		if got, ok := s.PushSubscriptionByEndpoint(spelling); !ok || got.ID != first.ID {
			t.Errorf("%s: %+v %v, want the first row", spelling, got, ok)
		}
		if _, _, err := s.AddPushSubscription(pushIn("bob", spelling), "ABCDEFGH", pushT0); !errors.Is(err, ErrPushEndpointTaken) {
			t.Errorf("bob registers %s: %v, want it taken", spelling, err)
		}
	}
	if _, ok := s.PushSubscriptionByEndpoint("https://push.example.net:8443/a/c"); ok {
		t.Error("another port is the same endpoint")
	}
}

// A password change through DASH_ADMIN_PASSWORD_HASH ends the account's sessions and turns its
// devices stale. A file whose epochs moved on while a row kept older ones (a bump that did not
// reach the rows) is reconciled when push opens.
func TestPushEpochReconciled(t *testing.T) {
	dir := t.TempDir()
	keys := secret.NewKeyring(pushKey(t), secret.Key{})
	s, _ := openPush(t, dir, keys)
	if _, err := s.NoteEnvHash("alice", "$argon2id$first"); err != nil {
		t.Fatal(err)
	}
	sub, _ := addPush(t, s, pushIn("alice", "https://push.example.net/a"))
	other, _ := addPush(t, s, pushIn("bob", "https://push.example.net/b"))
	if changed, err := s.NoteEnvHash("alice", "$argon2id$rotated"); err != nil || !changed {
		t.Fatalf("NoteEnvHash = %v %v", changed, err)
	}
	if got, _ := s.PushSubscriptionByID(sub.ID); got.State != PushStale {
		t.Errorf("alice's device after the rotation: %s, want stale", got.State)
	}
	if got, _ := s.PushSubscriptionByID(other.ID); got.State != PushPending {
		t.Errorf("bob's device: %s, want pending", got.State)
	}

	// bob's epoch moves on without the rows (as an older dash would have written it).
	must(t, s.update(true, func(d *document) bool { d.Epochs["bob"]++; return true }))
	must(t, s.Close())
	s, res := openPush(t, dir, keys)
	if res.SignedOut != 1 {
		t.Errorf("OpenPush = %+v, want one signed-out device", res)
	}
	if got, _ := s.PushSubscriptionByID(other.ID); got.State != PushStale {
		t.Errorf("bob's device after the reconciliation: %s, want stale", got.State)
	}
}
