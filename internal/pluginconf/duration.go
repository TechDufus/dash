package pluginconf

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Duration is a time.Duration written as a Go duration string ("30m") in the config file and
// the environment.
type Duration time.Duration

func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return errors.New(`expected a duration string such as "30s"`)
	}
	v, err := time.ParseDuration(strings.TrimSpace(s))
	if err != nil {
		return fmt.Errorf("%q is not a duration such as 30s or 5m", s)
	}
	*d = Duration(v)
	return nil
}

func (d Duration) MarshalJSON() ([]byte, error) { return json.Marshal(d.String()) }

// String writes d as an operator would: 30m, not 30m0s.
func (d Duration) String() string {
	s := time.Duration(d).String()
	if strings.HasSuffix(s, "m0s") {
		s = strings.TrimSuffix(s, "0s")
	}
	if strings.HasSuffix(s, "h0m") {
		s = strings.TrimSuffix(s, "0m")
	}
	return s
}

// JSONSchema makes a Duration a string with format "duration" in the generated schema. The
// result is the jsonschema.Schema of internal/jsonschema, which is a plain map, so this package
// does not import it.
func (Duration) JSONSchema() map[string]any {
	return map[string]any{"type": "string", "format": "duration", "examples": []string{"30s", "5m", "1h30m"}}
}
