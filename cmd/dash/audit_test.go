package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/TechDufus/dash/internal/audit"
)

// writeLog records one completed action and one event into dir.
func writeLog(t *testing.T, dir string) {
	t.Helper()
	l, err := audit.Open(dir, audit.Options{})
	if err != nil {
		t.Fatal(err)
	}
	p, err := l.Intent(audit.Record{Action: "restart", Target: audit.Target{Kind: "Deployment", Namespace: "media", Name: "app-0"}, Actor: audit.Actor{User: "alice"}})
	if err != nil {
		t.Fatal(err)
	}
	p.Outcome(nil)
	l.Event(audit.Record{Action: "login"})
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestAuditVerifyCleanLog(t *testing.T) {
	dir := t.TempDir()
	writeLog(t, dir)
	var out bytes.Buffer
	if err := command([]string{"audit", "verify", "--dir", dir}, &out); err != nil {
		t.Fatalf("clean log failed: %v\n%s", err, &out)
	}
	if !strings.Contains(out.String(), "chain intact") || !strings.Contains(out.String(), "3 records") {
		t.Errorf("report:\n%s", &out)
	}
}

// A crash mid-write leaves an unterminated last line: reported, not a broken chain.
func TestAuditVerifyReportsTornTailAsIntact(t *testing.T) {
	dir := t.TempDir()
	writeLog(t, dir)
	f, err := os.OpenFile(filepath.Join(dir, audit.FileName), os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(`{"kind":"inte`)
	f.Close()
	var out bytes.Buffer
	if err := command([]string{"audit", "verify", "--dir", dir}, &out); err != nil {
		t.Fatalf("torn tail failed verification: %v\n%s", err, &out)
	}
	if !strings.Contains(out.String(), "chain intact") || !strings.Contains(out.String(), "torn tail: audit.jsonl:4") {
		t.Errorf("report:\n%s", &out)
	}
}

func TestAuditVerifyNamesTheEditedLine(t *testing.T) {
	dir := t.TempDir()
	writeLog(t, dir)
	path := filepath.Join(dir, audit.FileName)
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(b), "\n")
	lines[1] = strings.Replace(lines[1], `"result":"ok"`, `"result":"no"`, 1) // the outcome
	if lines[1] == strings.Split(string(b), "\n")[1] {
		t.Fatal("test did not edit the outcome line")
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o600); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	err = command([]string{"audit", "verify", "--dir", dir}, &out)
	if !errors.Is(err, errAuditFailed) {
		t.Fatalf("err = %v, want errAuditFailed\n%s", err, &out)
	}
	if !strings.Contains(out.String(), "BROKEN: audit.jsonl:2:") {
		t.Errorf("report does not name line 2:\n%s", &out)
	}
}

// An intent whose outcome never arrived is a crash mid-action; verify reports it and fails.
func TestAuditVerifyReportsDanglingIntent(t *testing.T) {
	dir := t.TempDir()
	l, err := audit.Open(dir, audit.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l.Intent(audit.Record{Action: "reconcile", Target: audit.Target{Kind: "Kustomization", Namespace: "flux-system", Name: "app"}}); err != nil {
		t.Fatal(err)
	}
	l.Close()

	var out bytes.Buffer
	if err := command([]string{"audit", "verify", "--dir", dir}, &out); !errors.Is(err, errAuditFailed) {
		t.Fatalf("err = %v, want errAuditFailed", err)
	}
	if !strings.Contains(out.String(), "chain intact") || !strings.Contains(out.String(), "dangling intent") ||
		!strings.Contains(out.String(), "reconcile Kustomization flux-system/app") {
		t.Errorf("report:\n%s", &out)
	}
}

func TestAuditVerifyDefaultsToTheDataDir(t *testing.T) {
	dir := t.TempDir()
	writeLog(t, dir)
	t.Setenv("DASH_DATA_DIR", dir)
	var out bytes.Buffer
	if err := command([]string{"audit", "verify"}, &out); err != nil {
		t.Fatalf("%v\n%s", err, &out)
	}
	if !strings.Contains(out.String(), dir) {
		t.Errorf("did not verify the configured data dir:\n%s", &out)
	}
}

func TestAuditVerifyRejectsStrayArguments(t *testing.T) {
	err := command([]string{"audit", "verify", "extra"}, &bytes.Buffer{})
	if err == nil || errors.Is(err, errAuditFailed) || !strings.Contains(err.Error(), "extra") {
		t.Errorf("err = %v", err)
	}
}
