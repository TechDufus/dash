package ssh

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	sshmodel "github.com/TechDufus/dash/internal/plugins/ssh/model"
)

// The script is the constant with its three placeholders replaced by two integers and a hex
// nonce, and nothing else: buildScript has no parameter a setting could reach, and a nonce that is
// not hex never reaches the shell.
func TestScriptTakesNoConfiguredString(t *testing.T) {
	nonce := strings.Repeat("ab", nonceBytes)
	got := buildScript(7, 9, nonce)
	want := strings.NewReplacer("__NONCE__", nonce, "__INTERVAL__", "7", "__EVERY__", "9").Replace(collectorScript)
	if got != want {
		t.Fatal("the script differs from the constant with its placeholders replaced")
	}
	if strings.Contains(got, "__") {
		t.Error("a placeholder is left in the script")
	}
	for _, bad := range []string{"'; rm -rf / #", strings.Repeat("A", 2*nonceBytes), "$(id)" + strings.Repeat("a", 27), ""} {
		s := buildScript(5, 12, bad)
		if bad != "" && strings.Contains(s, bad) {
			t.Errorf("a nonce that is not hex reached the script: %q", bad)
		}
		if !strings.Contains(s, "M='@@"+strings.Repeat("0", 2*nonceBytes)+"'") {
			t.Errorf("nonce %q was not replaced by zeros", bad)
		}
	}
	if s := buildScript(-3, 0, nonce); !strings.Contains(s, "\nI=1\n") || !strings.Contains(s, "\nK=1\n") {
		t.Error("a non-positive integer reached the script")
	}
	for i := 1; i <= 60; i++ {
		if k := inventoryEvery(i); k*i < 60 || (k-1)*i >= 60 {
			t.Errorf("inventoryEvery(%d) = %d: not about a minute", i, k)
		}
	}
}

// runScript runs the collector under shell (a command line, its first word the program) for
// frames frames and parses them as a connection does: the frames and the sample of each.
func runScript(t *testing.T, shell []string, frames int) (*collector, []frame, []sshmodel.Stats) {
	t.Helper()
	nonce, err := newNonce()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, shell[0], shell[1:]...)
	cmd.Stdin = strings.NewReader(buildScript(1, 2, nonce))
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot start %v: %v", shell, err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	fr := newFrameReader(out, nonce)
	col := &collector{}
	var got []frame
	var stats []sshmodel.Stats
	for range frames {
		f, err := fr.next()
		if err != nil {
			t.Fatalf("frame %d: %v", len(got), err)
		}
		stats = append(stats, col.frame(f, time.Now()))
		got = append(got, f)
	}
	return col, got, stats
}

// checkLinuxFrames checks what any Linux host reports, and that the second sample has rates.
func checkLinuxFrames(t *testing.T, col *collector, frames []frame, stats []sshmodel.Stats) {
	t.Helper()
	if k := kernelOf(frames[0]); k != "Linux" {
		t.Fatalf("uname -s = %q", k)
	}
	if !frames[0].has("uname") || frames[1].has("uname") {
		t.Error("the inventory is not on the first frame only")
	}
	info := col.info()
	if info == nil || info.Kernel == "" || info.Arch == "" || info.CPUs <= 0 {
		t.Fatalf("inventory = %+v", info)
	}
	st := stats[1]
	if st.MemTotalBytes <= 0 || st.MemUsedBytes <= 0 || st.UptimeSeconds <= 0 || st.CPURatio <= 0 || st.CPURatio > 1 {
		t.Errorf("stats = %+v", st)
	}
}

// The real script, run by the local sh for two frames, gives an inventory and a sample.
func TestScriptRunsUnderTheLocalShell(t *testing.T) {
	if _, err := os.Stat("/proc/stat"); err != nil {
		t.Skip("no /proc on this machine")
	}
	col, frames, stats := runScript(t, []string{"sh"}, 2)
	checkLinuxFrames(t, col, frames, stats)
	if info := col.info(); len(info.Filesystems) == 0 {
		t.Errorf("no file system: %+v", info)
	}
}

// SSH_SCRIPT_SHELL runs the script under another shell and prints what dash reads, e.g.
// SSH_SCRIPT_SHELL="docker run --rm -i alpine:3 sh" for busybox ash or
// SSH_SCRIPT_SHELL="docker run --rm -i debian:stable-slim dash" for Debian's dash.
func TestScriptUnderAnotherShell(t *testing.T) {
	shell := strings.Fields(os.Getenv("SSH_SCRIPT_SHELL"))
	if len(shell) == 0 {
		t.Skip("SSH_SCRIPT_SHELL is not set")
	}
	col, frames, stats := runScript(t, shell, 2)
	checkLinuxFrames(t, col, frames, stats)
	b, _ := json.MarshalIndent(map[string]any{"info": col.info(), "stats": stats[1]}, "", "  ")
	t.Logf("%v read:\n%s", shell, b)
}
