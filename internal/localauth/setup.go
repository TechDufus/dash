package localauth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// The setup token (contract §4.4 Setup token, G4-R2-S10, amendment A7).
const (
	// SetupTokenPrefix starts every token; the log scrubber deliberately does not match it, and
	// the logs API redacts it (secret.ScrubLogLine).
	SetupTokenPrefix = "dash_setup_"
	// SetupTokenTTL is how long a token is valid after the start that printed it.
	SetupTokenTTL = time.Hour
	// SetupMessage is the msg of the startup line; the quickstart greps for "setup token".
	SetupMessage = "setup token required: open the url within an hour"
)

// setupTokenBytes is the random part of a token, before base64url.
const setupTokenBytes = 32

// SetupToken is the one-time token that lets the first visitor create the local administrator:
// SetupTokenPrefix + base64url of 32 random bytes, in memory only (a restart prints a new one),
// valid for SetupTokenTTL, consumed by its first successful use. Safe for concurrent use.
type SetupToken struct {
	value   string
	expires time.Time
	now     func() time.Time

	mu   sync.Mutex
	used bool
}

// NewSetupToken creates a token valid until now()+SetupTokenTTL (now nil = time.Now).
func NewSetupToken(now func() time.Time) (*SetupToken, error) {
	if now == nil {
		now = time.Now
	}
	b := make([]byte, setupTokenBytes)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	return &SetupToken{
		value:   SetupTokenPrefix + base64.RawURLEncoding.EncodeToString(b),
		expires: now().Add(SetupTokenTTL),
		now:     now,
	}, nil
}

// Value is the token. Only the startup line and tests read it.
func (t *SetupToken) Value() string { return t.value }

// Expires is when the token stops being accepted.
func (t *SetupToken) Expires() time.Time { return t.expires }

// Usable reports whether the token is neither consumed nor expired.
func (t *SetupToken) Usable() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.usableLocked()
}

func (t *SetupToken) usableLocked() bool { return !t.used && t.now().Before(t.expires) }

// Consume reports whether candidate is the token, compared in constant time, and the token is
// usable; a true answer consumes it, so a second use is refused. A wrong candidate leaves it
// usable.
func (t *SetupToken) Consume(candidate string) bool {
	match := t.equal(candidate)
	t.mu.Lock()
	defer t.mu.Unlock()
	if !match || !t.usableLocked() {
		return false
	}
	t.used = true
	return true
}

func (t *SetupToken) equal(candidate string) bool {
	return subtle.ConstantTimeCompare([]byte(candidate), []byte(t.value)) == 1
}

// SetupURL is publicURL + "/setup#token=" + the token: the token travels in the fragment, which
// never reaches access logs, proxies or Referer.
func SetupURL(publicURL string, t *SetupToken) string {
	return strings.TrimRight(publicURL, "/") + "/setup#token=" + t.value
}

// WriteSetupLine writes the one startup line to w (os.Stderr) through a plain slog JSON handler,
// not the scrubbing one: {"msg": SetupMessage, "url": SetupURL(...), "expires": RFC 3339}.
func WriteSetupLine(w io.Writer, publicURL string, t *SetupToken) error {
	if t == nil {
		return errors.New("localauth: no setup token")
	}
	ew := &errWriter{w: w}
	slog.New(slog.NewJSONHandler(ew, nil)).Info(SetupMessage,
		"url", SetupURL(publicURL, t), "expires", t.expires.UTC().Format(time.RFC3339))
	return ew.err
}

// errWriter keeps the first write error, which a slog handler does not report.
type errWriter struct {
	w   io.Writer
	err error
}

func (e *errWriter) Write(p []byte) (int, error) {
	n, err := e.w.Write(p)
	if err != nil && e.err == nil {
		e.err = err
	}
	return n, err
}
