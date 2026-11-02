package secret

import (
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

const goodRef = "k8s:dash/proxmox-token#secret"

func TestParseK8sRef(t *testing.T) {
	long := strings.Repeat("a", 64)
	tests := []struct {
		name, raw string
		want      K8sRef
		wantErr   string
	}{
		{name: "plain", raw: goodRef, want: K8sRef{"dash", "proxmox-token", "secret", goodRef}},
		{name: "dotted name and key", raw: "k8s:media/my.app.v2#tls.key_1-a", want: K8sRef{"media", "my.app.v2", "tls.key_1-a", "k8s:media/my.app.v2#tls.key_1-a"}},
		{name: "no key", raw: "k8s:dash/x", wantErr: "must look like"},
		{name: "no namespace", raw: "k8s:x#k", wantErr: "must look like"},
		{name: "empty namespace", raw: "k8s:/x#k", wantErr: "namespace"},
		{name: "upper case namespace", raw: "k8s:Dash/x#k", wantErr: "namespace"},
		{name: "dotted namespace", raw: "k8s:a.b/x#k", wantErr: "namespace"},
		{name: "namespace too long", raw: "k8s:" + long + "/x#k", wantErr: "namespace"},
		{name: "empty name", raw: "k8s:dash/#k", wantErr: "Secret name"},
		{name: "underscore in name", raw: "k8s:dash/a_b#k", wantErr: "Secret name"},
		{name: "slash in name", raw: "k8s:dash/a/b#k", wantErr: "Secret name"},
		{name: "empty key", raw: "k8s:dash/x#", wantErr: "key"},
		{name: "dot key", raw: "k8s:dash/x#.", wantErr: "key"},
		{name: "dotdot key", raw: "k8s:dash/x#..", wantErr: "key"},
		{name: "key with a space", raw: "k8s:dash/x#a b", wantErr: "key"},
		{name: "second hash", raw: "k8s:dash/x#a#b", wantErr: "key"},
		{name: "other scheme", raw: "env:X", wantErr: "not a k8s: reference"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseK8sRef(tt.raw)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %v, want one containing %q", err, tt.wantErr)
				}
				if strings.Contains(err.Error(), tt.raw[len("k8s:"):]) && len(tt.raw) > len("k8s:") {
					t.Errorf("the error repeats the reference: %v", err)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Fatalf("got %+v, %v; want %+v", got, err, tt.want)
			}
		})
	}
}

// The same reference gives the same holder for the life of the process, so a reload sees no
// change where only the reading progressed (R4-8).
func TestK8sRefsStableHolder(t *testing.T) {
	k := NewK8sRefs(nil)
	a, err := k.Handle(goodRef)
	if err != nil {
		t.Fatal(err)
	}
	if a.IsSet() || !a.Pending() || !a.Present() || a.Ref() != goodRef {
		t.Fatalf("a new handle is pending: set=%v pending=%v present=%v ref=%q", a.IsSet(), a.Pending(), a.Present(), a.Ref())
	}
	if a.Reason() != ReasonKubernetesUnavailable {
		t.Errorf("first reason = %q", a.Reason())
	}
	b, _ := k.Handle(goodRef)
	if a.h != b.h {
		t.Fatal("the same reference must give the same holder")
	}
	other, _ := k.Handle("k8s:dash/other#secret")
	if other.h == a.h {
		t.Fatal("different references share a holder")
	}
	k.Fill(goodRef, canary)
	if b.Reveal() != canary || a.Pending() || !a.IsSet() {
		t.Error("a fill reaches every copy")
	}
	if c, _ := k.Handle(goodRef); c.h != a.h || c.Reveal() != canary {
		t.Error("the handle is still the same one after the fill")
	}
	if refs := k.Refs(); len(refs) != 2 || refs[0].Raw != goodRef || refs[1].Name != "other" {
		t.Errorf("Refs() = %+v", refs)
	}
}

// NewK8sRefs(reg) puts every handle in the scrub registry when it is made, so a value read
// later is scrubbed from that moment, also for a reference only a reload introduced (S5).
func TestRefAddedByReloadIsScrubbed(t *testing.T) {
	reg := NewRegistry()
	k := NewK8sRefs(reg)
	if _, err := k.Handle(goodRef); err != nil {
		t.Fatal(err)
	}
	k.Fill(goodRef, canary)
	if got := reg.Scrub("upstream said " + canary); strings.Contains(got, canary) {
		t.Errorf("not scrubbed: %q", got)
	}
	k.Fill(goodRef, canary+"-rotated")
	if got := reg.Scrub("old " + canary + " new " + canary + "-rotated"); strings.Contains(got, canary) {
		t.Errorf("a rotated value must still scrub the old one: %q", got)
	}
}

func TestK8sRefsCallbacks(t *testing.T) {
	k := NewK8sRefs(nil)
	var fills, changes atomic.Int32
	k.OnFill(func() { fills.Add(1) })
	k.OnChange(func() { changes.Add(1) })
	expect := func(step string, fill, change int32) {
		t.Helper()
		if fills.Load() != fill || changes.Load() != change {
			t.Errorf("%s: fills=%d changes=%d, want %d and %d", step, fills.Load(), changes.Load(), fill, change)
		}
	}

	if _, err := k.Handle(goodRef); err != nil {
		t.Fatal(err)
	}
	expect("a reference seen for the first time is a change", 0, 1)
	k.Handle(goodRef)
	expect("the same reference again is nothing", 0, 1)

	k.SetReason(goodRef, ReasonNotFound)
	expect("a new reason is a change", 0, 2)
	k.SetReason(goodRef, ReasonNotFound)
	expect("the same reason is nothing", 0, 2)
	if k.Reason(goodRef) != ReasonNotFound {
		t.Errorf("Reason = %q", k.Reason(goodRef))
	}

	k.Fill(goodRef, canary)
	expect("the first fill", 1, 3)
	k.Fill(goodRef, canary+"2")
	expect("a rotation needs no reload: consumers read per request", 1, 3)

	k.SetReason(goodRef, ReasonKeyMissing)
	expect("the key vanishing is a change", 1, 4)
	k.Fill(goodRef, canary+"3")
	expect("the key back ends the reason: a change, not a first fill", 1, 5)

	k.Fill(goodRef, "")
	k.Fill("k8s:dash/unknown#k", "x")
	k.SetReason("k8s:dash/unknown#k", ReasonForbidden)
	expect("an empty value and an unknown reference are ignored", 1, 5)
	if v, _ := k.Handle(goodRef); v.Reveal() != canary+"3" {
		t.Error("an empty Fill must not clear the value")
	}
}

func TestK8sRefsSubscribeReplaysAndDelivers(t *testing.T) {
	k := NewK8sRefs(nil)
	k.Handle("k8s:a/one#k")
	var mu sync.Mutex
	var got []string
	cancel := k.Subscribe(func(r K8sRef) {
		mu.Lock()
		defer mu.Unlock()
		got = append(got, r.Namespace+"/"+r.Name+"#"+r.Key)
	})
	k.Handle("k8s:b/two#k")
	cancel()
	cancel() // twice is fine
	k.Handle("k8s:c/three#k")
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(got, ",") != "a/one#k,b/two#k" {
		t.Errorf("subscriber saw %v: existing ones replayed, new ones delivered, none after cancel", got)
	}
}

// Handles are made and filled from several goroutines: the reloader, the watchers and the API.
func TestK8sRefsConcurrentUse(t *testing.T) {
	reg := NewRegistry()
	k := NewK8sRefs(reg)
	k.OnChange(func() {})
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 50 {
				v, err := k.Handle(goodRef)
				if err != nil {
					t.Error(err)
					return
				}
				k.Fill(goodRef, canary)
				k.SetReason(goodRef, ReasonUnreachable)
				_ = v.Reveal()
				_ = reg.Scrub("x")
				_ = k.Refs()
			}
		}()
	}
	wg.Wait()
	if v, _ := k.Handle(goodRef); v.Reveal() != canary {
		t.Error("value lost")
	}
}
