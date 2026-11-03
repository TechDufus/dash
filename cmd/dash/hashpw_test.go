package main

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/TechDufus/dash/internal/localauth"
)

func TestHashPasswordCLI(t *testing.T) {
	const pw = "correct horse battery"
	// A pipe is an *os.File that is not a terminal: the stdin path, as `echo … | dash
	// hash-password` runs it.
	pipe := func(s string) io.Reader {
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { r.Close() })
		go func() { _, _ = io.WriteString(w, s); w.Close() }()
		return r
	}
	for _, tc := range []struct {
		name string
		in   io.Reader
	}{
		{"pipe with newline", pipe(pw + "\n")},
		{"reader with CRLF", strings.NewReader(pw + "\r\n")},
		{"no line ending", strings.NewReader(pw)},
		{"only the first line", strings.NewReader(pw + "\nsecond line\n")},
	} {
		var out bytes.Buffer
		if err := hashPasswordCommand(nil, tc.in, &out); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		got := out.String()
		phc, ok := strings.CutSuffix(got, "\n")
		if !ok || strings.Contains(phc, "\n") || !strings.HasPrefix(phc, "$argon2id$v=19$m=65536,t=3,p=4$") {
			t.Fatalf("%s: output %q, want exactly one argon2id PHC line", tc.name, got)
		}
		if strings.Contains(got, pw) {
			t.Fatalf("%s: the output contains the password", tc.name)
		}
		if ok, err := localauth.VerifyPassword(phc, pw); err != nil || !ok {
			t.Errorf("%s: the printed hash does not verify the password: %v %v", tc.name, ok, err)
		}
	}

	for _, tc := range []struct {
		name string
		args []string
		in   string
	}{
		{"too short", nil, "short-pw\n"},
		{"empty", nil, ""},
		{"too long", nil, strings.Repeat("p", localauth.MaxPasswordBytes+1) + "\n"},
		{"far too long", nil, strings.Repeat("p", 4096)},
		{"an argument", []string{pw}, pw + "\n"},
	} {
		var out bytes.Buffer
		err := hashPasswordCommand(tc.args, strings.NewReader(tc.in), &out)
		if err == nil || out.Len() != 0 {
			t.Errorf("%s: err %v, output %q; want an error and no output", tc.name, err, out.String())
			continue
		}
		if line := strings.TrimSpace(tc.in); line != "" && strings.Contains(err.Error(), line) {
			t.Errorf("%s: the error quotes the password", tc.name)
		}
		if exitCode(err) != 2 {
			t.Errorf("%s: exit code %d, want 2 (usage)", tc.name, exitCode(err))
		}
	}
}
