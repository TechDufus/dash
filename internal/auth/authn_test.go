package auth

import (
	"net/http"
	"testing"
)

func TestBearer(t *testing.T) {
	for _, c := range []struct {
		name   string
		header string // "" with set=false means no header
		set    bool
		token  string
		want   bool
	}{
		{"no header", "", false, "", false},
		{"empty header", "", true, "", false},
		{"blank header", "   ", true, "", false},
		{"Bearer", "Bearer abc", true, "abc", true},
		{"bearer", "bearer abc", true, "abc", true},
		{"BEARER", "BEARER abc", true, "abc", true},
		{"BeArEr", "BeArEr abc", true, "abc", true},
		{"empty token with space", "Bearer ", true, "", true},
		{"empty token bare scheme", "Bearer", true, "", true},
		{"extra spaces", "Bearer    abc  ", true, "abc", true},
		{"tab separator", "Bearer\tabc", true, "abc", true},
		{"leading space", "  Bearer abc", true, "abc", true},
		{"token keeps inner content", "Bearer a.b-c_d~+/=", true, "a.b-c_d~+/=", true},
		{"Basic ignored", "Basic dXNlcjpwYXNz", true, "", false},
		{"Digest ignored", `Digest username="a"`, true, "", false},
		{"Bearerx not present", "Bearerx abc", true, "", false},
		{"Bearer- not present", "Bearer-abc", true, "", false},
		{"token alone not present", "abc", true, "", false},
		{"short value", "Bear", true, "", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := &http.Request{Header: http.Header{}}
			if c.set {
				r.Header.Set("Authorization", c.header)
			}
			tok, ok := Bearer(r)
			if ok != c.want || tok != c.token {
				t.Errorf("Bearer(%q) = (%q, %v), want (%q, %v)", c.header, tok, ok, c.token, c.want)
			}
		})
	}
}

// Any line may carry the Bearer: an earlier Basic line does not hide it.
func TestBearerOnAnyAuthorizationLine(t *testing.T) {
	for _, c := range []struct {
		name  string
		lines []string
		token string
		want  bool
	}{
		{"Basic then Bearer", []string{"Basic dXNlcjpwYXNz", "Bearer x"}, "x", true},
		{"Bearer then Basic", []string{"Bearer x", "Basic dXNlcjpwYXNz"}, "x", true},
		{"empty then Bearer", []string{"", "Bearer x"}, "x", true},
		{"two Bearers, the first token", []string{"Bearer a", "Bearer b"}, "a", true},
		{"Basic then Basic", []string{"Basic a", "Basic b"}, "", false},
		{"Basic then a lookalike", []string{"Basic a", "Bearerx b"}, "", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := &http.Request{Header: http.Header{}}
			for _, l := range c.lines {
				r.Header.Add("Authorization", l)
			}
			tok, ok := Bearer(r)
			if ok != c.want || tok != c.token {
				t.Errorf("Bearer(%q) = (%q, %v), want (%q, %v)", c.lines, tok, ok, c.token, c.want)
			}
		})
	}
}
