package secret

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

const canary = "canary/S3cret+Value=0123456789"

type withPublicValue struct {
	Name  string
	Token Value
}

type withPrivateValue struct {
	name  string
	token Value
}

func TestValueNeverPrints(t *testing.T) {
	v := New(canary, "env:TOKEN")
	outputs := map[string]string{
		"%v":     fmt.Sprintf("%v", v),
		"%+v":    fmt.Sprintf("%+v", v),
		"%#v":    fmt.Sprintf("%#v", v),
		"%s":     fmt.Sprintf("%s", v),
		"%q":     fmt.Sprintf("%q", v),
		"%x":     fmt.Sprintf("%x", v),
		"pub%+v": fmt.Sprintf("%+v", withPublicValue{"a", v}),
		"pub%#v": fmt.Sprintf("%#v", withPublicValue{"a", v}),
		"prv%v":  fmt.Sprintf("%v", withPrivateValue{"a", v}),
		"prv%+v": fmt.Sprintf("%+v", withPrivateValue{"a", v}),
		"prv%#v": fmt.Sprintf("%#v", withPrivateValue{"a", v}),
		"ptr%+v": fmt.Sprintf("%+v", &withPrivateValue{"a", v}),
	}
	if b, err := json.Marshal(withPublicValue{"a", v}); err != nil {
		t.Fatal(err)
	} else {
		outputs["json"] = string(b)
	}
	if b, err := json.Marshal(map[string]any{"v": v, "p": &v}); err != nil {
		t.Fatal(err)
	} else {
		outputs["jsonmap"] = string(b)
	}
	for name, out := range outputs {
		if strings.Contains(out, canary) || strings.Contains(out, "S3cret") {
			t.Errorf("%s leaked the secret: %s", name, out)
		}
	}
	if got := fmt.Sprintf("%v %+v %#v", v, v, v); got != "[redacted] [redacted] [redacted]" {
		t.Errorf("printed = %q", got)
	}
}

func TestValueJSONMetadata(t *testing.T) {
	b, _ := json.Marshal(withPublicValue{"n", New(canary, "file:/run/secrets/x")})
	if got, want := string(b), `{"Name":"n","Token":{"set":true,"ref":"file:/run/secrets/x"}}`; got != want {
		t.Errorf("json = %s, want %s", got, want)
	}
	b, _ = json.Marshal(Value{})
	if string(b) != `{"set":false}` {
		t.Errorf("unset json = %s", b)
	}
	b, _ = json.Marshal(New(canary, ""))
	if string(b) != `{"set":true}` {
		t.Errorf("literal json = %s", b)
	}
}

func TestValueNeverLogged(t *testing.T) {
	var buf bytes.Buffer
	// No redacting handler: LogValue alone must keep the secret out.
	log := slog.New(slog.NewJSONHandler(&buf, nil))
	v := New(canary, "env:X")
	log.Info("startup", "token", v, slog.Group("proxmox", slog.Any("secret", v)))
	log.With("token", v).Info("again")
	if strings.Contains(buf.String(), "S3cret") {
		t.Fatalf("log leaked the secret: %s", buf.String())
	}
	if !strings.Contains(buf.String(), `"token":"[redacted]"`) {
		t.Errorf("expected the redacted marker: %s", buf.String())
	}
}

func TestValueZero(t *testing.T) {
	var v Value
	if v.IsSet() || v.Reveal() != "" || v.Ref() != "" {
		t.Errorf("zero value not empty: set=%v reveal=%q ref=%q", v.IsSet(), v.Reveal(), v.Ref())
	}
	if v.Update("x") {
		t.Error("Update on the zero Value must report false")
	}
}

func TestValueRotationSeenByCopies(t *testing.T) {
	orig := New("old-secret-value", "file:/x")
	cp := orig
	holder := struct{ V Value }{orig}
	if !orig.Update("new-secret-value") {
		t.Fatal("Update failed")
	}
	if cp.Reveal() != "new-secret-value" || holder.V.Reveal() != "new-secret-value" {
		t.Errorf("copies did not see the rotation: %q %q", cp.Reveal(), holder.V.Reveal())
	}
	if cp.Ref() != "file:/x" {
		t.Errorf("ref changed: %q", cp.Ref())
	}
}

func TestValueConcurrentRotation(t *testing.T) {
	v := New("start-secret-value", "file:/x")
	reg := NewRegistry()
	reg.Add(v)
	var wg sync.WaitGroup
	for i := range 4 {
		wg.Add(2)
		go func() {
			defer wg.Done()
			for j := range 200 {
				v.Update(fmt.Sprintf("secret-%d-%d-padding", i, j))
			}
		}()
		go func() {
			defer wg.Done()
			for range 200 {
				_ = v.Reveal()
				_ = reg.Scrub("line with start-secret-value inside")
			}
		}()
	}
	wg.Wait()
	if got := reg.Scrub(v.Reveal()); got != redacted {
		t.Errorf("final value not scrubbed: %q", got)
	}
}

func TestResolve(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "token")
	if err := os.WriteFile(file, []byte("  from-file-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	empty := filepath.Join(dir, "empty")
	if err := os.WriteFile(empty, []byte("\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DASH_TEST_SECRET", "from-env-secret")
	t.Setenv("DASH_TEST_EMPTY", "")

	tests := []struct {
		name, raw string
		want, ref string
		set       bool
		wantErr   []string // substrings, all required
	}{
		{name: "unset", raw: ""},
		{name: "literal", raw: "plain-literal", want: "plain-literal", set: true},
		{name: "env", raw: "env:DASH_TEST_SECRET", want: "from-env-secret", ref: "env:DASH_TEST_SECRET", set: true},
		{name: "file trimmed", raw: "file:" + file, want: "from-file-secret", ref: "file:" + file, set: true},
		{name: "env missing", raw: "env:DASH_TEST_NOPE", wantErr: []string{"x.y", "DASH_TEST_NOPE", "not set"}},
		{name: "env empty", raw: "env:DASH_TEST_EMPTY", wantErr: []string{"x.y", "empty"}},
		{name: "env nameless", raw: "env:", wantErr: []string{"x.y", "variable name"}},
		{name: "file missing", raw: "file:" + filepath.Join(dir, "gone"), wantErr: []string{"x.y", "cannot read secret file"}},
		{name: "file empty", raw: "file:" + empty, wantErr: []string{"x.y", "empty"}},
		{name: "file pathless", raw: "file:", wantErr: []string{"x.y", "no path"}},
		{name: "k8s stays strict without K8sRefs", raw: "k8s:dash/proxmox#tokenSecret", wantErr: []string{"x.y", "kubernetes plugin"}},
		{name: "op", raw: "op://vault/item/field", wantErr: []string{"x.y", "not built in"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v, err := Resolve(tt.raw, "x.y")
			if len(tt.wantErr) > 0 {
				if err == nil {
					t.Fatalf("want error, got value (set=%v)", v.IsSet())
				}
				for _, s := range tt.wantErr {
					if !strings.Contains(err.Error(), s) {
						t.Errorf("error %q lacks %q", err, s)
					}
				}
				if v.IsSet() {
					t.Error("a failed Resolve must return the zero Value")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if v.Reveal() != tt.want || v.IsSet() != tt.set || v.Ref() != tt.ref {
				t.Errorf("got reveal=%q set=%v ref=%q; want %q %v %q", v.Reveal(), v.IsSet(), v.Ref(), tt.want, tt.set, tt.ref)
			}
		})
	}
}

func TestResolveErrorsNeverContainValue(t *testing.T) {
	// A literal that looks like a reference to something broken must not be echoed, strict
	// resolution or K8sRefs alike.
	_, err := Resolve("k8s:"+canary, "p")
	if err == nil || strings.Contains(err.Error(), canary) {
		t.Errorf("error = %v", err)
	}
	_, err = NewK8sRefs(nil).Handle("k8s:" + canary)
	if err == nil || strings.Contains(err.Error(), canary) {
		t.Errorf("Handle error = %v", err)
	}
}

func TestRegistryScrubsEncodings(t *testing.T) {
	reg := NewRegistry()
	reg.Add(New(canary, ""))
	b := []byte(canary)
	forms := map[string]string{
		"exact":     canary,
		"std":       base64.StdEncoding.EncodeToString(b),
		"rawstd":    base64.RawStdEncoding.EncodeToString(b),
		"url":       base64.URLEncoding.EncodeToString(b),
		"rawurl":    base64.RawURLEncoding.EncodeToString(b),
		"query":     url.QueryEscape(canary),
		"pathescap": url.PathEscape(canary),
	}
	for name, form := range forms {
		in := "GET /x?token=" + form + " failed"
		got := reg.Scrub(in)
		if got != "GET /x?token=[redacted] failed" {
			t.Errorf("%s form: Scrub(%q) = %q", name, in, got)
		}
	}
	if got := reg.Scrub("nothing to see"); got != "nothing to see" {
		t.Errorf("clean text changed: %q", got)
	}
}

func TestRegistryShortSecret(t *testing.T) {
	reg := NewRegistry()
	reg.Add(New("abc", ""))
	enc := base64.StdEncoding.EncodeToString([]byte("abc")) // "YWJj"
	if got := reg.Scrub("abc " + enc); got != redacted+" "+enc {
		t.Errorf("short secret: exact form is scrubbed, encoded forms are not: %q", got)
	}
}

func TestRegistryLongestFormWins(t *testing.T) {
	reg := NewRegistry()
	reg.Add(New("abcdefgh", ""))
	reg.AddString("abcdefghTAIL")
	if got := reg.Scrub("x abcdefghTAIL y"); got != "x [redacted] y" {
		t.Errorf("got %q", got)
	}
}

func TestScrubPrefixes(t *testing.T) {
	var reg *Registry // shape scrubbing needs no registered secrets
	tests := []struct{ in, want string }{
		{"auth dash_pat_ABCDEFGHIJ_abc-DEF_123 rejected", "auth [redacted] rejected"},
		{"dash_at_AbC123-_xyz.", "[redacted]."},
		{"rt=dash_rt_0123456789_secretPART_x&next=1", "rt=[redacted]&next=1"},
		{"stored enc:v1:0a1b2c3d:QUJD+/xyz== here", "stored [redacted] here"},
		{"setup dash_setup_Abc123_-x", "setup dash_setup_Abc123_-x"},
		{"bare dash_pat_ prefix", "bare dash_pat_ prefix"},
		{"dash_other_token", "dash_other_token"},
	}
	for _, tt := range tests {
		if got := reg.Scrub(tt.in); got != tt.want {
			t.Errorf("Scrub(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestScrubLogLineAddsSetupToken(t *testing.T) {
	reg := NewRegistry()
	reg.Add(New(canary, ""))
	in := "setup dash_setup_Abc123_-x pat dash_pat_id_sec canary " + canary
	want := "setup [redacted] pat [redacted] canary [redacted]"
	if got := reg.ScrubLogLine(in); got != want {
		t.Errorf("ScrubLogLine = %q, want %q", got, want)
	}
	if got := reg.Scrub("dash_setup_Abc123"); got != "dash_setup_Abc123" {
		t.Errorf("Scrub must leave dash_setup_ for the one-time startup line: %q", got)
	}
}

// A very short registered secret (a one-character client secret, say) only over-redacts: it
// never defeats the shapes. With the literals first, "e" or "_" broke "dash_setup_…" or
// "enc:v1:…" apart before the shape could match, and most of the token stayed readable. The
// markers stay intact too. (From the v0.12.0 security verification.)
func TestShortSecretDoesNotDefeatShapeRedaction(t *testing.T) {
	const setupTok = "dash_setup_q5Zk3m0D1yWb8c4tVfN2uE7xLpR9sJhA6oGiKwTnY0Q"
	const encVal = "enc:v1:k1:c2VhbGVkLXNlY3JldC12YWx1ZS1mb3ItdGVzdHM="
	line := `{"msg":"setup token required","url":"https://dash.example.com/setup#token=` + setupTok + `","pw":"` + encVal + `"}`
	for _, short := range []string{"", "e", "a", "s", "_", "d", "1", ":"} {
		reg := NewRegistry()
		if short != "" {
			reg.Add(New(short, ""))
		}
		got := reg.ScrubLogLine(line)
		for _, tok := range []string{setupTok, encVal} {
			body := tok[strings.LastIndex(tok, "_")+1:]
			if strings.HasPrefix(tok, "enc:") {
				body = tok[len("enc:v1:k1:"):]
			}
			for i := 0; i+8 <= len(body); i++ {
				if strings.Contains(got, body[i:i+8]) {
					t.Errorf("registered %q: %q of the token survives: %s", short, body[i:i+8], got)
					break
				}
			}
		}
		if strings.Count(got, redacted) < 2 || strings.Contains(got, "[[") {
			t.Errorf("registered %q: markers missing or mangled: %s", short, got)
		}
	}
}

// The scrubber redacts the union of what the secrets and the shapes match on the original text,
// so no case depends on which pass runs first. Overlapping or touching matches become one
// "[redacted]".
func TestScrubRedactsTheUnionOfSecretsAndShapes(t *testing.T) {
	for _, tc := range []struct {
		name    string
		secrets []string
		in      string
		want    string
		logLine bool
	}{
		// Shapes first left "abc-" readable.
		{"a secret that contains a token", []string{"abc-dash_pat_X1y2"}, "key abc-dash_pat_X1y2 end", "key [redacted] end", false},
		// Literals first broke the token apart at the secret's end; shapes first left "XYZ".
		{"a token that straddles a secret's end", []string{"XYZdash_s"}, "v=XYZdash_setup_TOKEN123 end", "v=[redacted] end", true},
		{"overlapping secrets", []string{"abcdef", "defghi"}, "x abcdefghi y", "x [redacted] y", false},
		{"a secret overlapping itself", []string{"aba"}, "x ababa y", "x [redacted] y", false},
		{"touching secrets", []string{"AAAA1111", "BBBB2222"}, "x AAAA1111BBBB2222 y", "x [redacted] y", false},
		{"a secret touching a token", []string{"AAAA1111"}, "x AAAA1111dash_pat_Q y", "x [redacted] y", false},
		{"apart, as before", []string{"AAAA1111"}, "AAAA1111 and dash_pat_Q", "[redacted] and [redacted]", false},
	} {
		reg := NewRegistry()
		for _, s := range tc.secrets {
			reg.Add(New(s, ""))
		}
		got := reg.Scrub(tc.in)
		if tc.logLine {
			got = reg.ScrubLogLine(tc.in)
		}
		if got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestRegistryFollowsRotation(t *testing.T) {
	v := New("first-secret-value", "file:/x")
	reg := NewRegistry()
	reg.Add(v)
	reg.Add(v) // duplicate add is harmless
	if got := reg.Scrub("first-secret-value"); got != redacted {
		t.Fatalf("before rotation: %q", got)
	}
	v.Update("second-secret-value")
	if got := reg.Scrub("second-secret-value"); got != redacted {
		t.Errorf("rotated value not scrubbed: %q", got)
	}
	if got := reg.Scrub("first-secret-value"); got != redacted {
		t.Errorf("an error built before the rotation must still be scrubbed: %q", got)
	}
	for i := range 3 * maxRetired {
		v.Update(fmt.Sprintf("rotation-number-%d", i))
	}
	if got := reg.Scrub("first-secret-value"); got != "first-secret-value" {
		t.Errorf("retired values are bounded: %q", got)
	}
}

func TestHandlerScrubsEverything(t *testing.T) {
	reg := NewRegistry()
	v := New(canary, "")
	reg.Add(v)
	enc := base64.StdEncoding.EncodeToString([]byte(canary))

	var buf bytes.Buffer
	base := slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	log := slog.New(NewHandler(base, reg))

	sentinel := errors.New("sentinel")
	wrapped := fmt.Errorf("dial https://x/?t=%s: %w", canary, sentinel)

	log.With("pre", "with "+canary).WithGroup("g").With("inner", enc).
		Error("failed "+canary,
			"direct", canary,
			"err", wrapped,
			"valuer", v,
			slog.Group("a", slog.String("s", canary), slog.Group("b", slog.String("deep", "x "+canary+" y"), slog.Any("e", errors.New(canary)))),
			"pat", "dash_pat_ID_SECRET",
			"num", 42,
		)

	out := buf.String()
	for _, leak := range []string{canary, "S3cret", enc, "dash_pat_ID_SECRET"} {
		if strings.Contains(out, leak) {
			t.Errorf("output leaked %q:\n%s", leak, out)
		}
	}
	var rec map[string]any
	if err := json.Unmarshal(buf.Bytes(), &rec); err != nil {
		t.Fatal(err)
	}
	if rec["msg"] != "failed [redacted]" {
		t.Errorf("msg = %v", rec["msg"])
	}
	g := rec["g"].(map[string]any)
	if g["direct"] != redacted || g["valuer"] != redacted || g["inner"] != redacted || g["num"].(float64) != 42 {
		t.Errorf("group attrs = %v", g)
	}
	if rec["pre"] != "with [redacted]" { // attached before WithGroup, so top level
		t.Errorf("pre = %v", rec["pre"])
	}
	deep := g["a"].(map[string]any)["b"].(map[string]any)
	if deep["deep"] != "x [redacted] y" || deep["e"] != redacted {
		t.Errorf("nested = %v", deep)
	}
	if g["err"] != "dial https://x/?t=[redacted]: sentinel" {
		t.Errorf("err = %v", g["err"])
	}
}

func TestHandlerKeepsErrorIdentity(t *testing.T) {
	reg := NewRegistry()
	reg.Add(New(canary, ""))
	h := &handler{reg: reg}
	sentinel := errors.New("sentinel " + canary)
	a := h.attr(slog.Any("err", fmt.Errorf("wrap: %w", sentinel)))
	err, ok := a.Value.Any().(error)
	if !ok || !errors.Is(err, sentinel) || strings.Contains(err.Error(), canary) {
		t.Errorf("scrubbed error = %v (ok=%v)", err, ok)
	}
	clean := errors.New("clean")
	if got := h.attr(slog.Any("err", clean)).Value.Any(); got != clean {
		t.Errorf("an error without secrets must pass through unchanged, got %v", got)
	}
}

func TestHandlerEnabledAndNilError(t *testing.T) {
	reg := NewRegistry()
	var buf bytes.Buffer
	h := NewHandler(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}), reg)
	if h.Enabled(context.Background(), slog.LevelInfo) || !h.Enabled(context.Background(), slog.LevelError) {
		t.Error("Enabled must follow the wrapped handler")
	}
	slog.New(h).Warn("m", "err", error(nil))
	if !strings.Contains(buf.String(), `"err":null`) {
		t.Errorf("nil error: %s", buf.String())
	}
}
