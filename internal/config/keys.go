package config

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/TechDufus/dash/internal/secret"
)

// The secret keys are process environment, not tree fields (design C.4): a key stored in the
// settings it protects would protect nothing, and it must never be shown by the settings API.
const (
	envSecretKey         = "DASH_SECRET_KEY"
	envSecretKeyPrevious = "DASH_SECRET_KEY_PREVIOUS"
	envAdminPasswordHash = "DASH_ADMIN_PASSWORD_HASH"
)

// AdminPasswordHash reads the local account's password hash from DASH_ADMIN_PASSWORD_HASH (or
// _FILE), "" when neither is set. Like the secret keys it is process environment, not a tree
// field: a credential the settings API must never show. The text is registered with reg, so it
// can never reach a log line; nothing here echoes it. Whether it is an argon2id hash is the
// local mode's check.
func AdminPasswordHash(environ []string, reg *secret.Registry) (string, error) {
	s, _, err := readEnvSecret(envMap(environ), envAdminPasswordHash, reg, true)
	return s, err
}

// SecretKeys reads the keys that seal UI-entered secrets: DASH_SECRET_KEY (or _FILE) and, while
// a rotation is in progress, DASH_SECRET_KEY_PREVIOUS (or _FILE). Without a current key the
// result is nil, which means "no key": secrets can only come from env and file references, and
// dash otherwise runs as usual. The key text is registered with reg so it can never reach a
// log line or an API response; nothing here echoes it.
func SecretKeys(environ []string, reg *secret.Registry) (*secret.Keyring, error) {
	return secretKeys(environ, reg, true)
}

// CheckSecretKeys applies the rules of SecretKeys that need no key file: a key given directly
// must parse, a name and its _FILE form are not both set, and a previous key needs a current
// one. A _FILE form counts as set without being read (`dash config validate` does not read
// files unless asked).
func CheckSecretKeys(environ []string) error {
	_, err := secretKeys(environ, nil, false)
	return err
}

func secretKeys(environ []string, reg *secret.Registry, readFiles bool) (*secret.Keyring, error) {
	env := envMap(environ)
	cur, curSet, curErr := readKey(env, envSecretKey, reg, readFiles)
	prev, prevSet, prevErr := readKey(env, envSecretKeyPrevious, reg, readFiles)
	if err := errors.Join(curErr, prevErr); err != nil {
		return nil, err
	}
	switch {
	case !curSet && !prevSet:
		return nil, nil
	case !curSet:
		return nil, fmt.Errorf("%s is set but %s is not: the previous key only decrypts, new values need the current key", envSecretKeyPrevious, envSecretKey)
	}
	return secret.NewKeyring(cur, prev), nil
}

// readKey parses the key NAME or NAME_FILE holds and reports whether there is one. Empty counts
// as unset, as for every other variable; both set is an error naming both. Without readFiles a
// NAME_FILE is reported as set and not opened.
func readKey(env map[string]string, name string, reg *secret.Registry, readFiles bool) (k secret.Key, set bool, err error) {
	s, set, err := readEnvSecret(env, name, reg, readFiles)
	if err != nil || !set || s == "" {
		return k, set, err
	}
	src := name
	if strings.TrimSpace(env[name+"_FILE"]) != "" {
		src = name + "_FILE"
	}
	if k, err = secret.ParseKey(s); err != nil {
		return k, false, fmt.Errorf("%s: %w", src, err)
	}
	return k, true, nil
}

// readEnvSecret reads the credential NAME or NAME_FILE holds, trimmed, and reports whether one is
// set. Empty counts as unset; both set is an error naming both; an empty file is an error.
// Without readFiles a NAME_FILE is reported as set and not opened (the value is then ""). The
// value is registered with reg and never quoted in an error.
func readEnvSecret(env map[string]string, name string, reg *secret.Registry, readFiles bool) (string, bool, error) {
	direct, file := strings.TrimSpace(env[name]), strings.TrimSpace(env[name+"_FILE"])
	switch {
	case direct != "" && file != "":
		return "", false, fmt.Errorf("%s and %s_FILE are both set; set only one", name, name)
	case file != "" && !readFiles:
		return "", true, nil
	case file != "":
		b, err := os.ReadFile(file)
		if err != nil {
			return "", false, fmt.Errorf("%s_FILE: %w", name, err)
		}
		direct = strings.TrimSpace(string(b))
		if direct == "" {
			return "", false, fmt.Errorf("%s_FILE: file %s is empty", name, file)
		}
	case direct == "":
		return "", false, nil
	}
	if reg != nil {
		reg.AddString(direct)
	}
	return direct, true, nil
}
