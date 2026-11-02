package world

import (
	"reflect"
	"testing"
	"time"
)

// The fake Postgres cluster is a ready one with a backup age that drifts but stays plausible.
func TestCNPGClusterIsHealthyWithARecentBackup(t *testing.T) {
	d := testWorld().CNPG()
	if len(d.Clusters) != 1 {
		t.Fatalf("clusters = %+v", d.Clusters)
	}
	c := d.Clusters[0]
	if c.Instances == 0 || c.ReadyInstances != c.Instances || c.Phase == "" {
		t.Errorf("cluster = %+v, want every instance ready", c)
	}
	if c.LastBackupAgeSeconds == nil || *c.LastBackupAgeSeconds < 0 || *c.LastBackupAgeSeconds > 36*3600 {
		t.Errorf("backup age = %v, want under the 36 h warning", c.LastBackupAgeSeconds)
	}
	if len(c.Links) == 0 {
		t.Error("no links")
	}
	if c.Ref != "" {
		t.Errorf("ref = %q: the twin adds it", c.Ref)
	}
}

func TestCNPGIsDeterministicAndMovesWithTheClock(t *testing.T) {
	if a, b := testWorld().CNPG(), testWorld().CNPG(); !reflect.DeepEqual(a, b) {
		t.Errorf("two builds at one instant differ: %+v %+v", a, b)
	}
	later := New(func() time.Time { return testNow.Add(3 * time.Hour) }, testStart, fakeLinks{}).CNPG()
	if reflect.DeepEqual(testWorld().CNPG(), later) {
		t.Error("the backup age does not move with the clock")
	}
}
