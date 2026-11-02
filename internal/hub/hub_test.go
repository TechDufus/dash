package hub

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/TechDufus/dash/internal/model"
)

func src(ok bool, err string) []model.SourceStatus {
	return []model.SourceStatus{{Name: "x", OK: ok, Error: err, CheckedAt: time.Now()}}
}

func TestPublishDedupe(t *testing.T) {
	h := New()
	tests := []struct {
		name     string
		data     any
		sources  []model.SourceStatus
		wantSent bool
	}{
		{"first publish", map[string]int{"a": 1}, src(true, ""), true},
		{"same data, new checkedAt", map[string]int{"a": 1}, src(true, ""), false},
		{"data changed", map[string]int{"a": 2}, src(true, ""), true},
		{"source failed", map[string]int{"a": 2}, src(false, "boom"), true},
		{"same failure", map[string]int{"a": 2}, src(false, "boom"), false},
	}
	for _, tc := range tests {
		sent, err := h.Publish(model.TopicCluster, tc.data, tc.sources)
		if err != nil || sent != tc.wantSent {
			t.Errorf("%s: sent=%v err=%v, want sent=%v", tc.name, sent, err, tc.wantSent)
		}
	}
	ev, ok := h.Latest("", model.TopicCluster)
	if !ok {
		t.Fatal("no latest event")
	}
	var env model.Envelope
	if err := json.Unmarshal(ev.Data, &env); err != nil || env.Topic != "cluster" || string(env.Data) != `{"a":2}` || len(env.Sources) != 1 || env.Sources[0].OK {
		t.Errorf("envelope = %+v err=%v", env, err)
	}
}

func TestSubscribeSnapshotAndLive(t *testing.T) {
	h := New()
	h.SetOrder(func() []model.Topic { return []model.Topic{model.TopicOverview, model.TopicCluster, model.TopicApps} })
	h.Publish(model.TopicApps, 1, nil)
	h.Publish(model.TopicOverview, 2, nil)
	snap, ch, cancel := h.Subscribe("", nil)
	defer cancel()
	if len(snap) != 2 || snap[0].Topic != "core.overview" || snap[1].Topic != "apps" {
		t.Fatalf("snapshot must follow topic order, got %+v", snap)
	}
	h.Publish(model.TopicCluster, 3, nil)
	select {
	case ev := <-ch:
		if ev.Topic != "cluster" {
			t.Errorf("live topic = %s", ev.Topic)
		}
	case <-time.After(time.Second):
		t.Fatal("no live event")
	}
}

func topicsOf(evs []Event) []string {
	var out []string
	for _, ev := range evs {
		out = append(out, ev.Topic)
	}
	return out
}

func TestSubscribeOrderFromSetOrder(t *testing.T) {
	h := New()
	for _, name := range []model.Topic{"zeta", "alpha", "mid", "core.plugins", "beta"} {
		h.Publish(name, 1, nil)
	}
	names := func() []string { snap, _, c := h.Subscribe("", nil); c(); return topicsOf(snap) }

	if got := names(); !slices.Equal(got, []string{"alpha", "beta", "core.plugins", "mid", "zeta"}) {
		t.Errorf("no order set: %v, want name order", got)
	}
	// Listed topics come first in the listed order (missing and repeated names are harmless);
	// the rest follow by name.
	h.SetOrder(func() []model.Topic { return []model.Topic{"mid", "absent", "core.plugins", "mid"} })
	if got := names(); !slices.Equal(got, []string{"mid", "core.plugins", "alpha", "beta", "zeta"}) {
		t.Errorf("with order: %v", got)
	}
	h.SetOrder(nil)
	if got := names(); !slices.Equal(got, []string{"alpha", "beta", "core.plugins", "mid", "zeta"}) {
		t.Errorf("order reset: %v", got)
	}
}

func TestSubscribeOrderFuncRunsOutsideTheLock(t *testing.T) {
	h := New()
	h.Publish("a", 1, nil)
	h.SetOrder(func() []model.Topic {
		h.Latest("", "a") // takes the hub lock: would deadlock if Subscribe held it
		return nil
	})
	done := make(chan struct{})
	go func() { _, _, c := h.Subscribe("", nil); c(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Subscribe deadlocked calling the order func")
	}
}

func TestSubscribeFilterAppliesToSnapshotAndLive(t *testing.T) {
	h := New()
	h.Publish("a", 1, nil)
	h.Publish("b", 1, nil)
	snap, ch, cancel := h.Subscribe("", func(topic string) bool { return topic == "b" || topic == "c" })
	defer cancel()
	if got := topicsOf(snap); !slices.Equal(got, []string{"b"}) {
		t.Fatalf("snapshot = %v, want only b", got)
	}
	h.Publish("a", 2, nil) // filtered out
	h.Publish("c", 1, nil) // unknown at subscribe time, still delivered
	select {
	case ev := <-ch:
		if ev.Topic != "c" {
			t.Errorf("live topic = %s, want c (a is filtered)", ev.Topic)
		}
	case <-time.After(time.Second):
		t.Fatal("no live event")
	}
}

func TestFilteredTopicsDoNotOverflowASubscriber(t *testing.T) {
	h := New()
	_, ch, cancel := h.Subscribe("", func(topic string) bool { return topic == "wanted" })
	defer cancel()
	for i := range subscriberBuffer * 3 {
		h.Publish("noise", i, nil)
	}
	h.Publish("wanted", 1, nil)
	select {
	case ev, ok := <-ch:
		if !ok || ev.Topic != "wanted" {
			t.Errorf("got %+v ok=%v, want the wanted event on an open channel", ev, ok)
		}
	case <-time.After(time.Second):
		t.Fatal("no live event")
	}
}

func TestRemoveDropsLatestAndNewSubscribersGetNothing(t *testing.T) {
	h := New()
	h.Publish("gone", 1, nil)
	h.Publish("kept", 1, nil)
	h.PublishFor("alice", "gone", 2, nil)
	_, ch, cancel := h.Subscribe("alice", nil)
	defer cancel()
	h.Remove("gone")

	if _, ok := h.Latest("alice", "gone"); ok {
		t.Error("Latest still returns the removed topic for the scoped user")
	}
	if _, ok := h.Latest("", "gone"); ok {
		t.Error("Latest still returns the removed global topic")
	}
	snap, _, c2 := h.Subscribe("alice", nil)
	c2()
	if got := topicsOf(snap); !slices.Equal(got, []string{"kept"}) {
		t.Errorf("new subscriber snapshot = %v, want only kept", got)
	}
	select {
	case ev, ok := <-ch:
		t.Errorf("Remove sent %+v (open=%v); it must send nothing", ev, ok)
	default:
	}
	// The dedup digest went with it: the same data publishes again.
	if sent, _ := h.Publish("gone", 1, nil); !sent {
		t.Error("identical publish after Remove was deduplicated")
	}
}

func TestUserScopedTopicOnlyReachesItsOwner(t *testing.T) {
	h := New()
	h.PublishFor("alice", "core.approvals", "for-alice", nil)
	h.Publish("shared", 1, nil)

	aSnap, aCh, aCancel := h.Subscribe("alice", nil)
	defer aCancel()
	bSnap, bCh, bCancel := h.Subscribe("bob", nil)
	defer bCancel()
	if got := topicsOf(aSnap); !slices.Equal(got, []string{"core.approvals", "shared"}) {
		t.Errorf("alice snapshot = %v", got)
	}
	if got := topicsOf(bSnap); !slices.Equal(got, []string{"shared"}) {
		t.Errorf("bob snapshot = %v, must not hold alice's topic", got)
	}

	h.PublishFor("alice", "core.approvals", "again", nil)
	h.PublishFor("bob", "core.approvals", "for-bob", nil)
	if ev := <-aCh; ev.Topic != "core.approvals" || ev.User != "alice" || !strings.Contains(string(ev.Data), "again") {
		t.Errorf("alice live = %+v", ev)
	}
	if ev := <-bCh; !strings.Contains(string(ev.Data), "for-bob") {
		t.Errorf("bob's first live event = %s, want his own", ev.Data)
	}
	select {
	case ev := <-aCh:
		t.Errorf("alice received a second event: %s", ev.Data)
	case ev := <-bCh:
		t.Errorf("bob received a second event: %s", ev.Data)
	default:
	}

	// Latest: own copy; another user and an anonymous caller get nothing.
	if ev, ok := h.Latest("alice", "core.approvals"); !ok || !strings.Contains(string(ev.Data), "again") {
		t.Errorf("alice Latest = %+v ok=%v", ev, ok)
	}
	if ev, ok := h.Latest("bob", "core.approvals"); !ok || !strings.Contains(string(ev.Data), "for-bob") {
		t.Errorf("bob Latest = %+v ok=%v", ev, ok)
	}
	if _, ok := h.Latest("carol", "core.approvals"); ok {
		t.Error("carol sees a scoped topic she has no copy of")
	}
	if _, ok := h.Latest("", "core.approvals"); ok {
		t.Error("the global view holds a scoped copy")
	}
	if _, err := h.PublishFor("", "x", 1, nil); err == nil {
		t.Error("PublishFor with no user must fail, not publish globally")
	}
}

func TestPublishForDedupIsPerUser(t *testing.T) {
	h := New()
	for _, tc := range []struct {
		user string
		data any
		want bool
	}{
		{"alice", 1, true},
		{"alice", 1, false}, // same user, same content
		{"bob", 1, true},    // another user's digest is separate
		{"alice", 2, true},
		{"bob", 1, false},
	} {
		if sent, err := h.PublishFor(tc.user, "t", tc.data, nil); err != nil || sent != tc.want {
			t.Errorf("PublishFor(%s, %v) sent=%v err=%v, want %v", tc.user, tc.data, sent, err, tc.want)
		}
	}
	// The global topic of the same name has its own digest too.
	if sent, _ := h.Publish("t", 1, nil); !sent {
		t.Error("a global publish was deduplicated against a scoped one")
	}
}

func TestHashDedupUnchanged(t *testing.T) {
	h := New()
	if sent, _ := h.Publish("t", map[string]int{"a": 1}, src(true, "")); !sent {
		t.Fatal("first publish not sent")
	}
	first, _ := h.Latest("", "t")
	// A new checkedAt and a later clock change neither the dedup decision nor the stored event.
	h.now = func() time.Time { return time.Now().Add(time.Hour) }
	if sent, _ := h.Publish("t", map[string]int{"a": 1}, src(true, "")); sent {
		t.Error("same data and sources published again")
	}
	if again, _ := h.Latest("", "t"); !bytes.Equal(again.Data, first.Data) {
		t.Error("a deduplicated publish replaced the stored envelope")
	}
}

func TestETagIsDigestPrefixAndStableAcrossSourceTimeChanges(t *testing.T) {
	h := New()
	sources := []model.SourceStatus{{Name: "x", OK: true, CheckedAt: time.Now()}}
	h.Publish("t", map[string]int{"a": 1}, sources)
	ev, _ := h.Latest("", "t")

	// Independent of the implementation: the first 16 hex chars of sha256(data, 0, name, ok-flag, error).
	sum := sha256.Sum256([]byte(`{"a":1}` + "\x00x\x01"))
	if want := hex.EncodeToString(sum[:])[:16]; ev.ETag != want {
		t.Errorf("ETag = %q, want %q", ev.ETag, want)
	}
	if ev.User != "" || ev.Topic != "t" {
		t.Errorf("event = %+v", ev)
	}

	// A new checkedAt is deduplicated: the stored ETag stays.
	sources[0].CheckedAt = sources[0].CheckedAt.Add(time.Minute)
	h.Publish("t", map[string]int{"a": 1}, sources)
	if now, _ := h.Latest("", "t"); now.ETag != ev.ETag {
		t.Errorf("ETag moved from %s to %s on a checkedAt change", ev.ETag, now.ETag)
	}
	// A content change and a source failure each move it.
	h.Publish("t", map[string]int{"a": 2}, sources)
	changed, _ := h.Latest("", "t")
	h.Publish("t", map[string]int{"a": 2}, src(false, "boom"))
	failed, _ := h.Latest("", "t")
	if changed.ETag == ev.ETag || failed.ETag == changed.ETag {
		t.Errorf("ETags did not change: %s, %s, %s", ev.ETag, changed.ETag, failed.ETag)
	}
	// Scoped copies carry the same kind of tag and their user.
	h.PublishFor("alice", "t", 1, nil)
	if own, _ := h.Latest("alice", "t"); own.User != "alice" || len(own.ETag) != 16 {
		t.Errorf("scoped event = %+v", own)
	}
}

func TestSlowSubscriberIsDropped(t *testing.T) {
	h := New()
	_, ch, cancel := h.Subscribe("", nil)
	defer cancel()
	for i := range subscriberBuffer + 1 {
		h.Publish(model.TopicCluster, i, nil)
	}
	n := 0
	for range ch { // closed by the hub after the buffer overflowed
		n++
	}
	if n != subscriberBuffer {
		t.Errorf("drained %d events, want %d", n, subscriberBuffer)
	}
	// A second cancel after the hub closed the channel must be harmless.
	cancel()
}

func TestResendRedeliversTheLatestEventUnchanged(t *testing.T) {
	h := New()
	h.Publish("p.a", map[string]int{"a": 1}, nil)
	before, _ := h.Latest("", "p.a")
	_, ch, cancel := h.Subscribe("", nil)
	defer cancel()
	if !h.Resend("p.a") {
		t.Fatal("Resend of a topic with a stored event must report true")
	}
	select {
	case ev := <-ch:
		if ev.Topic != "p.a" || ev.ETag != before.ETag || !bytes.Equal(ev.Data, before.Data) {
			t.Errorf("resent %+v, want the stored event %+v", ev, before)
		}
	case <-time.After(time.Second):
		t.Fatal("nothing resent")
	}
	after, _ := h.Latest("", "p.a")
	if after.ETag != before.ETag || !bytes.Equal(after.Data, before.Data) {
		t.Errorf("Resend changed the stored event: %+v -> %+v", before, after)
	}
	// The digest is untouched too: an identical publish is still deduplicated.
	if sent, _ := h.Publish("p.a", map[string]int{"a": 1}, nil); sent {
		t.Error("an identical Publish after Resend must still be deduplicated")
	}
}

func TestResendWithoutAStoredEvent(t *testing.T) {
	h := New()
	_, ch, cancel := h.Subscribe("", nil)
	defer cancel()
	if h.Resend("p.none") {
		t.Error("Resend of an unknown topic must report false")
	}
	select {
	case ev := <-ch:
		t.Errorf("unexpected event %+v", ev)
	default:
	}
}

func TestResendHonoursTopicFilterAndUserScope(t *testing.T) {
	h := New()
	h.Publish("p.a", 1, nil)
	h.Publish("p.b", 2, nil)
	h.PublishFor("alice", "p.a", 3, nil)
	h.PublishFor("bob", "p.a", 4, nil)

	_, aliceCh, cancelA := h.Subscribe("alice", func(topic string) bool { return topic == "p.a" })
	defer cancelA()
	_, bobCh, cancelB := h.Subscribe("bob", func(topic string) bool { return topic == "p.a" })
	defer cancelB()
	_, otherCh, cancelO := h.Subscribe("carol", func(topic string) bool { return topic == "p.b" })
	defer cancelO()

	if !h.Resend("p.a") {
		t.Fatal("Resend = false")
	}
	drain := func(ch <-chan Event) (out []string) {
		for {
			select {
			case ev := <-ch:
				out = append(out, ev.Topic+"/"+ev.User)
			default:
				return out
			}
		}
	}
	// The global copy and the subscriber's own copy; another user's copy never.
	if got, want := drain(aliceCh), []string{"p.a/", "p.a/alice"}; !slices.Equal(got, want) {
		t.Errorf("alice got %v, want %v", got, want)
	}
	if got, want := drain(bobCh), []string{"p.a/", "p.a/bob"}; !slices.Equal(got, want) {
		t.Errorf("bob got %v, want %v", got, want)
	}
	// A subscriber that did not ask for the topic gets nothing.
	if got := drain(otherCh); len(got) != 0 {
		t.Errorf("carol got %v for a topic she did not ask for", got)
	}
}

func TestResendDropsASlowSubscriber(t *testing.T) {
	h := New()
	h.Publish("p.a", 1, nil)
	_, ch, cancel := h.Subscribe("", nil)
	defer cancel()
	for range subscriberBuffer + 1 {
		h.Resend("p.a")
	}
	n := 0
	for range ch {
		n++
	}
	if n != subscriberBuffer {
		t.Errorf("drained %d events, want %d", n, subscriberBuffer)
	}
}
