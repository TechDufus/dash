package main

import (
	"bytes"
	"encoding/base64"
	"log/slog"
	"strings"
	"testing"

	"github.com/TechDufus/dash/internal/audit"
	"github.com/TechDufus/dash/internal/config"
	"github.com/TechDufus/dash/internal/secret"
	"github.com/TechDufus/dash/internal/settings"
)

const tokenSecretPath = "plugins.proxmox.tokenSecret"

func testKey(n byte) string { return base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{n}, 32)) }

// storeWithSecret saves a secret into the data directory the way the settings API will, sealed
// with the key.
func storeWithSecret(t *testing.T, dir, key, value string) {
	t.Helper()
	keys, err := config.SecretKeys([]string{"DASH_SECRET_KEY=" + key}, nil)
	if err != nil {
		t.Fatal(err)
	}
	st, err := settings.Open(dir, settings.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	alog, err := audit.Open(dir, audit.Options{Logger: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatal(err)
	}
	defer alog.Close()
	if err := (config.SecretWriter{Store: st, Keys: keys, Audit: alog}).Replace(tokenSecretPath, value, audit.Actor{Type: "proxy", User: "alice"}); err != nil {
		t.Fatal(err)
	}
}

// Startup with the key opens a stored secret; with the wrong key, or none, it exits listing the
// field and releases the data directory (P1.5 acceptance).
func TestStartupDecryptsStoredSecretsOrFailsListingThem(t *testing.T) {
	dir := t.TempDir()
	storeWithSecret(t, dir, testKey(1), "stored-secret-0123456789")
	startEnv(t, dir, "DASH_PLUGINS_PROXMOX_URL=https://pve.example.com:8006", "DASH_PLUGINS_PROXMOX_TOKEN_ID=u@pve!t")

	t.Setenv("DASH_SECRET_KEY", testKey(1))
	res, store, err := loadConfig(secret.NewRegistry())
	if err != nil {
		t.Fatal(err)
	}
	store.Close()
	if res.Config.ProxmoxTokenSecret.Reveal() != "stored-secret-0123456789" {
		t.Error("the stored secret must be decrypted at startup")
	}

	for name, key := range map[string]string{"the wrong key": testKey(2), "no key": ""} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("DASH_SECRET_KEY", key)
			_, store, err := loadConfig(secret.NewRegistry())
			if err == nil || !strings.Contains(err.Error(), tokenSecretPath) {
				t.Fatalf("want an error listing %s, got %v", tokenSecretPath, err)
			}
			if store != nil {
				t.Error("a failed start returns no store")
			}
		})
	}
	// The failed starts released the lock.
	t.Setenv("DASH_SECRET_KEY", testKey(1))
	if _, store, err = loadConfig(secret.NewRegistry()); err != nil {
		t.Fatalf("the data directory must be free after a failed start: %v", err)
	}
	store.Close()
}

func TestStartupRefusesBothFormsOfTheKey(t *testing.T) {
	startEnv(t, t.TempDir(), "DASH_SECRET_KEY="+testKey(1), "DASH_SECRET_KEY_FILE=/run/secrets/key")
	if _, _, err := loadConfig(secret.NewRegistry()); err == nil || !strings.Contains(err.Error(), "DASH_SECRET_KEY and DASH_SECRET_KEY_FILE are both set") {
		t.Errorf("got %v", err)
	}
}
