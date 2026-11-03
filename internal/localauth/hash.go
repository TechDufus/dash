// Package localauth is the local mode's login machinery (contract §4.4, P4.3): argon2id password
// hashes with a cap on concurrent hashing, the login throttle, the one-time setup token and its
// startup line, and the one local administrator account. The HTTP handlers are in
// internal/api/authroutes_local.go and `dash hash-password` in cmd/dash/hashpw.go.
//
// Scaffold (G4-R2-S13): the signatures are final; the bodies are the local-auth slice's (S11).
package localauth

import "errors"

// The argon2id parameters of new hashes (RFC 9106, the second recommended option). Verify reads
// the parameters back from the stored PHC string, so changing these never locks anyone out.
const (
	ArgonTime      = 3
	ArgonMemoryKiB = 64 * 1024
	ArgonThreads   = 4
	SaltBytes      = 16
	KeyBytes       = 32

	// MinPasswordBytes and MaxPasswordBytes bound a new password (setup); a login with a
	// password outside them is refused like a wrong one.
	MinPasswordBytes = 12
	MaxPasswordBytes = 256

	// MaxConcurrentHashes is how many hashes (or verifies) run at once: 64 MiB each.
	MaxConcurrentHashes = 2
)

// Errors of the hashing functions.
var (
	// ErrBusy means MaxConcurrentHashes are running: the request is answered 429 rate_limited at
	// once, without queueing.
	ErrBusy = errors.New("localauth: too many password checks in progress")
	// ErrMalformedHash means a stored or supplied hash that is not an argon2id PHC string.
	ErrMalformedHash = errors.New("localauth: not an argon2id hash")
	// ErrPasswordLength means a password outside MinPasswordBytes..MaxPasswordBytes.
	ErrPasswordLength = errors.New("localauth: a password is 12 to 256 bytes")
)

// errSkeleton is what the scaffold's bodies return until the slice implements them.
var errSkeleton = errors.New("localauth: not implemented in the scaffold")

// ValidPassword reports ErrPasswordLength for a password outside the bounds.
func ValidPassword(password string) error { return errSkeleton }

// HashPassword returns the PHC string "$argon2id$v=19$m=65536,t=3,p=4$<salt>$<hash>" (salt and
// hash in unpadded standard base64) of password with a random salt. It takes no slot: `dash
// hash-password` calls it; the server hashes through a Hasher.
func HashPassword(password string) (string, error) { return "", errSkeleton }

// VerifyPassword reports whether password matches phc, reading the algorithm parameters from
// phc, in constant time. ErrMalformedHash for a phc that is not argon2id.
func VerifyPassword(phc, password string) (bool, error) { return false, errSkeleton }

// HasherOptions replace the hashing functions, for tests (a blocking stub makes the third
// concurrent call deterministic). Nil fields use HashPassword and VerifyPassword.
type HasherOptions struct {
	Hash   func(password string) (string, error)
	Verify func(phc, password string) (bool, error)
}

// Hasher runs hashes and verifies under a semaphore of MaxConcurrentHashes slots, taken without
// waiting: a call that finds no free slot returns ErrBusy at once.
type Hasher struct{}

// NewHasher returns a Hasher with o's functions.
func NewHasher(o HasherOptions) *Hasher { return &Hasher{} }

// Hash is HashPassword in a slot. ErrBusy without a free slot.
func (h *Hasher) Hash(password string) (string, error) { return "", ErrBusy }

// Verify is VerifyPassword in a slot. ErrBusy without a free slot.
func (h *Hasher) Verify(phc, password string) (bool, error) { return false, ErrBusy }

// VerifyDummy verifies password against a fixed hash of the current parameters in a slot and
// reports only ErrBusy: an unknown account name costs the same time and takes the same slot as
// a known one.
func (h *Hasher) VerifyDummy(password string) error { return ErrBusy }
