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
)

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
	direct, file := strings.TrimSpace(env[name]), strings.TrimSpace(env[name+"_FILE"])
	src := name
	switch {
	case direct != "" && file != "":
		return k, false, fmt.Errorf("%s and %s_FILE are both set; set only one", name, name)
	case file != "" && !readFiles:
		return k, true, nil
	case file != "":
		b, err := os.ReadFile(file)
		if err != nil {
			return k, false, fmt.Errorf("%s_FILE: %w", name, err)
		}
		direct, src = strings.TrimSpace(string(b)), name+"_FILE"
		if direct == "" {
			return k, false, fmt.Errorf("%s: file %s is empty", src, file)
		}
	case direct == "":
		return k, false, nil
	}
	if reg != nil {
		reg.AddString(direct)
	}
	if k, err = secret.ParseKey(direct); err != nil {
		return k, false, fmt.Errorf("%s: %w", src, err)
	}
	return k, true, nil
}
