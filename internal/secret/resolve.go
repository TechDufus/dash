package secret

import (
	"fmt"
	"os"
	"strings"
)

// Resolve turns a configured string into a Value (design C.3):
//
//	""            unset (zero Value)
//	env:NAME      the process environment variable NAME
//	file:/path    the file's contents with surrounding whitespace trimmed
//	k8s:...       rejected: the kubernetes plugin resolves these, not this package
//	anything else a literal
//
// path is the config field path (for example "plugins.proxmox.tokenSecret"). It
// prefixes every error so a failed startup names the field, and no error ever
// contains the secret.
func Resolve(raw, path string) (Value, error) {
	switch {
	case raw == "":
		return Value{}, nil
	case strings.HasPrefix(raw, "env:"):
		name := strings.TrimPrefix(raw, "env:")
		if name == "" {
			return Value{}, fmt.Errorf("%s: env reference has no variable name", path)
		}
		val, ok := os.LookupEnv(name)
		if !ok {
			return Value{}, fmt.Errorf("%s: environment variable %s is not set", path, name)
		}
		val = strings.TrimSpace(val)
		if val == "" {
			return Value{}, fmt.Errorf("%s: environment variable %s is empty", path, name)
		}
		return New(val, raw), nil
	case strings.HasPrefix(raw, "file:"):
		file := strings.TrimPrefix(raw, "file:")
		if file == "" {
			return Value{}, fmt.Errorf("%s: file reference has no path", path)
		}
		b, err := os.ReadFile(file)
		if err != nil {
			return Value{}, fmt.Errorf("%s: cannot read secret file: %w", path, err)
		}
		val := strings.TrimSpace(string(b))
		if val == "" {
			return Value{}, fmt.Errorf("%s: secret file %s is empty", path, file)
		}
		return New(val, raw), nil
	case strings.HasPrefix(raw, "k8s:"):
		return Value{}, fmt.Errorf("%s: k8s: references are resolved by the kubernetes plugin and are not available to this field", path)
	case strings.HasPrefix(raw, "op://"):
		return Value{}, fmt.Errorf("%s: op:// references are not built in; materialise the secret as an env variable or a file and use env: or file:", path)
	}
	return New(raw, ""), nil
}
