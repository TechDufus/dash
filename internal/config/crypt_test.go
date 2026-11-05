package config

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/TechDufus/dash/internal/audit"
	"github.com/TechDufus/dash/internal/secret"
	"github.com/TechDufus/dash/internal/settings"
)

const tokenPath = "plugins.proxmox.tokenSecret"

// keyText is a deterministic 32-byte key in the form `openssl rand -base64 32` prints.
func keyText(n byte) string { return base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{n}, 32)) }

func keyring(t *testing.T, current byte, previous ...byte) *secret.Keyring {
	t.Helper()
	env := []string{"DASH_SECRET_KEY=" + keyText(current)}
	if len(previous) > 0 {
		env = append(env, "DASH_SECRET_KEY_PREVIOUS="+keyText(previous[0]))
	}
	k, err := SecretKeys(env, nil)
	if err != nil || k == nil {
		t.Fatalf("keyring: %v", err)
	}
	return k
}

// secretRig is a writable settings store with an audit log in the same directory.
type secretRig struct {
	dir   string
	store *settings.Store
	alog  *audit.Log
}

func newSecretRig(t *testing.T) *secretRig {
	t.Helper()
	dir := t.TempDir()
	st, err := settings.Open(dir, settings.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	alog, err := audit.Open(dir, audit.Options{Logger: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { alog.Close() })
	return &secretRig{dir: dir, store: st, alog: alog}
}

// seal stores plaintext at the token path as the settings API does: sealed with keys, the path
// as the associated data, old copies scrubbed from the backups.
func (g *secretRig) seal(t *testing.T, keys *secret.Keyring, plaintext string) {
	t.Helper()
	sealed, err := keys.Encrypt(tokenPath, plaintext)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := g.store.Apply(map[string]any{tokenPath: sealed}, nil, []string{tokenPath}); err != nil {
		t.Fatal(err)
	}
}

// stored is the raw value the store holds at the token path.
func (g *secretRig) stored(t *testing.T) string {
	t.Helper()
	v, _ := g.store.Get(tokenPath)
	s, _ := v.(string)
	return s
}

// backups is the text of every retained ui-settings.yaml.bak.N.
func (g *secretRig) backups(t *testing.T) string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(g.dir, "ui-settings.yaml.bak.*"))
	if err != nil {
		t.Fatal(err)
	}
	var all strings.Builder
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		all.Write(b)
	}
	return all.String()
}

// records are the audit records of the given actions, oldest first; none means all.
func (g *secretRig) records(t *testing.T, actions ...string) []audit.Record {
	t.Helper()
	f, err := os.Open(filepath.Join(g.dir, audit.FileName))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []audit.Record
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var r audit.Record
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			t.Fatal(err)
		}
		if len(actions) == 0 || slices.Contains(actions, r.Action) {
			out = append(out, r)
		}
	}
	return out
}

func (g *secretRig) resolve(t *testing.T, keys *secret.Keyring) (*Resolved, string, error) {
	t.Helper()
	return resolveWith(t, Sources{Environ: pveBase, UI: g.store.Values(), UIRef: g.store.Path(), Keys: keys, Registry: secret.NewRegistry()})
}

// A secret typed into the UI is sealed in the store, never stored in clear, and resolves back
// to the typed value with the store as its source (P1.5: round trip).
func TestUISecretRoundTripsThroughResolve(t *testing.T) {
	g := newSecretRig(t)
	keys := keyring(t, 1)
	g.seal(t, keys, canary)
	if got := g.stored(t); !secret.IsEncrypted(got) || strings.Contains(got, canary) {
		t.Fatalf("the store must hold a sealed value, got %q", got)
	}

	res, log, err := g.resolve(t, keys)
	if err != nil {
		t.Fatal(err)
	}
	v := res.Tree.Plugins.Proxmox.TokenSecret
	if v.Reveal() != canary || v.Ref() != "" {
		t.Errorf("secret ref %q, value ok: %v", v.Ref(), v.Reveal() == canary)
	}
	if p := res.Provenance[tokenPath]; p.Source != SourceUI || p.Ref != g.store.Path() {
		t.Errorf("provenance = %+v", p)
	}
	if strings.Contains(log, canary) || strings.Contains(log, "WARN") {
		t.Errorf("unexpected log:\n%s", log)
	}

	// Env still wins over the stored secret, as for every field.
	res, _, err = resolveWith(t, Sources{
		Environ: append([]string{"DASH_PLUGINS_PROXMOX_TOKEN_SECRET=from-env-0123456789"}, pveBase...),
		UI:      g.store.Values(), Keys: keys,
	})
	if err != nil || res.Tree.Plugins.Proxmox.TokenSecret.Reveal() != "from-env-0123456789" {
		t.Errorf("env over a stored secret: %v", err)
	}
}

// A ciphertext copied to another field does not decrypt: the path is bound as the associated
// data (P1.5). Startup fails naming the field and the stored value stays as it is.
func TestCiphertextMovedToAnotherFieldFailsStartup(t *testing.T) {
	g := newSecretRig(t)
	keys := keyring(t, 1)
	moved, err := keys.Encrypt("plugins.grafana.url", canary)
	if err != nil {
		t.Fatal(err)
	}
	if err := g.store.Set(tokenPath, moved); err != nil {
		t.Fatal(err)
	}
	_, _, err = g.resolve(t, keys)
	if err == nil || !strings.Contains(err.Error(), tokenPath) || !errors.Is(err, secret.ErrDecrypt) {
		t.Fatalf("want a decryption error naming %s, got %v", tokenPath, err)
	}
	if g.stored(t) != moved {
		t.Error("a value that does not decrypt is never dropped or rewritten")
	}
}

// Missing key, wrong key or a mangled value all fail startup naming every affected field; none
// of them touches the stored value (P1.5).
func TestStoredSecretWithMissingOrWrongKeyFailsStartup(t *testing.T) {
	g := newSecretRig(t)
	g.seal(t, keyring(t, 1), canary)
	before := g.stored(t)
	for name, tc := range map[string]struct {
		keys *secret.Keyring
		want error
	}{
		"no key":          {nil, secret.ErrNoKey},
		"a different key": {keyring(t, 2), secret.ErrUnknownKey},
	} {
		t.Run(name, func(t *testing.T) {
			_, _, err := g.resolve(t, tc.keys)
			if err == nil || !strings.Contains(err.Error(), tokenPath) || !errors.Is(err, tc.want) {
				t.Fatalf("want %v naming %s, got %v", tc.want, tokenPath, err)
			}
			if strings.Contains(err.Error(), canary) || strings.Contains(err.Error(), keyText(1)) {
				t.Errorf("error leaks a secret: %v", err)
			}
			if g.stored(t) != before {
				t.Error("the stored value changed")
			}
		})
	}
	if err := g.store.Set(tokenPath, "enc:v1:nonsense"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := g.resolve(t, keyring(t, 1)); err == nil || !strings.Contains(err.Error(), tokenPath) {
		t.Errorf("a malformed value must fail startup too: %v", err)
	}
	if g.stored(t) != "enc:v1:nonsense" {
		t.Error("a malformed value is kept, not blanked")
	}
	// The same value below an env override still fails: nothing stored is silently ignored.
	_, _, err := resolveWith(t, Sources{
		Environ: append([]string{"DASH_PLUGINS_PROXMOX_TOKEN_SECRET=from-env-0123456789"}, pveBase...),
		UI:      g.store.Values(),
	})
	if err == nil || !strings.Contains(err.Error(), tokenPath) {
		t.Errorf("an overridden but undecryptable value must still fail startup: %v", err)
	}
}

// Without a key nothing can be sealed and the error names the key; dash itself runs, and env
// and file references work, without a key (P1.5).
func TestNoKeyRefusesToSealButFileRefsResolve(t *testing.T) {
	var none *secret.Keyring
	if _, err := none.Encrypt(tokenPath, canary); !errors.Is(err, secret.ErrNoKey) || !strings.Contains(err.Error(), "DASH_SECRET_KEY") {
		t.Fatalf("want an error naming DASH_SECRET_KEY, got %v", err)
	}
	file := writeSecretFile(t, canary)
	res, _, err := resolveWith(t, Sources{Environ: append([]string{"DASH_PLUGINS_PROXMOX_TOKEN_SECRET_FILE=" + file}, pveBase...)})
	if err != nil || res.Tree.Plugins.Proxmox.TokenSecret.Reveal() != canary {
		t.Errorf("a keyless dash must still resolve file refs: %v", err)
	}
}

// Rotation: values sealed with the previous key are re-encrypted with the current one on
// startup, written through the scrubbing path and audited as secrets.rekey (P1.5).
func TestRekeyReencryptsEveryValueAndAuditsIt(t *testing.T) {
	g := newSecretRig(t)
	g.seal(t, keyring(t, 1), canary)
	old := g.stored(t)
	if err := g.store.Set("plugins.grafana.url", "https://a.example.com"); err != nil { // the old ciphertext lands in a backup
		t.Fatal(err)
	}
	if !strings.Contains(g.backups(t), old) {
		t.Fatal("test setup: the old ciphertext should be in a backup")
	}

	rotated := keyring(t, 2, 1)
	// With both keys the old value still resolves, before and after the rekey.
	if res, _, err := g.resolve(t, rotated); err != nil || res.Tree.Plugins.Proxmox.TokenSecret.Reveal() != canary {
		t.Fatalf("resolve during rotation: %v", err)
	}
	RekeyStore(g.store, rotated, g.alog, slog.New(slog.DiscardHandler))

	now := g.stored(t)
	if now == old || rotated.NeedsRekey(now) || !secret.IsEncrypted(now) {
		t.Errorf("the value was not re-encrypted with the current key: %q", now)
	}
	if strings.Contains(g.backups(t), old) {
		t.Error("the old-key ciphertext survives in a backup")
	}
	if res, _, err := g.resolve(t, keyring(t, 2)); err != nil || res.Tree.Plugins.Proxmox.TokenSecret.Reveal() != canary {
		t.Errorf("after the rotation the current key alone must open it: %v", err)
	}

	recs := g.records(t, ActionSecretRekey)
	if len(recs) != 2 || recs[0].Kind != audit.KindIntent || recs[1].Kind != audit.KindOutcome ||
		recs[0].Action != ActionSecretRekey || recs[1].Action != ActionSecretRekey ||
		recs[1].Result != audit.ResultOK || recs[0].ID != recs[1].ID ||
		len(recs[0].Fields) != 1 || recs[0].Fields[0] != tokenPath {
		t.Errorf("want secrets.rekey intent and outcome naming the field, got %+v", recs)
	}

	// Nothing left to do: a second start writes and audits nothing.
	rev := g.store.Revision()
	RekeyStore(g.store, rotated, g.alog, slog.New(slog.DiscardHandler))
	if g.store.Revision() != rev || len(g.records(t, ActionSecretRekey)) != 2 {
		t.Error("a rekey with nothing to rewrite must be silent")
	}
}

// A read-only store cannot be rewritten: one WARN says the rekey is pending, the values stay
// as they are and still decrypt (P1.5).
func TestRekeyOnAReadOnlyStoreWarnsOnce(t *testing.T) {
	g := newSecretRig(t)
	g.seal(t, keyring(t, 1), canary)
	old := g.stored(t)
	g.store.Close()
	ro, err := settings.Open(g.dir, settings.Options{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()

	var buf bytes.Buffer
	rotated := keyring(t, 2, 1)
	RekeyStore(ro, rotated, g.alog, slog.New(slog.NewTextHandler(&buf, nil)))
	if n := strings.Count(buf.String(), "level=WARN"); n != 1 || !strings.Contains(buf.String(), "pending") {
		t.Errorf("want exactly one WARN saying the rekey is pending:\n%s", buf.String())
	}
	if v, _ := ro.Get(tokenPath); v != old {
		t.Error("a read-only store is not rewritten")
	}
	if n := len(g.records(t, ActionSecretRekey)); n != 0 {
		t.Errorf("%d audit records for a rekey that did not run", n)
	}
}

// If the intent cannot be recorded the rekey is skipped with an ERROR; the values still
// decrypt with the previous key (P1.5, P1.8 contract).
func TestRekeySkippedWhenTheIntentCannotBeRecorded(t *testing.T) {
	g := newSecretRig(t)
	g.seal(t, keyring(t, 1), canary)
	old := g.stored(t)
	g.alog.Close()

	var buf bytes.Buffer
	rotated := keyring(t, 2, 1)
	RekeyStore(g.store, rotated, g.alog, slog.New(slog.NewTextHandler(&buf, nil)))
	if !strings.Contains(buf.String(), "level=ERROR") {
		t.Errorf("want an ERROR:\n%s", buf.String())
	}
	if g.stored(t) != old {
		t.Error("nothing may be rewritten without a recorded intent")
	}
	if res, _, err := g.resolve(t, rotated); err != nil || res.Tree.Plugins.Proxmox.TokenSecret.Reveal() != canary {
		t.Errorf("the value must still decrypt: %v", err)
	}
}

func TestSecretKeysFromTheEnvironment(t *testing.T) {
	file := writeSecretFile(t, keyText(3)+"\n")
	reg := secret.NewRegistry()
	for name, tc := range map[string]struct {
		env     []string
		wantNil bool
		wantErr string
	}{
		"none":                 {nil, true, ""},
		"key":                  {[]string{"DASH_SECRET_KEY=" + keyText(1)}, false, ""},
		"key file":             {[]string{"DASH_SECRET_KEY_FILE=" + file}, false, ""},
		"both previous forms":  {[]string{"DASH_SECRET_KEY=" + keyText(1), "DASH_SECRET_KEY_PREVIOUS=" + keyText(2), "DASH_SECRET_KEY_PREVIOUS_FILE=" + file}, true, "DASH_SECRET_KEY_PREVIOUS and DASH_SECRET_KEY_PREVIOUS_FILE are both set"},
		"both current forms":   {[]string{"DASH_SECRET_KEY=" + keyText(1), "DASH_SECRET_KEY_FILE=" + file}, true, "DASH_SECRET_KEY and DASH_SECRET_KEY_FILE are both set"},
		"previous without key": {[]string{"DASH_SECRET_KEY_PREVIOUS=" + keyText(2)}, true, "DASH_SECRET_KEY_PREVIOUS is set but DASH_SECRET_KEY is not"},
		"previous file":        {[]string{"DASH_SECRET_KEY=" + keyText(1), "DASH_SECRET_KEY_PREVIOUS_FILE=" + file}, false, ""},
		"missing file":         {[]string{"DASH_SECRET_KEY_FILE=/missing/key"}, true, "DASH_SECRET_KEY_FILE"},
		"not a 32 byte key":    {[]string{"DASH_SECRET_KEY=" + base64.StdEncoding.EncodeToString([]byte("too short a key"))}, true, "DASH_SECRET_KEY"},
		"not base64 at all":    {[]string{"DASH_SECRET_KEY=%%%not-base64%%%"}, true, "DASH_SECRET_KEY"},
	} {
		t.Run(name, func(t *testing.T) {
			k, err := SecretKeys(tc.env, reg)
			switch {
			case tc.wantErr != "":
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error = %v, want %q", err, tc.wantErr)
				}
				for _, e := range tc.env {
					if _, v, _ := strings.Cut(e, "="); len(v) > 12 && strings.Contains(err.Error(), v) {
						t.Errorf("the error echoes key material: %v", err)
					}
				}
			case err != nil:
				t.Fatal(err)
			case (k == nil) != tc.wantNil:
				t.Fatalf("keyring nil = %v, want %v", k == nil, tc.wantNil)
			}
		})
	}
	if got := reg.Scrub("key " + keyText(1) + " and " + keyText(3)); strings.Contains(got, keyText(1)) || strings.Contains(got, keyText(3)) {
		t.Errorf("key material must be registered for scrubbing: %q", got)
	}
}

// The key variables are process settings, not tree fields: they never warn as unknown.
func TestSecretKeyVariablesAreNotUnknownSettings(t *testing.T) {
	file := writeSecretFile(t, keyText(3))
	_, log, err := resolveWith(t, Sources{Environ: []string{
		"DASH_DEMO=true", "DASH_SECRET_KEY=" + keyText(1), "DASH_SECRET_KEY_FILE=" + file,
		"DASH_SECRET_KEY_PREVIOUS=" + keyText(2), "DASH_SECRET_KEY_PREVIOUS_FILE=" + file,
	}})
	if err != nil || strings.Contains(log, "unknown") {
		t.Errorf("err %v, log:\n%s", err, log)
	}
}
