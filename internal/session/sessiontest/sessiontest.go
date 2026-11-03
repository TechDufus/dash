// Package sessiontest builds session managers for tests in other packages (the local and OIDC
// routes, the API's authn), so each does not derive its own key and pick its own cookie names.
package sessiontest

import (
	"bytes"
	"time"

	"github.com/TechDufus/dash/internal/session"
)

// Key is a fixed codec key, so a test can open what a manager sealed (TestSessionPayloadHoldsNoRole
// decrypts a cookie with it).
func Key() []byte { return bytes.Repeat([]byte{0x42}, session.KeySize) }

// Codec is the codec of Key.
func Codec() *session.Codec {
	c, err := session.NewCodec(Key())
	if err != nil {
		panic(err)
	}
	return c
}

// Manager is a session manager over epochs with the codec of Key, the plain-http cookie name
// (dash_session) and the clock now (nil = time.Now).
func Manager(epochs session.Epochs, now func() time.Time) *session.Manager {
	return session.NewManager(session.Options{Codec: Codec(), Epochs: epochs, Now: now})
}
