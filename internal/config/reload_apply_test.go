package config

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

const (
	gfURL  = "https://grafana.ui.example.com"
	gfPath = "plugins.grafana.url"
)

func TestReloadNowAppliesWithoutWaitingForTheTick(t *testing.T) {
	g := newStoreRig(t, headlampConfig("https://hl-a.example.com"), nil)
	// Run is never started here: whatever happens, Reload did it.
	if err := g.store.Set(gfPath, gfURL); err != nil {
		t.Fatal(err)
	}
	if err := g.rl.Reload(); err != nil {
		t.Fatal(err)
	}
	if len(g.changes) != 1 || g.changes[0].Section != "plugins.grafana" {
		t.Fatalf("the subscribers must hear of the change before Reload returns: %+v", g.changes)
	}
	if got := g.rl.Tree().Plugins.Grafana.URL; got != gfURL {
		t.Errorf("Tree after Reload: %q", got)
	}

	// Nothing changed since: a second Reload announces nothing.
	if err := g.rl.Reload(); err != nil || len(g.changes) != 1 {
		t.Errorf("an idempotent Reload: err %v, changes %+v", err, g.changes)
	}

	// It fails when the resolution does, and the running configuration stays in force. The
	// error is the caller's every time, though it is logged once.
	writeFile(t, g.cfgPath, "plugins:\n  headlamp:\n    url: [not, a, string]\n")
	for range 2 {
		if err := g.rl.Reload(); err == nil || !strings.Contains(err.Error(), "plugins.headlamp.url") {
			t.Errorf("Reload of an invalid file = %v", err)
		}
	}
	if n := strings.Count(g.log.String(), "level=ERROR"); n != 1 {
		t.Errorf("want one ERROR, got %d:\n%s", n, g.log)
	}
	if got := g.rl.Tree().Plugins.Headlamp.URL; got != "https://hl-a.example.com" {
		t.Errorf("a failed Reload changed the running config: %q", got)
	}
}

func TestResolvedCarriesProvenance(t *testing.T) {
	g := newStoreRig(t, headlampConfig("https://hl-a.example.com"), nil)
	if g.rl.Resolved() != g.res {
		t.Fatal("before any reload Resolved is the startup resolution")
	}
	if p := g.rl.Resolved().Provenance["plugins.headlamp.url"]; p != (Provenance{Source: SourceFile, Ref: g.cfgPath}) {
		t.Errorf("startup provenance = %+v", p)
	}

	if err := g.store.Set(gfPath, gfURL); err != nil {
		t.Fatal(err)
	}
	if err := g.rl.Reload(); err != nil {
		t.Fatal(err)
	}
	res := g.rl.Resolved()
	if p := res.Provenance[gfPath]; p != (Provenance{Source: SourceUI, Ref: g.store.Path()}) {
		t.Errorf("provenance of a saved value = %+v", p)
	}
	if res.Tree.Plugins.Grafana.URL != gfURL || g.rl.Tree().Plugins.Grafana.URL != gfURL {
		t.Errorf("Resolved and Tree disagree: %q, %q", res.Tree.Plugins.Grafana.URL, g.rl.Tree().Plugins.Grafana.URL)
	}

	// A tick swaps them together too.
	writeFile(t, g.cfgPath, headlampConfig("https://hl-b.example.com")+"  grafana:\n    url: https://grafana.file.example.com\n")
	g.rl.poll()
	res = g.rl.Resolved()
	if p := res.Provenance[gfPath]; p != (Provenance{Source: SourceFile, Ref: g.cfgPath}) {
		t.Errorf("the file now wins: provenance = %+v", p)
	}
	if res.Tree.Plugins.Headlamp.URL != "https://hl-b.example.com" || g.rl.Tree().Plugins.Headlamp.URL != res.Tree.Plugins.Headlamp.URL {
		t.Errorf("Resolved and Tree disagree after a tick: %q, %q", res.Tree.Plugins.Headlamp.URL, g.rl.Tree().Plugins.Headlamp.URL)
	}

	// An invalid edit keeps both.
	writeFile(t, g.cfgPath, "plugins:\n  headlamp:\n    urll: x\n")
	if err := g.rl.Reload(); err == nil {
		t.Fatal("an invalid file reloaded")
	}
	if g.rl.Resolved() != res {
		t.Error("a failed reload replaced Resolved")
	}
}

func TestReloaderLockIsHeldAcrossCandidateApplyReload(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		g := newStoreRig(t, headlampConfig("https://hl-a.example.com"), nil)
		ticked := make(chan struct{})
		// assertWaiting proves the tick has not run: synctest.Wait returns once the tick
		// goroutine is blocked on the reloader lock, or has finished.
		assertWaiting := func(step string) {
			synctest.Wait()
			select {
			case <-ticked:
				t.Errorf("a tick ran inside WithLock, %s", step)
			default:
			}
		}

		err := g.rl.WithLock(func() error {
			go func() {
				g.rl.poll()
				close(ticked)
			}()
			assertWaiting("before the candidate")

			ui := g.store.Values()
			ui["plugins"] = map[string]any{"grafana": map[string]any{"url": gfURL}}
			if _, err := g.rl.Candidate(ui); err != nil {
				return err
			}
			if _, err := g.store.Apply(map[string]any{gfPath: gfURL}, nil, nil); err != nil {
				return err
			}
			assertWaiting("between Apply and Reload")

			if err := g.rl.Reload(); err != nil {
				return err
			}
			if len(g.changes) != 1 || g.changes[0].Section != "plugins.grafana" {
				t.Errorf("Reload, not the tick, announces the write: %+v", g.changes)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}

		<-ticked // released by WithLock, the tick finds nothing left to do
		if len(g.changes) != 1 {
			t.Errorf("the waiting tick announced the write again: %+v", g.changes)
		}
	})
}

// A plugin URL that keeps its value but moves between the config file and the UI store changes
// its provenance, which decides whether the URL may reach loopback, so the section changes
// although no value does. RestartRequired follows values only.
func TestReloadAnnouncesAPluginURLThatMovesIntoOrOutOfTheUI(t *testing.T) {
	withFile := headlampConfig("https://hl-a.example.com") + "  grafana:\n    url: " + gfURL + "\n"
	g := newStoreRig(t, withFile, map[string]any{gfPath: gfURL})
	fromUI := func(res *Resolved) bool { return res.Provenance[gfPath].Source == SourceUI }
	if fromUI(g.rl.Resolved()) {
		t.Fatal("at startup the file's URL wins")
	}

	for _, step := range []struct {
		name   string
		cfg    string
		fromUI bool
	}{
		{"removed from the file", headlampConfig("https://hl-a.example.com"), true},
		{"back in the file", withFile, false},
	} {
		g.changes = nil
		writeFile(t, g.cfgPath, step.cfg)
		if err := g.rl.Reload(); err != nil {
			t.Fatal(err)
		}
		res := g.rl.Resolved()
		if res.Tree.Plugins.Grafana.URL != gfURL || fromUI(res) != step.fromUI {
			t.Fatalf("%s: url %q, from the UI %v", step.name, res.Tree.Plugins.Grafana.URL, fromUI(res))
		}
		if len(g.changes) != 1 || g.changes[0].Section != "plugins.grafana" || len(g.changes[0].Paths) != 1 || g.changes[0].Paths[0] != gfPath {
			t.Errorf("%s: changes %+v, want plugins.grafana [%s]", step.name, g.changes, gfPath)
		}
		if rr := g.rl.RestartRequired(); len(rr) != 0 {
			t.Errorf("%s: RestartRequired %v, want none: no value changed", step.name, rr)
		}
	}
}

// Reload outside WithLock, Candidate and ticks run at once: the subscribers still run one at a
// time. The rig's subscriber appends to a plain slice, which -race reports when a Reload and a
// tick notify together. Run it with -race.
func TestReloadOutsideWithLockNotifiesInTurnWithTicks(t *testing.T) {
	g := newStoreRig(t, headlampConfig("https://hl-a.example.com")+"  proxmox:\n    url: https://pve.example.com:8006\n    tokenID: u@pve!t\n    tokenSecret: "+refA+"\n", map[string]any{gfPath: "https://gf.example.com"})
	g.rl.interval = time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); g.rl.Run(ctx) }()

	var wg sync.WaitGroup
	for i := range 6 {
		wg.Go(func() {
			for j := range 50 {
				ui := g.store.Values()
				ui["plugins"].(map[string]any)["headlamp"] = map[string]any{"cluster": fmt.Sprintf("c%d-%d", i, j)}
				if _, err := g.rl.Candidate(ui); err != nil {
					t.Error(err)
				}
			}
		})
	}
	wg.Go(func() {
		for j := range 50 {
			if err := g.store.Set(gfPath, fmt.Sprintf("https://gf-%d.example.com", j)); err != nil {
				t.Error(err)
			}
			if err := g.rl.Reload(); err != nil {
				t.Error(err)
			}
		}
	})
	wg.Wait()
	cancel()
	<-done
	if len(g.changes) == 0 {
		t.Error("no change was announced")
	}
}
