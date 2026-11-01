package pluginconf

import (
	"testing"
	"time"
)

func TestDurationIsCompactInTheEnvironmentAndTheFile(t *testing.T) {
	for in, want := range map[time.Duration]string{
		30 * time.Minute:              "30m",
		10 * time.Second:              "10s",
		time.Hour:                     "1h",
		90 * time.Minute:              "1h30m",
		1500 * time.Millisecond:       "1.5s",
		time.Hour + 5*time.Second:     "1h0m5s",
		0:                             "0s",
		2*time.Minute + 3*time.Second: "2m3s",
	} {
		if got := Duration(in).String(); got != want {
			t.Errorf("String(%v) = %q, want %q", in, got, want)
		}
		b, err := Duration(in).MarshalJSON()
		if err != nil || string(b) != `"`+want+`"` {
			t.Errorf("MarshalJSON(%v) = %s, %v", in, b, err)
		}
		var back Duration
		if err := back.UnmarshalJSON(b); err != nil || back != Duration(in) {
			t.Errorf("round trip of %v: %v, %v", in, back, err)
		}
	}
}

func TestDurationRejectsWhatIsNotADuration(t *testing.T) {
	for _, in := range []string{`30`, `"30"`, `"soon"`, `true`, `null5`} {
		var d Duration
		if err := d.UnmarshalJSON([]byte(in)); err == nil {
			t.Errorf("%s accepted as %v", in, d)
		}
	}
	var d Duration
	if err := d.UnmarshalJSON([]byte(`" 5s "`)); err != nil || d != Duration(5*time.Second) {
		t.Errorf("padded duration = %v, %v", d, err)
	}
}

func TestDurationSchema(t *testing.T) {
	s := Duration(0).JSONSchema()
	if s["type"] != "string" || s["format"] != "duration" {
		t.Errorf("schema = %v", s)
	}
}
