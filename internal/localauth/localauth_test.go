package localauth

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/argon2"
)

// clock is a settable clock for the throttle and the setup token.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock() *clock { return &clock{t: time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)} }

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

const goodPassword = "correct horse battery"

func TestHashVerifyReadsParamsFromThePHCString(t *testing.T) {
	// A hash made with other parameters than today's constants: Verify must read them back.
	salt := []byte("0123456789abcdef")
	key := argon2.IDKey([]byte(goodPassword), salt, 1, 8*1024, 1, 24)
	phc := encodePHC(phcParams{time: 1, memory: 8 * 1024, threads: 1}, salt, key)
	if !strings.HasPrefix(phc, "$argon2id$v=19$m=8192,t=1,p=1$") {
		t.Fatalf("phc = %q", phc)
	}
	if ok, err := VerifyPassword(phc, goodPassword); err != nil || !ok {
		t.Fatalf("VerifyPassword(own params) = %v, %v; want true", ok, err)
	}
	if ok, err := VerifyPassword(phc, goodPassword+"x"); err != nil || ok {
		t.Errorf("VerifyPassword(wrong password) = %v, %v; want false", ok, err)
	}
	// Each parameter of the string is used: changing one changes the derived key.
	for _, params := range []string{"m=16384,t=1,p=1", "m=8192,t=2,p=1", "m=8192,t=1,p=2"} {
		other := strings.Replace(phc, "m=8192,t=1,p=1", params, 1)
		if ok, err := VerifyPassword(other, goodPassword); err != nil || ok {
			t.Errorf("VerifyPassword with %s = %v, %v; want false", params, ok, err)
		}
	}

	// A new hash carries today's parameters, a 16-byte salt and a 32-byte key.
	got, err := HashPassword(goodPassword)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got, "$argon2id$v=19$m=65536,t=3,p=4$") {
		t.Fatalf("HashPassword = %q, want the argon2id t=3 m=64MiB p=4 PHC prefix", got)
	}
	p, s, k, err := parsePHC(got)
	if err != nil || p != (phcParams{time: ArgonTime, memory: ArgonMemoryKiB, threads: ArgonThreads}) || len(s) != SaltBytes || len(k) != KeyBytes {
		t.Fatalf("parsePHC(new hash) = %+v, salt %d, key %d, %v", p, len(s), len(k), err)
	}
	if strings.Contains(got, goodPassword) {
		t.Fatal("the hash contains the password")
	}
	if ok, err := VerifyPassword(got, goodPassword); err != nil || !ok {
		t.Errorf("VerifyPassword(new hash) = %v, %v; want true", ok, err)
	}
}

func TestMalformedHashesAreRefusedWithoutQuotingThem(t *testing.T) {
	salt := phcEncoding.EncodeToString([]byte("0123456789abcdef"))
	key := phcEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32))
	good := "$argon2id$v=19$m=65536,t=3,p=4$" + salt + "$" + key
	if _, _, _, err := parsePHC(good); err != nil {
		t.Fatalf("the well-formed base case is refused: %v", err)
	}
	for name, phc := range map[string]string{
		"empty":             "",
		"argon2i":           strings.Replace(good, "argon2id", "argon2i", 1),
		"bcrypt":            "$2a$10$abcdefghijklmnopqrstuuSUPERSECRETabcdefghijklmnopqrstu",
		"version 16":        strings.Replace(good, "v=19", "v=16", 1),
		"no params":         "$argon2id$v=19$" + salt + "$" + key,
		"memory too large":  strings.Replace(good, "m=65536", "m=4194304", 1),
		"memory below 8p":   strings.Replace(good, "m=65536", "m=31", 1),
		"zero time":         strings.Replace(good, "t=3", "t=0", 1),
		"time too large":    strings.Replace(good, "t=3", "t=65", 1),
		"zero lanes":        strings.Replace(good, "p=4", "p=0", 1),
		"lanes over 255":    strings.Replace(good, "p=4", "p=256", 1),
		"leading zero":      strings.Replace(good, "t=3", "t=03", 1),
		"signed":            strings.Replace(good, "t=3", "t=+3", 1),
		"params reordered":  strings.Replace(good, "m=65536,t=3", "t=3,m=65536", 1),
		"padded salt":       strings.Replace(good, salt, salt+"==", 1),
		"short salt":        strings.Replace(good, salt, phcEncoding.EncodeToString([]byte("1234567")), 1),
		"short key":         strings.Replace(good, key, phcEncoding.EncodeToString(make([]byte, 15)), 1),
		"bad base64":        strings.Replace(good, key, "!!!!", 1),
		"trailing field":    good + "$x",
		"missing leading $": strings.TrimPrefix(good, "$"),
	} {
		ok, err := VerifyPassword(phc, goodPassword)
		if ok || !errors.Is(err, ErrMalformedHash) {
			t.Errorf("%s: VerifyPassword = %v, %v; want ErrMalformedHash", name, ok, err)
		}
		if phc != "" && err != nil && strings.Contains(err.Error(), phc) {
			t.Errorf("%s: the error quotes the hash", name)
		}
	}
}

// The dummy verify of an unknown name must cost what the account's own verify costs: the same
// parameters and key length as a new hash, and a string the real verify accepts.
func TestDummyHashHasTheCurrentParameters(t *testing.T) {
	p, salt, key, err := parsePHC(dummyPHC)
	if err != nil {
		t.Fatalf("dummyPHC does not parse: %v", err)
	}
	if p != (phcParams{time: ArgonTime, memory: ArgonMemoryKiB, threads: ArgonThreads}) || len(salt) != SaltBytes || len(key) != KeyBytes {
		t.Errorf("dummyPHC = %+v, salt %d, key %d; want the parameters of a new hash", p, len(salt), len(key))
	}
}

func TestPasswordLengthBounds(t *testing.T) {
	for _, tc := range []struct {
		name string
		pw   string
		ok   bool
	}{
		{"empty", "", false},
		{"11 bytes", strings.Repeat("a", 11), false},
		{"12 bytes", strings.Repeat("a", 12), true},
		{"256 bytes", strings.Repeat("a", 256), true},
		{"257 bytes", strings.Repeat("a", 257), false},
		// Bytes, not characters: six two-byte runes are 12 bytes.
		{"6 runes of 2 bytes", strings.Repeat("é", 6), true},
		{"11 runes of 2 bytes is 22 bytes", strings.Repeat("é", 11), true},
		{"129 runes of 2 bytes is 258 bytes", strings.Repeat("é", 129), false},
	} {
		err := ValidPassword(tc.pw)
		if (err == nil) != tc.ok || (err != nil && !errors.Is(err, ErrPasswordLength)) {
			t.Errorf("%s: ValidPassword = %v, want ok=%v", tc.name, err, tc.ok)
		}
	}
	for _, pw := range []string{strings.Repeat("a", 11), strings.Repeat("a", 257)} {
		if phc, err := HashPassword(pw); !errors.Is(err, ErrPasswordLength) || phc != "" {
			t.Errorf("HashPassword(%d bytes) = %q, %v; want ErrPasswordLength", len(pw), phc, err)
		}
	}
}

// blockingHasher is a Hasher whose functions park until release is closed; entered receives
// one value per call that got a slot. A call that runs while MaxConcurrentHashes others are
// parked sets overflow instead of parking, so a broken semaphore fails the test, not hangs it.
type blockingHasher struct {
	*Hasher
	entered  chan string
	release  chan struct{}
	inflight atomic.Int32
	overflow atomic.Bool
}

func newBlockingHasher() *blockingHasher {
	b := &blockingHasher{entered: make(chan string, 8), release: make(chan struct{})}
	park := func(phc string) {
		if b.inflight.Add(1) > MaxConcurrentHashes {
			b.overflow.Store(true)
		} else {
			b.entered <- phc
			<-b.release
		}
		b.inflight.Add(-1)
	}
	b.Hasher = NewHasher(HasherOptions{
		Hash:   func(string) (string, error) { park(""); return "stub-hash", nil },
		Verify: func(phc, _ string) (bool, error) { park(phc); return false, nil },
	})
	return b
}

// within runs f and returns its result, failing the test (instead of hanging it until -timeout)
// when f has not returned after a few seconds: a broken semaphore that parks a call it should
// refuse, or refuses one it should park, then shows as a failure naming the step.
func within[T any](t *testing.T, what string, f func() T) T {
	t.Helper()
	done := make(chan T, 1)
	go func() { done <- f() }()
	select {
	case v := <-done:
		return v
	case <-time.After(5 * time.Second):
		t.Fatalf("%s did not return within 5 s", what)
		var zero T
		return zero
	}
}

func TestHasherRunsTwoAndRefusesTheThirdAtOnce(t *testing.T) {
	b := newBlockingHasher()
	var wg sync.WaitGroup
	wg.Go(func() { _, _ = b.Verify("stored", goodPassword) })
	wg.Go(func() { _ = b.VerifyDummy(goodPassword) })
	entered := func() string { return <-b.entered }
	got := map[string]bool{within(t, "the first parked call", entered): true, within(t, "the second parked call", entered): true}
	if !got["stored"] || !got[dummyPHC] {
		t.Errorf("the parked calls saw %v; want the stored hash and dummyPHC", got)
	}

	if err := within(t, "a third Hash", func() error { _, err := b.Hash(goodPassword); return err }); !errors.Is(err, ErrBusy) {
		t.Errorf("third Hash: %v, want ErrBusy", err)
	}
	if err := within(t, "a third Verify", func() error { _, err := b.Verify("stored", goodPassword); return err }); !errors.Is(err, ErrBusy) {
		t.Errorf("third Verify: %v, want ErrBusy", err)
	}
	if err := within(t, "a third VerifyDummy", func() error { return b.VerifyDummy(goodPassword) }); !errors.Is(err, ErrBusy) {
		t.Errorf("third VerifyDummy: %v, want ErrBusy", err)
	}
	if b.overflow.Load() {
		t.Error("a third hash ran while two were in progress")
	}

	close(b.release)
	within(t, "the parked calls after the release", func() bool { wg.Wait(); return true })
	// The slots came back.
	var h string
	if err := within(t, "Hash after the release", func() (err error) { h, err = b.Hash(goodPassword); return err }); err != nil || h != "stub-hash" {
		t.Errorf("Hash after release = %q, %v", h, err)
	}
	within(t, "the call after the release to be seen", entered)
}

func TestBackoffDoublesAfterFiveFailures(t *testing.T) {
	c := newClock()
	th := NewThrottle("admin", c.Now)
	for i := range MaxFailures - 1 {
		th.Fail("192.0.2.7", "admin")
		if _, ok := th.Allow("192.0.2.7", "admin"); !ok {
			t.Fatalf("refused after %d failures", i+1)
		}
	}
	want := FirstBackoff
	for step := range 14 {
		th.Fail("192.0.2.7", "admin")
		wait, ok := th.Allow("192.0.2.7", "admin")
		if ok || wait != want {
			t.Fatalf("step %d: Allow = %v, %v; want refused for %v", step, wait, ok, want)
		}
		c.Advance(want - time.Millisecond)
		if wait, ok := th.Allow("192.0.2.7", "admin"); ok || wait != time.Millisecond {
			t.Fatalf("step %d: 1ms before the end: Allow = %v, %v", step, wait, ok)
		}
		c.Advance(time.Millisecond)
		if _, ok := th.Allow("192.0.2.7", "admin"); !ok {
			t.Fatalf("step %d: still refused after the %v window", step, want)
		}
		want = min(2*want, MaxBackoff)
	}
	if want != MaxBackoff {
		t.Fatalf("the window never reached %v", MaxBackoff)
	}

	// Each key backs off on its own: the account's key from rotating peers, the peer's key for
	// a setup attempt that names no user.
	c2 := newClock()
	th = NewThrottle("admin", c2.Now)
	for i := range MaxFailures {
		th.Fail("198.51.100."+string(rune('1'+i)), "admin")
	}
	if wait, ok := th.Allow("203.0.113.9", "admin"); ok || wait != FirstBackoff {
		t.Errorf("the account from a fresh peer: Allow = %v, %v; want refused for 1s", wait, ok)
	}
	if _, ok := th.Allow("203.0.113.9", "someone"); !ok {
		t.Error("another name from a fresh peer is refused by the account's key")
	}
	for range MaxFailures {
		th.Fail("203.0.113.10", "")
	}
	if wait, ok := th.Allow("203.0.113.10", ""); ok || wait != FirstBackoff {
		t.Errorf("the peer alone: Allow = %v, %v; want refused for 1s", wait, ok)
	}
}

func TestUnknownNamesShareOneBucket(t *testing.T) {
	c := newClock()
	th := NewThrottle("admin", c.Now)
	for i := range MaxFailures {
		name := "guess-" + string(rune('a'+i))
		th.Fail("192.0.2."+string(rune('1'+i)), name)
	}
	if _, ok := th.Allow("203.0.113.1", "never-tried"); ok {
		t.Error("five failures under five names did not close the shared bucket")
	}
	if _, ok := th.Allow("203.0.113.1", "admin"); !ok {
		t.Error("the configured account shares the bucket of unknown names")
	}
}

func TestSuccessClearsBothKeys(t *testing.T) {
	c := newClock()
	th := NewThrottle("admin", c.Now)
	for range MaxFailures {
		th.Fail("192.0.2.7", "admin")
	}
	if _, ok := th.Allow("192.0.2.7", "admin"); ok {
		t.Fatal("not refused after five failures")
	}
	th.Succeed("192.0.2.7", "admin")
	if _, ok := th.Allow("192.0.2.7", "admin"); !ok {
		t.Fatal("still refused after a success")
	}
	// The counters restarted at zero: four more failures are allowed.
	for range MaxFailures - 1 {
		th.Fail("192.0.2.7", "admin")
	}
	if _, ok := th.Allow("192.0.2.7", "admin"); !ok {
		t.Error("the failures before the success still count")
	}
}

func TestPeerTableIsCappedOldestEvicted(t *testing.T) {
	c := newClock()
	th := NewThrottle("admin", c.Now)
	for range MaxFailures {
		th.Fail("first", "")
	}
	peer := func(i int) string { return fmt.Sprintf("10.0.%d.%d", i/256, i%256) }
	for i := range MaxPeers - 1 {
		th.Fail(peer(i), "")
	}
	if _, ok := th.Allow("first", ""); ok {
		t.Fatal("evicted before the table was full")
	}
	th.Fail(peer(MaxPeers-1), "")
	if n := len(th.peers); n != MaxPeers {
		t.Errorf("%d peer keys, want the cap %d", n, MaxPeers)
	}
	if _, ok := th.Allow("first", ""); !ok {
		t.Error("the oldest peer was not evicted by the 4097th")
	}
}

func TestSetupTokenIsSingleUseAndExpires(t *testing.T) {
	c := newClock()
	tok, err := NewSetupToken(c.Now)
	if err != nil {
		t.Fatal(err)
	}
	raw, ok := strings.CutPrefix(tok.Value(), SetupTokenPrefix)
	if b, err := base64.RawURLEncoding.DecodeString(raw); !ok || err != nil || len(b) != 32 {
		t.Fatalf("token %q is not dash_setup_ + base64url(32 bytes)", tok.Value())
	}
	if !tok.Expires().Equal(c.Now().Add(time.Hour)) {
		t.Errorf("expires %v, want an hour from now", tok.Expires())
	}
	if other, _ := NewSetupToken(c.Now); other.Value() == tok.Value() {
		t.Error("two tokens are equal")
	}
	if tok.Consume("") || tok.Consume(tok.Value()+"x") || tok.Consume(tok.Value()[:len(tok.Value())-1]) {
		t.Fatal("a wrong token was accepted")
	}
	if !tok.Usable() {
		t.Fatal("a wrong candidate made the token unusable")
	}
	if !tok.Consume(tok.Value()) {
		t.Fatal("the token was refused")
	}
	if tok.Consume(tok.Value()) || tok.Usable() {
		t.Error("the token is accepted twice")
	}

	late, _ := NewSetupToken(c.Now)
	c.Advance(time.Hour - time.Second)
	if !late.Usable() {
		t.Error("expired before the hour")
	}
	c.Advance(2 * time.Second)
	if late.Usable() || late.Consume(late.Value()) {
		t.Error("accepted at 1h+1s")
	}
}

func TestSetupLineCarriesTheTokenInTheFragment(t *testing.T) {
	c := newClock()
	tok, _ := NewSetupToken(c.Now)
	var buf bytes.Buffer
	if err := WriteSetupLine(&buf, "https://dash.example.com/", tok); err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(buf.String(), "\n"); n != 1 {
		t.Fatalf("%d lines, want 1: %q", n, buf.String())
	}
	var line struct{ Msg, URL, Expires string }
	if err := json.Unmarshal(buf.Bytes(), &line); err != nil {
		t.Fatal(err)
	}
	if line.Msg != SetupMessage || !strings.Contains(line.Msg, "setup token") {
		t.Errorf("msg = %q", line.Msg)
	}
	if want := "https://dash.example.com/setup#token=" + tok.Value(); line.URL != want {
		t.Errorf("url = %q, want %q", line.URL, want)
	}
	if strings.Contains(line.URL, "?") {
		t.Error("the token is in the query")
	}
	if exp, err := time.Parse(time.RFC3339, line.Expires); err != nil || !exp.Equal(tok.Expires()) {
		t.Errorf("expires = %q (%v), want %v", line.Expires, err, tok.Expires())
	}
	if err := WriteSetupLine(failWriter{}, "https://dash.example.com", tok); err == nil {
		t.Error("a failed write is not reported")
	}
}

type failWriter struct{}

func (failWriter) Write([]byte) (int, error) { return 0, errors.New("disk full") }

func TestEnvHashMakesTheAccountEnvManaged(t *testing.T) {
	c := newClock()
	env := Account{User: "admin", EnvHash: "$argon2id$from-env"}
	if !env.EnvManaged() || !env.Exists() {
		t.Error("an env hash does not make the account env-managed and existing")
	}
	if h, ok := env.Hash(); !ok || h != env.EnvHash {
		t.Errorf("Hash = %q, %v; want the env hash", h, ok)
	}
	tok, _ := NewSetupToken(c.Now)
	if (&Local{Account: env, Setup: tok}).SetupNeeded() {
		t.Error("setup is needed for an env-managed account")
	}

	none := Account{User: "admin"}
	if none.EnvManaged() || none.Exists() {
		t.Error("an account without a hash exists")
	}
	if _, ok := none.Hash(); ok {
		t.Error("an account without a hash has one")
	}
	// Setup stays needed until an account exists, whatever the token's state: expired, used or
	// absent, the UI keeps pointing to setup (a restart prints a new token).
	waiting := &Local{Account: none, Setup: tok}
	if !waiting.SetupNeeded() {
		t.Error("setup is not needed before the account exists")
	}
	c.Advance(SetupTokenTTL + time.Second)
	if tok.Usable() || !waiting.SetupNeeded() {
		t.Error("an expired token ended the need for setup")
	}
	if !(&Local{Account: none}).SetupNeeded() {
		t.Error("setup is not needed without a token and without an account")
	}
	if (*Local)(nil).SetupNeeded() {
		t.Error("a nil Local (another mode) needs setup")
	}
}
