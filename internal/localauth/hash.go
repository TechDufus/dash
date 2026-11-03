// Package localauth is the local mode's login machinery (contract §4.4, P4.3): argon2id password
// hashes with a cap on concurrent hashing, the login throttle, the one-time setup token and its
// startup line, and the one local administrator account. The HTTP handlers are in
// internal/api/authroutes_local.go and `dash hash-password` in cmd/dash/hashpw.go.
package localauth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"golang.org/x/crypto/argon2"
)

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

// Bounds on the parameters a PHC string may ask Verify for. A hash comes from the operator (the
// environment or credentials.yaml), but a typo must not make one login allocate gigabytes or run
// for minutes: anything outside them is ErrMalformedHash.
const (
	maxArgonTime      = 64
	maxArgonMemoryKiB = 1024 * 1024 // 1 GiB
	minSaltBytes      = 8
	minKeyBytes       = 16
	maxKeyBytes       = 128
)

// phcEncoding is the PHC string format's base64: standard alphabet, no padding.
var phcEncoding = base64.RawStdEncoding

// ValidPassword reports ErrPasswordLength for a password outside the bounds.
func ValidPassword(password string) error {
	if len(password) < MinPasswordBytes || len(password) > MaxPasswordBytes {
		return ErrPasswordLength
	}
	return nil
}

// HashPassword returns the PHC string "$argon2id$v=19$m=65536,t=3,p=4$<salt>$<hash>" (salt and
// hash in unpadded standard base64) of password with a random salt. It takes no slot: `dash
// hash-password` calls it; the server hashes through a Hasher.
func HashPassword(password string) (string, error) {
	if err := ValidPassword(password); err != nil {
		return "", err
	}
	salt := make([]byte, SaltBytes)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(password), salt, ArgonTime, ArgonMemoryKiB, ArgonThreads, KeyBytes)
	return encodePHC(phcParams{time: ArgonTime, memory: ArgonMemoryKiB, threads: ArgonThreads}, salt, key), nil
}

// VerifyPassword reports whether password matches phc, reading the algorithm parameters from
// phc, in constant time. ErrMalformedHash for a phc that is not argon2id.
func VerifyPassword(phc, password string) (bool, error) {
	p, salt, want, err := parsePHC(phc)
	if err != nil {
		return false, err
	}
	got := argon2.IDKey([]byte(password), salt, p.time, p.memory, p.threads, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

type phcParams struct {
	time, memory uint32
	threads      uint8
}

func encodePHC(p phcParams, salt, key []byte) string {
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, p.memory, p.time, p.threads,
		phcEncoding.EncodeToString(salt), phcEncoding.EncodeToString(key))
}

// parsePHC reads "$argon2id$v=19$m=<KiB>,t=<passes>,p=<lanes>$<salt>$<key>". The error never
// quotes the input: it is a password hash.
func parsePHC(phc string) (phcParams, []byte, []byte, error) {
	var p phcParams
	parts := strings.Split(phc, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" || parts[2] != "v="+strconv.Itoa(argon2.Version) {
		return p, nil, nil, ErrMalformedHash
	}
	params := strings.Split(parts[3], ",")
	if len(params) != 3 {
		return p, nil, nil, ErrMalformedHash
	}
	m, okM := phcUint(params[0], "m=", maxArgonMemoryKiB)
	t, okT := phcUint(params[1], "t=", maxArgonTime)
	l, okP := phcUint(params[2], "p=", 255)
	// argon2 needs at least 8 KiB of memory per lane.
	if !okM || !okT || !okP || t < 1 || l < 1 || m < 8*l {
		return p, nil, nil, ErrMalformedHash
	}
	salt, err := phcEncoding.Strict().DecodeString(parts[4])
	if err != nil || len(salt) < minSaltBytes {
		return p, nil, nil, ErrMalformedHash
	}
	key, err := phcEncoding.Strict().DecodeString(parts[5])
	if err != nil || len(key) < minKeyBytes || len(key) > maxKeyBytes {
		return p, nil, nil, ErrMalformedHash
	}
	return phcParams{time: uint32(t), memory: uint32(m), threads: uint8(l)}, salt, key, nil
}

// phcUint parses "<prefix><decimal>" with no sign, no leading zero and a value at most max.
func phcUint(s, prefix string, max uint64) (uint64, bool) {
	digits, ok := strings.CutPrefix(s, prefix)
	if !ok || digits == "" || len(digits) > 10 || (len(digits) > 1 && digits[0] == '0') {
		return 0, false
	}
	n, err := strconv.ParseUint(digits, 10, 64)
	if err != nil || n > max {
		return 0, false
	}
	return n, true
}

// dummyPHC is what VerifyDummy verifies against: the current parameters with a fixed salt and
// key, so an unknown name costs what the account's own hash costs. No password is known to
// match it, and VerifyDummy discards the answer anyway.
var dummyPHC = encodePHC(phcParams{time: ArgonTime, memory: ArgonMemoryKiB, threads: ArgonThreads},
	[]byte("dash-dummy-salt!"), []byte("dash-dummy-key-that-never-match!"))

// HasherOptions replace the hashing functions, for tests (a blocking stub makes the third
// concurrent call deterministic). Nil fields use HashPassword and VerifyPassword.
type HasherOptions struct {
	Hash   func(password string) (string, error)
	Verify func(phc, password string) (bool, error)
}

// Hasher runs hashes and verifies under a semaphore of MaxConcurrentHashes slots, taken without
// waiting: a call that finds no free slot returns ErrBusy at once.
type Hasher struct {
	hash   func(string) (string, error)
	verify func(string, string) (bool, error)
	slots  chan struct{}
}

// NewHasher returns a Hasher with o's functions.
func NewHasher(o HasherOptions) *Hasher {
	h := &Hasher{hash: o.Hash, verify: o.Verify, slots: make(chan struct{}, MaxConcurrentHashes)}
	if h.hash == nil {
		h.hash = HashPassword
	}
	if h.verify == nil {
		h.verify = VerifyPassword
	}
	return h
}

// acquire takes a slot without waiting; the returned func gives it back.
func (h *Hasher) acquire() (func(), bool) {
	select {
	case h.slots <- struct{}{}:
		return func() { <-h.slots }, true
	default:
		return nil, false
	}
}

// Hash is HashPassword in a slot. ErrBusy without a free slot.
func (h *Hasher) Hash(password string) (string, error) {
	release, ok := h.acquire()
	if !ok {
		return "", ErrBusy
	}
	defer release()
	return h.hash(password)
}

// Verify is VerifyPassword in a slot. ErrBusy without a free slot.
func (h *Hasher) Verify(phc, password string) (bool, error) {
	release, ok := h.acquire()
	if !ok {
		return false, ErrBusy
	}
	defer release()
	return h.verify(phc, password)
}

// VerifyDummy verifies password against a fixed hash of the current parameters in a slot and
// reports only ErrBusy: an unknown account name costs the same time and takes the same slot as
// a known one.
func (h *Hasher) VerifyDummy(password string) error {
	release, ok := h.acquire()
	if !ok {
		return ErrBusy
	}
	defer release()
	_, _ = h.verify(dummyPHC, password)
	return nil
}
