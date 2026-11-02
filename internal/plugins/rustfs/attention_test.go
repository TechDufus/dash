package rustfs

import (
	"testing"
	"time"

	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin/plugintest"
)

// The bucket rule the kubernetes plugin's storage topic had: id bucket:<name>, only for a bucket
// with a quota, warning from 80%, critical from 90%, source storage, route /storage, the
// bucket's ref and an empty links list.
func TestBucketsRatioAndAttention(t *testing.T) {
	items := attentionOf(bucketsOf(snapshot{
		bucketUsed:  map[string]float64{"noquota": 999, "fine": 50, "edge": 79, "warn": 80, "crit": 90, "full": 95},
		bucketQuota: map[string]float64{"fine": 100, "edge": 100, "warn": 100, "crit": 100, "full": 100},
	}))
	got := map[string]model.AttentionItem{}
	for _, it := range items {
		got[it.ID] = it
	}
	if len(items) != 3 || got["bucket:warn"].ID == "" || got["bucket:crit"].ID == "" || got["bucket:full"].ID == "" {
		t.Fatalf("items = %+v, want warn, crit and full only", items)
	}
	if got["bucket:warn"].Severity != model.SeverityWarn || got["bucket:crit"].Severity != model.SeverityCrit {
		t.Errorf("severities: warn %q crit %q", got["bucket:warn"].Severity, got["bucket:crit"].Severity)
	}
	full := got["bucket:full"]
	if full.Severity != model.SeverityCrit || full.Title != "Bucket full is 95% of its quota" || full.Source != "storage" ||
		full.Route != "/storage" || full.Plugin != "rustfs" || full.Ref != "rustfs:Bucket/full" || full.Links == nil || len(full.Links) != 0 ||
		full.Detail != "" || !full.Since.IsZero() {
		t.Errorf("full = %+v", full)
	}
	plugintest.AssertRefs(t, nil, ID, items)
}

func TestAttentionOfNothingIsAnEmptyList(t *testing.T) {
	if items := attentionOf(bucketsOf(snapshot{})); items == nil || len(items) != 0 {
		t.Errorf("items = %#v, want an empty non-nil slice", items)
	}
}

// Through the supervisor: nil (no opinion) before the first poll, then a non-nil list: empty for
// a store with nothing close to its quota, and the item the supervisor stamps with the time it
// first saw it. An emptied bucket list prunes the sighting, so a bucket that fills again is
// stamped again.
func TestAttentionIsNonNilWhenNothingToRaise(t *testing.T) {
	r := newRig(t, rigOpts{gated: true, prefill: func(q *fakeQuerier) {
		q.set(exprBucketUsed, sample(10, "bucket", "a"))
		q.set(exprBucketQuota, sample(100, "bucket", "a"))
	}})
	r.waitState(model.PluginStarting)
	if c, ok := r.contributor(); ok && c.Attention.Attention(time.Now()) != nil {
		t.Error("attention is not nil before the first poll")
	}
	close(r.q.gate)
	r.waitState(model.PluginHealthy)
	c, ok := r.contributor()
	if !ok {
		t.Fatal("the plugin is not a contributor")
	}
	plugintest.AssertAttentionNonNil(t, c)
	if items := c.Attention.Attention(time.Now()); len(items) != 0 {
		t.Errorf("items = %+v, want none", items)
	}
}

func TestAttentionStampsAndPrunesFirstSightings(t *testing.T) {
	r := newRig(t, rigOpts{prefill: func(q *fakeQuerier) {
		q.set(exprBucketUsed, sample(95, "bucket", "a"))
		q.set(exprBucketQuota, sample(100, "bucket", "a"))
	}})
	r.waitState(model.PluginHealthy)
	c, _ := r.contributor()
	t0 := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	items := c.Attention.Attention(t0)
	if len(items) != 1 || !items[0].Since.Equal(t0) {
		t.Fatalf("items = %+v, want one first seen at t0", items)
	}
	if again := c.Attention.Attention(t0.Add(time.Hour)); len(again) != 1 || !again[0].Since.Equal(t0) {
		t.Errorf("a second look moved the first sighting: %+v", again)
	}

	// The bucket empties: the list is empty (not nil), and the sighting is pruned.
	r.q.set(exprBucketUsed, sample(10, "bucket", "a"))
	eventually(t, "the bucket to leave attention", func() bool {
		got := c.Attention.Attention(t0.Add(2 * time.Hour))
		return got != nil && len(got) == 0
	})
	r.q.set(exprBucketUsed, sample(95, "bucket", "a"))
	later := t0.Add(3 * time.Hour)
	eventually(t, "the bucket to come back", func() bool { return len(c.Attention.Attention(later)) == 1 })
	if got := c.Attention.Attention(later.Add(time.Minute)); !got[0].Since.Equal(later) {
		t.Errorf("a bucket that filled again has since %v, want %v", got[0].Since, later)
	}
}

// A hidden-by-supervisor or unbuilt plugin raises nothing: the contributor of a disabled plugin
// does not exist.
func TestDisabledPluginIsNotAContributor(t *testing.T) {
	r := newRig(t, rigOpts{unset: true})
	r.waitState(model.PluginDisabled)
	if _, ok := r.contributor(); ok {
		t.Error("a disabled plugin contributes to the overview")
	}
}
