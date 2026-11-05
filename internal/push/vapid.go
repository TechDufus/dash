package push

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/asn1"
	"encoding/json"
	"errors"
	"math/big"
	"net/url"
	"sync"
	"time"
)

// VAPIDLifetime is how long a VAPID JWT is valid (contract D38: at most 12 hours; RFC 8292 2
// allows 24).
const VAPIDLifetime = 12 * time.Hour

// vapidRefresh is how long before its expiry a cached JWT is replaced.
const vapidRefresh = time.Hour

// GenerateVAPIDKey makes a new P-256 key pair for VAPID.
func GenerateVAPIDKey() (*ecdsa.PrivateKey, error) {
	return ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
}

// ParseVAPIDKey reads a private key VAPIDKeyBytes wrote: the 32-byte P-256 scalar.
func ParseVAPIDKey(raw []byte) (*ecdsa.PrivateKey, error) {
	return ecdsa.ParseRawPrivateKey(elliptic.P256(), raw)
}

// VAPIDKeyBytes is the 32-byte scalar of key, what credentials.yaml seals.
func VAPIDKeyBytes(key *ecdsa.PrivateKey) ([]byte, error) { return key.Bytes() }

// VAPID signs push requests (RFC 8292) with one key and contact. It caches one JWT per
// audience until an hour before it expires. Safe for concurrent use.
type VAPID struct {
	key     *ecdsa.PrivateKey
	pub     string // the uncompressed public key, unpadded base64url (the k parameter)
	subject string
	now     func() time.Time

	mu    sync.Mutex
	cache map[string]vapidJWT
}

type vapidJWT struct {
	token string
	exp   time.Time
}

// NewVAPID is a signer with key and subject (push.contact: mailto: or https:). now is the clock;
// nil means time.Now.
func NewVAPID(key *ecdsa.PrivateKey, subject string, now func() time.Time) (*VAPID, error) {
	pub, err := key.PublicKey.Bytes()
	if err != nil {
		return nil, err
	}
	if now == nil {
		now = time.Now
	}
	return &VAPID{key: key, pub: b64.EncodeToString(pub), subject: subject, now: now, cache: map[string]vapidJWT{}}, nil
}

// PublicKey is the applicationServerKey browsers subscribe with: the uncompressed public key in
// unpadded base64url.
func (v *VAPID) PublicKey() string { return v.pub }

// Audience is the origin of a push endpoint, the JWT's aud (RFC 8292 2): scheme://host[:port].
func Audience(endpoint string) (string, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", errors.New("push: the endpoint is not an absolute URL")
	}
	return u.Scheme + "://" + u.Host, nil
}

// Authorization is the Authorization header of a push to endpoint: vapid t=<jwt>, k=<key>.
func (v *VAPID) Authorization(endpoint string) (string, error) {
	aud, err := Audience(endpoint)
	if err != nil {
		return "", err
	}
	now := v.now()
	v.mu.Lock()
	defer v.mu.Unlock()
	if c, ok := v.cache[aud]; ok && now.Before(c.exp.Add(-vapidRefresh)) {
		return "vapid t=" + c.token + ", k=" + v.pub, nil
	}
	exp := now.Add(VAPIDLifetime).Truncate(time.Second)
	token, err := v.sign(aud, exp)
	if err != nil {
		return "", err
	}
	for a, c := range v.cache { // drop the expired, so the map stays as small as the live audiences
		if !now.Before(c.exp) {
			delete(v.cache, a)
		}
	}
	v.cache[aud] = vapidJWT{token: token, exp: exp}
	return "vapid t=" + token + ", k=" + v.pub, nil
}

// sign is the ES256 JWT {aud, exp, sub}: the signature is r and s, 32 bytes each (RFC 7518 3.4).
func (v *VAPID) sign(aud string, exp time.Time) (string, error) {
	claims, err := json.Marshal(struct {
		Aud string `json:"aud"`
		Exp int64  `json:"exp"`
		Sub string `json:"sub"`
	}{aud, exp.Unix(), v.subject})
	if err != nil {
		return "", err
	}
	input := b64.EncodeToString([]byte(`{"typ":"JWT","alg":"ES256"}`)) + "." + b64.EncodeToString(claims)
	sum := sha256.Sum256([]byte(input))
	der, err := ecdsa.SignASN1(rand.Reader, v.key, sum[:])
	if err != nil {
		return "", err
	}
	var rs struct{ R, S *big.Int }
	if rest, err := asn1.Unmarshal(der, &rs); err != nil || len(rest) != 0 {
		return "", errors.New("push: an ECDSA signature that does not parse")
	}
	sig := make([]byte, 64)
	rs.R.FillBytes(sig[:32])
	rs.S.FillBytes(sig[32:])
	return input + "." + b64.EncodeToString(sig), nil
}
