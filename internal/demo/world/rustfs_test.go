package world

import (
	"reflect"
	"testing"

	"github.com/TechDufus/dash/internal/model"
)

// The fake store has a bucket without a quota, none over its quota, and a capacity that holds
// what the buckets use.
func TestRustFSBucketsAndCapacity(t *testing.T) {
	d := testWorld().RustFS()
	if len(d.Buckets) < 2 {
		t.Fatalf("buckets = %+v", d.Buckets)
	}
	var used int64
	noQuota := 0
	for _, b := range d.Buckets {
		used += b.UsedBytes
		if b.QuotaBytes == 0 {
			noQuota++
		} else if b.UsedBytes > b.QuotaBytes {
			t.Errorf("bucket %s is over its quota", b.Name)
		}
		if b.Health != model.HealthOK || b.Ref != "" {
			t.Errorf("bucket %+v: want ok health and no ref (the twin adds it)", b)
		}
	}
	if noQuota == 0 {
		t.Error("no bucket without a quota")
	}
	if d.Capacity.UsableBytes <= 0 || d.Capacity.UsedBytes < used || d.Capacity.UsedBytes > d.Capacity.UsableBytes {
		t.Errorf("capacity = %+v, buckets use %d", d.Capacity, used)
	}
}

func TestRustFSIsDeterministic(t *testing.T) {
	if a, b := testWorld().RustFS(), testWorld().RustFS(); !reflect.DeepEqual(a, b) {
		t.Errorf("two builds differ: %+v %+v", a, b)
	}
}
