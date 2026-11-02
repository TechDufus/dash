package plugintest

import (
	"context"
	"errors"
	"slices"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var (
	gvrA = schema.GroupVersionResource{Group: "a.example.com", Version: "v1", Resource: "as"}
	gvrB = schema.GroupVersionResource{Group: "b.example.com", Version: "v1", Resource: "bs"}
)

func kubeObj(name string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetName(name)
	return u
}

func TestKubeClusterStartsSyncedServedListedAndEmpty(t *testing.T) {
	k := NewKubeCluster()
	if !k.Synced() || !k.Has(gvrA) || !k.Listed(gvrA) {
		t.Errorf("Synced=%v Has=%v Listed=%v, want all true", k.Synced(), k.Has(gvrA), k.Listed(gvrA))
	}
	if got := k.List(gvrA); len(got) != 0 {
		t.Errorf("List = %v, want nothing", got)
	}
	if got := k.GVRRevision(); got != 0 {
		t.Errorf("GVRRevision = %d, want 0", got)
	}
	if _, ok := k.Refused(gvrA); ok {
		t.Error("Refused on a fresh cluster")
	}
	if k.FailedGroups() != nil && len(k.FailedGroups()) != 0 {
		t.Errorf("FailedGroups = %v", k.FailedGroups())
	}
}

func TestKubeClusterSetObjectsReplacesAndListReturnsACopy(t *testing.T) {
	k := NewKubeCluster()
	k.SetObjects(gvrA, kubeObj("one"), kubeObj("two"))
	k.SetObjects(gvrA, kubeObj("three"))
	got := k.List(gvrA)
	if len(got) != 1 || got[0].GetName() != "three" {
		t.Fatalf("List = %v, want only three", got)
	}
	got[0] = kubeObj("changed")
	if again := k.List(gvrA); again[0].GetName() != "three" {
		t.Errorf("editing the listed slice changed the cluster: %v", again)
	}
	if k.List(gvrB) != nil {
		t.Errorf("List of an untouched gvr = %v, want nil", k.List(gvrB))
	}
}

func TestKubeClusterRevisionSumsPerGVR(t *testing.T) {
	k := NewKubeCluster()
	k.SetObjects(gvrA, kubeObj("x"))
	k.SetObjects(gvrA, kubeObj("y"))
	k.SetObjects(gvrB, kubeObj("z"))
	if got := k.GVRRevision(gvrA); got != 2 {
		t.Errorf("revision of A = %d, want 2", got)
	}
	if got := k.GVRRevision(gvrB); got != 1 {
		t.Errorf("revision of B = %d, want 1", got)
	}
	if got := k.GVRRevision(gvrA, gvrB); got != 3 {
		t.Errorf("revision of A and B = %d, want 3", got)
	}
	if got := k.GVRRevision(); got != 3 {
		t.Errorf("revision of all = %d, want 3", got)
	}
	if got := k.GVRRevision(schema.GroupVersionResource{Resource: "never"}); got != 0 {
		t.Errorf("revision of an untouched gvr = %d, want 0", got)
	}
}

func TestKubeClusterSwitchesBumpTheirGVR(t *testing.T) {
	k := NewKubeCluster()
	for name, fn := range map[string]func(){
		"SetServed":    func() { k.SetServed(gvrA, false) },
		"SetListed":    func() { k.SetListed(gvrA, false) },
		"SetRefused":   func() { k.SetRefused(gvrA, "auth") },
		"ClearRefused": func() { k.ClearRefused(gvrA) },
	} {
		beforeA, beforeB := k.GVRRevision(gvrA), k.GVRRevision(gvrB)
		fn()
		if k.GVRRevision(gvrA) <= beforeA {
			t.Errorf("%s did not bump the revision of its gvr", name)
		}
		if k.GVRRevision(gvrB) != beforeB {
			t.Errorf("%s bumped the revision of another gvr", name)
		}
	}
}

func TestKubeClusterAbsentGVRListsNilAndCountsAsListed(t *testing.T) {
	k := NewKubeCluster()
	k.SetObjects(gvrA, kubeObj("x"))
	k.SetServed(gvrA, false)
	if k.Has(gvrA) {
		t.Error("Has of an unserved gvr")
	}
	if !k.Listed(gvrA) {
		t.Error("an unserved gvr must count as listed")
	}
	k.SetListed(gvrA, false)
	if !k.Listed(gvrA) {
		t.Error("an unserved gvr must count as listed even when SetListed(false)")
	}
	if k.List(gvrA) != nil {
		t.Errorf("List of an unserved gvr = %v, want nil", k.List(gvrA))
	}

	k.SetServed(gvrA, true)
	if !k.Has(gvrA) || k.Listed(gvrA) {
		t.Errorf("served again: Has=%v Listed=%v, want true,false (still not listed)", k.Has(gvrA), k.Listed(gvrA))
	}
	if k.List(gvrA) != nil {
		t.Errorf("List before the first list = %v, want nil", k.List(gvrA))
	}
	k.SetListed(gvrA, true)
	if got := k.List(gvrA); len(got) != 1 || got[0].GetName() != "x" {
		t.Errorf("List after listing = %v, want x", got)
	}
}

func TestKubeClusterWatchRecordsEveryCallAsACopy(t *testing.T) {
	k := NewKubeCluster()
	arg := []schema.GroupVersionResource{gvrA, gvrB}
	k.Watch(arg...)
	k.Watch(gvrA)
	arg[0] = schema.GroupVersionResource{}
	got := k.Watched()
	if len(got) != 2 || !slices.Equal(got[0], []schema.GroupVersionResource{gvrA, gvrB}) || !slices.Equal(got[1], []schema.GroupVersionResource{gvrA}) {
		t.Fatalf("Watched = %v", got)
	}
	got[0][0] = schema.GroupVersionResource{}
	if k.Watched()[0][0] != gvrA {
		t.Error("editing the returned record changed the cluster's")
	}
}

func TestKubeClusterPatchRecordsAndCanFail(t *testing.T) {
	k := NewKubeCluster()
	body := []byte(`{"spec":{"suspend":true}}`)
	if err := k.Patch(context.Background(), gvrA, "ns", "name", body); err != nil {
		t.Fatalf("Patch = %v", err)
	}
	body[0] = 'X'
	boom := errors.New("boom")
	k.SetPatchError(boom)
	if err := k.Patch(context.Background(), gvrB, "", "cluster", []byte("{}")); !errors.Is(err, boom) {
		t.Fatalf("Patch error = %v, want boom", err)
	}
	got := k.Patches()
	if len(got) != 2 {
		t.Fatalf("Patches = %v, want both calls, the failed one included", got)
	}
	if want := (KubePatch{GVR: gvrA, Namespace: "ns", Name: "name", Merge: []byte(`{"spec":{"suspend":true}}`)}); got[0].GVR != want.GVR || got[0].Namespace != want.Namespace || got[0].Name != want.Name || string(got[0].Merge) != string(want.Merge) {
		t.Errorf("first patch = %+v, want %+v", got[0], want)
	}
	if got[1].GVR != gvrB || got[1].Name != "cluster" {
		t.Errorf("second patch = %+v", got[1])
	}
	got[0].Merge[0] = 'Y'
	if string(k.Patches()[0].Merge) != `{"spec":{"suspend":true}}` {
		t.Error("editing the returned patch changed the cluster's")
	}
	k.SetPatchError(nil)
	if err := k.Patch(context.Background(), gvrA, "ns", "name", nil); err != nil {
		t.Errorf("Patch after SetPatchError(nil) = %v", err)
	}
}

func TestKubeClusterFailedGroupsAreSortedCopies(t *testing.T) {
	k := NewKubeCluster()
	in := []schema.GroupVersion{{Group: "b", Version: "v1"}, {Group: "a", Version: "v2"}, {Group: "a", Version: "v1"}}
	k.SetFailedGroups(in...)
	want := []schema.GroupVersion{{Group: "a", Version: "v1"}, {Group: "a", Version: "v2"}, {Group: "b", Version: "v1"}}
	if got := k.FailedGroups(); !slices.Equal(got, want) {
		t.Errorf("FailedGroups = %v, want %v", got, want)
	}
	if in[0].Group != "b" {
		t.Error("SetFailedGroups sorted the caller's slice")
	}
	got := k.FailedGroups()
	got[0] = schema.GroupVersion{}
	if k.FailedGroups()[0].Group != "a" {
		t.Error("editing the returned slice changed the cluster's")
	}
	k.SetFailedGroups()
	if len(k.FailedGroups()) != 0 {
		t.Errorf("FailedGroups after clearing = %v", k.FailedGroups())
	}
}

func TestKubeClusterRefusedSetAndClear(t *testing.T) {
	k := NewKubeCluster()
	k.SetRefused(gvrA, "auth")
	if c, ok := k.Refused(gvrA); !ok || c != "auth" {
		t.Errorf("Refused = %q,%v, want auth,true", c, ok)
	}
	if _, ok := k.Refused(gvrB); ok {
		t.Error("Refused leaked to another gvr")
	}
	k.SetRefused(gvrA, "")
	if c, ok := k.Refused(gvrA); !ok || c != "" {
		t.Errorf("Refused with an empty category = %q,%v, want \"\",true", c, ok)
	}
	k.ClearRefused(gvrA)
	if _, ok := k.Refused(gvrA); ok {
		t.Error("Refused after ClearRefused")
	}
}

func TestKubeClusterSyncedSwitch(t *testing.T) {
	k := NewKubeCluster()
	k.SetSynced(false)
	if k.Synced() {
		t.Error("Synced after SetSynced(false)")
	}
	k.SetSynced(true)
	if !k.Synced() {
		t.Error("not Synced after SetSynced(true)")
	}
}
