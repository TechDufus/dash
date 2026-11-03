package localauth

import (
	"io"
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

// SetupToken is the one-time token that lets the first visitor create the local administrator:
// SetupTokenPrefix + base64url of 32 random bytes, in memory only (a restart prints a new one),
// valid for SetupTokenTTL, consumed by its first successful use. Safe for concurrent use.
type SetupToken struct{}

// NewSetupToken creates a token valid until now()+SetupTokenTTL (now nil = time.Now).
func NewSetupToken(now func() time.Time) (*SetupToken, error) { return nil, errSkeleton }

// Value is the token. Only the startup line and tests read it.
func (t *SetupToken) Value() string { return "" }

// Expires is when the token stops being accepted.
func (t *SetupToken) Expires() time.Time { return time.Time{} }

// Usable reports whether the token is neither consumed nor expired.
func (t *SetupToken) Usable() bool { return false }

// Consume reports whether candidate is the token, compared in constant time, and the token is
// usable; a true answer consumes it, so a second use is refused. A wrong candidate leaves it
// usable.
func (t *SetupToken) Consume(candidate string) bool { return false }

// SetupURL is publicURL + "/setup#token=" + the token: the token travels in the fragment, which
// never reaches access logs, proxies or Referer.
func SetupURL(publicURL string, t *SetupToken) string { return "" }

// WriteSetupLine writes the one startup line to w (os.Stderr) through a plain slog JSON handler,
// not the scrubbing one: {"msg": SetupMessage, "url": SetupURL(...), "expires": RFC 3339}.
func WriteSetupLine(w io.Writer, publicURL string, t *SetupToken) error { return errSkeleton }
