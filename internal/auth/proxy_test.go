package auth

import (
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

// hdr builds a header from name/value pairs, adding one line per pair (so a repeated name is a
// repeated header line).
func hdr(kv ...string) http.Header {
	h := http.Header{}
	for i := 0; i < len(kv); i += 2 {
		h.Add(kv[i], kv[i+1])
	}
	return h
}

func TestProxyIdentityFrom(t *testing.T) {
	pi := ProxyIdentity{UserHeaders: []string{"X-Auth-Request-Preferred-Username", "X-Auth-Request-User", "X-Forwarded-User"}}
	for _, c := range []struct {
		name    string
		pi      ProxyIdentity
		h       http.Header
		user    string
		via     string
		wantErr error
	}{
		{name: "no headers", pi: pi, h: hdr()},
		{name: "first header wins", pi: pi, h: hdr("X-Auth-Request-Preferred-Username", "alice", "X-Auth-Request-User", "bob"), user: "alice", via: "X-Auth-Request-Preferred-Username"},
		{name: "second header when first absent", pi: pi, h: hdr("X-Auth-Request-User", "bob"), user: "bob", via: "X-Auth-Request-User"},
		{name: "third header", pi: pi, h: hdr("X-Forwarded-User", "carol"), user: "carol", via: "X-Forwarded-User"},
		{name: "order follows config not header map", pi: ProxyIdentity{UserHeaders: []string{"X-Forwarded-User", "X-Auth-Request-User"}}, h: hdr("X-Auth-Request-User", "bob", "X-Forwarded-User", "carol"), user: "carol", via: "X-Forwarded-User"},
		{name: "empty preferred falls through", pi: pi, h: hdr("X-Auth-Request-Preferred-Username", "", "X-Auth-Request-User", "bob"), user: "bob", via: "X-Auth-Request-User"},
		{name: "blank preferred falls through", pi: pi, h: hdr("X-Auth-Request-Preferred-Username", "  \t ", "X-Auth-Request-User", "bob"), user: "bob", via: "X-Auth-Request-User"},
		{name: "all empty", pi: pi, h: hdr("X-Auth-Request-Preferred-Username", "", "X-Auth-Request-User", " "), user: ""},
		{name: "trimmed", pi: pi, h: hdr("X-Auth-Request-User", "  bob \t"), user: "bob", via: "X-Auth-Request-User"},
		{name: "inner space kept", pi: pi, h: hdr("X-Auth-Request-User", "Bob Smith"), user: "Bob Smith", via: "X-Auth-Request-User"},
		{name: "case preserved", pi: pi, h: hdr("X-Auth-Request-User", "Bob"), user: "Bob", via: "X-Auth-Request-User"},
		{name: "unicode ok", pi: pi, h: hdr("X-Auth-Request-User", "zoë-ж"), user: "zoë-ж", via: "X-Auth-Request-User"},
		{name: "exactly 128 bytes", pi: pi, h: hdr("X-Auth-Request-User", strings.Repeat("a", 128)), user: strings.Repeat("a", 128), via: "X-Auth-Request-User"},
		{name: "129 bytes no identity", pi: pi, h: hdr("X-Auth-Request-User", strings.Repeat("a", 129))},
		{name: "128 bytes after trim ok", pi: pi, h: hdr("X-Auth-Request-User", " "+strings.Repeat("a", 128)+" "), user: strings.Repeat("a", 128), via: "X-Auth-Request-User"},
		{name: "multibyte counted in bytes", pi: pi, h: hdr("X-Auth-Request-User", strings.Repeat("é", 65))}, // 130 bytes
		{name: "multibyte 64 runes is 128 bytes", pi: pi, h: hdr("X-Auth-Request-User", strings.Repeat("é", 64)), user: strings.Repeat("é", 64), via: "X-Auth-Request-User"},
		{name: "invalid utf-8", pi: pi, h: hdr("X-Auth-Request-User", "bob\xff"), user: ""},
		{name: "newline", pi: pi, h: hdr("X-Auth-Request-User", "bo\nb"), user: ""},
		{name: "carriage return", pi: pi, h: hdr("X-Auth-Request-User", "bo\rb"), user: ""},
		{name: "tab inside", pi: pi, h: hdr("X-Auth-Request-User", "bo\tb"), user: ""},
		{name: "NUL", pi: pi, h: hdr("X-Auth-Request-User", "bo\x00b"), user: ""},
		{name: "DEL", pi: pi, h: hdr("X-Auth-Request-User", "bo\x7fb"), user: ""},
		{name: "C1 control", pi: pi, h: hdr("X-Auth-Request-User", "bo\u0085b"), user: ""},
		{name: "invalid chosen header does not fall through", pi: pi, h: hdr("X-Auth-Request-Preferred-Username", "bo\x7fb", "X-Auth-Request-User", "bob"), user: ""},
		{name: "too long chosen header does not fall through", pi: pi, h: hdr("X-Auth-Request-Preferred-Username", strings.Repeat("a", 129), "X-Auth-Request-User", "bob"), user: ""},
		{name: "two lines ambiguous", pi: pi, h: hdr("X-Auth-Request-User", "evil", "X-Auth-Request-User", "bob"), wantErr: ErrAmbiguousIdentity},
		{name: "two identical lines ambiguous", pi: pi, h: hdr("X-Auth-Request-User", "bob", "X-Auth-Request-User", "bob"), wantErr: ErrAmbiguousIdentity},
		{name: "empty then value is ambiguous", pi: pi, h: hdr("X-Auth-Request-User", "", "X-Auth-Request-User", "bob"), wantErr: ErrAmbiguousIdentity},
		{name: "two lines of the preferred header win over the next", pi: pi, h: hdr("X-Auth-Request-Preferred-Username", "a", "X-Auth-Request-Preferred-Username", "b", "X-Auth-Request-User", "bob"), wantErr: ErrAmbiguousIdentity},
		{name: "second header not consulted once first chosen (two lines there)", pi: pi, h: hdr("X-Auth-Request-Preferred-Username", "alice", "X-Auth-Request-User", "x", "X-Auth-Request-User", "y"), user: "alice", via: "X-Auth-Request-Preferred-Username"},
		{name: "second header not consulted once first chosen (invalid there)", pi: pi, h: hdr("X-Auth-Request-Preferred-Username", "alice", "X-Auth-Request-User", "b\x00d"), user: "alice", via: "X-Auth-Request-Preferred-Username"},
		{name: "two empty lines skipped", pi: pi, h: hdr("X-Auth-Request-Preferred-Username", "", "X-Auth-Request-Preferred-Username", "", "X-Auth-Request-User", "bob"), user: "bob", via: "X-Auth-Request-User"},
		{name: "via canonicalised from lowercase config", pi: ProxyIdentity{UserHeaders: []string{"x-auth-request-user"}}, h: hdr("X-Auth-Request-User", "bob"), user: "bob", via: "X-Auth-Request-User"},
		{name: "via canonicalised from shouting config", pi: ProxyIdentity{UserHeaders: []string{"X-AUTH-REQUEST-USER"}}, h: hdr("x-auth-request-user", "bob"), user: "bob", via: "X-Auth-Request-User"},
		{name: "no user headers configured", pi: ProxyIdentity{}, h: hdr("X-Auth-Request-User", "bob")},
		{name: "groups header is not a user header", pi: ProxyIdentity{UserHeaders: []string{"X-User"}, GroupsHeader: "X-Groups"}, h: hdr("X-Groups", "ops")},
	} {
		t.Run(c.name, func(t *testing.T) {
			user, groups, via, err := c.pi.From(c.h)
			if c.wantErr != nil {
				if !errors.Is(err, c.wantErr) {
					t.Fatalf("err = %v, want %v", err, c.wantErr)
				}
				if user != "" || groups != nil || via != "" {
					t.Errorf("identity (%q, %v, %q) returned with an error", user, groups, via)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if user != c.user || via != c.via {
				t.Errorf("From = (%q, via %q), want (%q, via %q)", user, via, c.user, c.via)
			}
			if user == "" && groups != nil {
				t.Errorf("groups %v returned without an identity", groups)
			}
		})
	}
}

func TestGroupsParsing(t *testing.T) {
	const user = "X-User"
	long := strings.Repeat("g", 256)
	var many []string
	for i := range 70 {
		many = append(many, fmt.Sprintf("g%d", i))
	}
	want64 := many[:64]
	for _, c := range []struct {
		name    string
		pi      ProxyIdentity
		h       http.Header
		want    []string
		wantErr error
	}{
		{name: "split", pi: ProxyIdentity{UserHeaders: []string{user}, GroupsHeader: "X-Groups"}, h: hdr(user, "bob", "X-Groups", "a,b,c"), want: []string{"a", "b", "c"}},
		{name: "trim", pi: ProxyIdentity{UserHeaders: []string{user}, GroupsHeader: "X-Groups"}, h: hdr(user, "bob", "X-Groups", " a , b\t,c "), want: []string{"a", "b", "c"}},
		{name: "empty entries dropped", pi: ProxyIdentity{UserHeaders: []string{user}, GroupsHeader: "X-Groups"}, h: hdr(user, "bob", "X-Groups", ",a,, ,b,"), want: []string{"a", "b"}},
		{name: "dedupe keeps first order", pi: ProxyIdentity{UserHeaders: []string{user}, GroupsHeader: "X-Groups"}, h: hdr(user, "bob", "X-Groups", "b,a,b,c,a"), want: []string{"b", "a", "c"}},
		{name: "dedupe is exact", pi: ProxyIdentity{UserHeaders: []string{user}, GroupsHeader: "X-Groups"}, h: hdr(user, "bob", "X-Groups", "Ops,ops"), want: []string{"Ops", "ops"}},
		{name: "inner spaces kept", pi: ProxyIdentity{UserHeaders: []string{user}, GroupsHeader: "X-Groups"}, h: hdr(user, "bob", "X-Groups", "team a,team b"), want: []string{"team a", "team b"}},
		{name: "all empty", pi: ProxyIdentity{UserHeaders: []string{user}, GroupsHeader: "X-Groups"}, h: hdr(user, "bob", "X-Groups", " , ,"), want: nil},
		{name: "header absent", pi: ProxyIdentity{UserHeaders: []string{user}, GroupsHeader: "X-Groups"}, h: hdr(user, "bob"), want: nil},
		{name: "cap of 64", pi: ProxyIdentity{UserHeaders: []string{user}, GroupsHeader: "X-Groups"}, h: hdr(user, "bob", "X-Groups", strings.Join(many, ",")), want: want64},
		{name: "duplicates do not eat the cap", pi: ProxyIdentity{UserHeaders: []string{user}, GroupsHeader: "X-Groups"}, h: hdr(user, "bob", "X-Groups", strings.Repeat("dup,", 100)+strings.Join(many, ",")), want: append([]string{"dup"}, many[:63]...)},
		{name: "256 bytes kept", pi: ProxyIdentity{UserHeaders: []string{user}, GroupsHeader: "X-Groups"}, h: hdr(user, "bob", "X-Groups", long+",a"), want: []string{long, "a"}},
		{name: "257 bytes dropped", pi: ProxyIdentity{UserHeaders: []string{user}, GroupsHeader: "X-Groups"}, h: hdr(user, "bob", "X-Groups", long+"x,a"), want: []string{"a"}},
		{name: "invalid utf-8 entry dropped", pi: ProxyIdentity{UserHeaders: []string{user}, GroupsHeader: "X-Groups"}, h: hdr(user, "bob", "X-Groups", "a,b\xff,c"), want: []string{"a", "c"}},
		{name: "control entry dropped", pi: ProxyIdentity{UserHeaders: []string{user}, GroupsHeader: "X-Groups"}, h: hdr(user, "bob", "X-Groups", "a,b\x7f,c\u0085c,d"), want: []string{"a", "d"}},
		{name: "groups off when header unset", pi: ProxyIdentity{UserHeaders: []string{user}}, h: hdr(user, "bob", "X-Groups", "ops"), want: nil},
		{name: "groups off ignores two lines", pi: ProxyIdentity{UserHeaders: []string{user}}, h: hdr(user, "bob", "X-Groups", "a", "X-Groups", "b"), want: nil},
		{name: "lowercase groups config", pi: ProxyIdentity{UserHeaders: []string{user}, GroupsHeader: "x-groups"}, h: hdr(user, "bob", "X-Groups", "ops"), want: []string{"ops"}},
		{name: "two lines ambiguous", pi: ProxyIdentity{UserHeaders: []string{user}, GroupsHeader: "X-Groups"}, h: hdr(user, "bob", "X-Groups", "evil", "X-Groups", "ops"), wantErr: ErrAmbiguousIdentity},
		{name: "no identity no groups and no error", pi: ProxyIdentity{UserHeaders: []string{user}, GroupsHeader: "X-Groups"}, h: hdr("X-Groups", "ops"), want: nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			u, groups, _, err := c.pi.From(c.h)
			if c.wantErr != nil {
				if !errors.Is(err, c.wantErr) {
					t.Fatalf("err = %v, want %v", err, c.wantErr)
				}
				if u != "" || groups != nil {
					t.Errorf("identity (%q, %v) returned with an error", u, groups)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(groups, c.want) {
				t.Errorf("groups = %q, want %q", groups, c.want)
			}
		})
	}
}

func TestHeaderNameRules(t *testing.T) {
	for _, c := range []struct {
		name    string
		pi      ProxyIdentity
		wantErr string // substring, "" for valid
	}{
		{"typical", ProxyIdentity{UserHeaders: []string{"X-Auth-Request-User"}, GroupsHeader: "X-Auth-Request-Groups"}, ""},
		{"several user headers", ProxyIdentity{UserHeaders: []string{"X-A", "x-b", "X_C"}}, ""},
		{"groups off", ProxyIdentity{UserHeaders: []string{"X-User"}, GroupsHeader: ""}, ""},
		{"token punctuation", ProxyIdentity{UserHeaders: []string{"!#$%&'*+-.^_`|~"}}, ""},
		{"empty list", ProxyIdentity{}, "at least one"},
		{"empty list with groups", ProxyIdentity{GroupsHeader: "X-Groups"}, "at least one"},
		{"empty name", ProxyIdentity{UserHeaders: []string{""}}, "empty"},
		{"space in name", ProxyIdentity{UserHeaders: []string{"X User"}}, "valid HTTP header name"},
		{"colon in name", ProxyIdentity{UserHeaders: []string{"X-User:"}}, "valid HTTP header name"},
		{"newline in name", ProxyIdentity{UserHeaders: []string{"X-User\nX-Evil"}}, "valid HTTP header name"},
		{"non-ascii name", ProxyIdentity{UserHeaders: []string{"X-Üser"}}, "valid HTTP header name"},
		{"parens in name", ProxyIdentity{UserHeaders: []string{"X(User)"}}, "valid HTTP header name"},
		{"bad second user header", ProxyIdentity{UserHeaders: []string{"X-User", "bad name"}}, `"bad name"`},
		{"bad groups header", ProxyIdentity{UserHeaders: []string{"X-User"}, GroupsHeader: "bad name"}, `"bad name"`},
		{"Authorization", ProxyIdentity{UserHeaders: []string{"Authorization"}}, "Authorization"},
		{"authorization lower", ProxyIdentity{UserHeaders: []string{"authorization"}}, "Authorization"},
		{"AUTHORIZATION upper", ProxyIdentity{UserHeaders: []string{"AUTHORIZATION"}}, "Authorization"},
		{"Cookie", ProxyIdentity{UserHeaders: []string{"Cookie"}}, "Cookie"},
		{"cookie lower", ProxyIdentity{UserHeaders: []string{"cookie"}}, "Cookie"},
		{"Host", ProxyIdentity{UserHeaders: []string{"Host"}}, "Host"},
		{"HOST upper", ProxyIdentity{UserHeaders: []string{"HOST"}}, "Host"},
		{"X-Dash-Csrf", ProxyIdentity{UserHeaders: []string{"X-Dash-Csrf"}}, "X-Dash-Csrf"},
		{"x-dash-csrf lower", ProxyIdentity{UserHeaders: []string{"x-dash-csrf"}}, "X-Dash-Csrf"},
		{"forbidden as second user header", ProxyIdentity{UserHeaders: []string{"X-User", "cookie"}}, "Cookie"},
		{"forbidden as groups header", ProxyIdentity{UserHeaders: []string{"X-User"}, GroupsHeader: "Authorization"}, "Authorization"},
		{"forbidden groups header lower", ProxyIdentity{UserHeaders: []string{"X-User"}, GroupsHeader: "x-dash-csrf"}, "X-Dash-Csrf"},
		{"similar name is fine", ProxyIdentity{UserHeaders: []string{"X-Authorization", "Cookies", "Hosts"}}, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			err := c.pi.Validate()
			if c.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Fatalf("Validate() = %v, want error containing %q", err, c.wantErr)
			}
		})
	}
}
