package main

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"

	"github.com/TechDufus/dash/internal/credentials"
)

// tokenUsage is the usage line of `dash token generate`.
const tokenUsage = "dash token generate"

// staticTokenPrefix starts a static token: dash_st_<43 base64url characters of 32 random bytes>.
// The log scrubber redacts the whole token by it (internal/secret), and the bearer step refuses
// any credential that does not start with dash_.
const staticTokenPrefix = "dash_st_"

// tokenCommand is `dash token generate` (design M.5): a new static token and its hash. The token
// goes to the client that will present it; only the hash goes into auth.tokens.
func tokenCommand(args []string, out io.Writer) error {
	if len(args) != 1 || args[0] != "generate" {
		return fmt.Errorf("usage: %s", tokenUsage)
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return err
	}
	token := staticTokenPrefix + base64.RawURLEncoding.EncodeToString(raw)
	hash := credentials.HashToken(token)
	_, err := fmt.Fprintf(out, "token: %s\nhash:  %s\n\nGive the token to the client, once; put only the hash into auth.tokens:\n"+
		"  - {name: <name>, role: viewer, audience: api, scopes: [read], hash: \"%s\"}\n", token, hash, hash)
	return err
}
