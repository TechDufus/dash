package push

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/TechDufus/dash/internal/credentials"
	"github.com/TechDufus/dash/internal/hub"
	"github.com/TechDufus/dash/internal/model"
)

func item(id string, sev model.Severity) model.AttentionItem {
	return model.AttentionItem{ID: id, Severity: sev, Source: "kubernetes", Title: "Pod " + id + " crash-looping", Detail: "restarted 5 times", Route: "/cluster/pods/apps/" + id}
}

func newNotifier(e *env, may func(string) bool) *Notifier {
	return NewNotifier(NotifierOptions{Service: e.svc, Store: e.store, MayNotify: may, Now: e.clock})
}

// pushed are the attention pushes device d received, decrypted.
func pushed(t *testing.T, f *fakeService, d device) []model.PushMessage {
	t.Helper()
	var out []model.PushMessage
	for _, r := range f.requests() {
		if r.path == strings.TrimPrefix(d.endpoint, f.URL) {
			out = append(out, d.open(t, r))
		}
	}
	return out
}

// An item still present two minutes after dash first saw it is pushed once; it is pushed again
// only after it cleared and reappeared, and a reappearance waits its own two minutes.
func TestAttentionTwoMinutesAndRenotify(t *testing.T) {
	f := newFakeService(t)
	e := newEnv(t, f, loopback)
	d := e.subscribe(f, "alice", false, nil)
	n := newNotifier(e, nil)
	tick := func() { n.Tick(); e.svc.Wait() }

	n.Observe([]model.AttentionItem{item("api", model.SeverityWarn)})
	e.advance(AttentionDelay - time.Second)
	n.Observe([]model.AttentionItem{item("api", model.SeverityWarn)}) // republished: still the first sighting
	tick()
	if got := pushed(t, f, d); len(got) != 0 {
		t.Fatalf("pushed before two minutes: %+v", got)
	}
	e.advance(time.Second)
	tick()
	got := pushed(t, f, d)
	if len(got) != 1 || got[0].ID != "api" || got[0].Type != model.PushTypeAttention || got[0].URL != "https://dash.example.com/cluster/pods/apps/api" ||
		got[0].Body != "kubernetes: restarted 5 times" {
		t.Fatalf("after two minutes: %+v", got)
	}
	e.advance(10 * time.Minute)
	n.Observe([]model.AttentionItem{item("api", model.SeverityWarn)})
	tick()
	if len(pushed(t, f, d)) != 1 {
		t.Fatal("a standing item was pushed again")
	}

	// It clears, comes back for less than two minutes, clears again: nothing.
	n.Observe(nil)
	n.Observe([]model.AttentionItem{item("api", model.SeverityWarn)})
	e.advance(time.Minute)
	tick()
	n.Observe(nil)
	e.advance(5 * time.Minute)
	tick()
	if len(pushed(t, f, d)) != 1 {
		t.Fatal("an item that came back for a minute was pushed")
	}
	// It comes back and stays: pushed once more.
	n.Observe([]model.AttentionItem{item("api", model.SeverityWarn)})
	e.advance(AttentionDelay)
	tick()
	if len(pushed(t, f, d)) != 2 {
		t.Errorf("a reappeared item was not pushed again: %d pushes", len(pushed(t, f, d)))
	}
}

// Each subscription gets the items at or above its threshold: crit takes critical items only,
// warn both, off none; a pending device and a user who may no longer see the overview none.
func TestAttentionThresholdsAndRoles(t *testing.T) {
	f := newFakeService(t)
	e := newEnv(t, f, loopback)
	warn := e.subscribe(f, "alice", false, func(in *credentials.PushInput) { in.Attention = model.PushAttentionWarn })
	crit := e.subscribe(f, "alice", false, func(in *credentials.PushInput) { in.Attention = model.PushAttentionCrit })
	off := e.subscribe(f, "alice", false, func(in *credentials.PushInput) { in.Attention = model.PushAttentionOff })
	pending := e.subscribe(f, "alice", true, nil)
	removed := e.subscribe(f, "mallory", false, nil)
	n := newNotifier(e, func(user string) bool { return user != "mallory" })

	n.Observe([]model.AttentionItem{item("w", model.SeverityWarn), item("c", model.SeverityCrit)})
	e.advance(AttentionDelay)
	n.Tick()
	e.svc.Wait()
	ids := func(d device) string {
		var s []string
		for _, m := range pushed(t, f, d) {
			s = append(s, m.ID)
		}
		return strings.Join(s, ",")
	}
	if got := ids(warn); got != "c,w" && got != "w,c" {
		t.Errorf("warn threshold got %q", got)
	}
	if got := ids(crit); got != "c" {
		t.Errorf("crit threshold got %q", got)
	}
	for name, d := range map[string]device{"off": off, "pending": pending, "removed user": removed} {
		if got := ids(d); got != "" {
			t.Errorf("%s got %q", name, got)
		}
	}
	for _, r := range f.requests() {
		if r.path == strings.TrimPrefix(crit.endpoint, f.URL) && r.header.Get("Urgency") != "high" {
			t.Error("a critical item is not urgent")
		}
	}
}

// A user receives at most 30 attention pushes an hour; the rest become one summary when the
// hour ends, and a new hour starts counting again.
func TestAttentionHourlyCapAndSummary(t *testing.T) {
	f := newFakeService(t)
	e := newEnv(t, f, loopback)
	d := e.subscribe(f, "alice", false, nil)
	n := newNotifier(e, nil)
	var items []model.AttentionItem
	for i := range AttentionHourlyLimit + 3 {
		items = append(items, item(fmt.Sprintf("i%02d", i), model.SeverityWarn))
	}
	n.Observe(items)
	e.advance(AttentionDelay)
	n.Tick()
	e.svc.Wait()
	if got := len(pushed(t, f, d)); got != AttentionHourlyLimit {
		t.Fatalf("%d pushes in the hour, want %d", got, AttentionHourlyLimit)
	}
	e.advance(time.Hour - time.Second)
	n.Tick()
	e.svc.Wait()
	if got := len(pushed(t, f, d)); got != AttentionHourlyLimit {
		t.Fatalf("the summary came before the hour ended (%d pushes)", got)
	}
	e.advance(time.Second)
	n.Tick()
	e.svc.Wait()
	got := pushed(t, f, d)
	if len(got) != AttentionHourlyLimit+1 || !strings.Contains(got[len(got)-1].Title, "3 more items need attention") {
		t.Fatalf("the summary = %+v", got[len(got)-1])
	}
	// A new item in the next hour is pushed on its own.
	n.Observe(append(items, item("next", model.SeverityWarn)))
	e.advance(AttentionDelay)
	n.Tick()
	e.svc.Wait()
	if got := pushed(t, f, d); len(got) != AttentionHourlyLimit+2 || got[len(got)-1].ID != "next" {
		t.Errorf("the next hour: %d pushes, last %+v", len(got), got[len(got)-1])
	}
}

// A warning pushed to the warn devices that turns critical is pushed once to the crit devices,
// and not again to the warn ones; it is not pushed a third time while it stands.
func TestAttentionEscalationReachesCritDevices(t *testing.T) {
	f := newFakeService(t)
	e := newEnv(t, f, loopback)
	warn := e.subscribe(f, "alice", false, func(in *credentials.PushInput) { in.Attention = model.PushAttentionWarn })
	crit := e.subscribe(f, "alice", false, func(in *credentials.PushInput) { in.Attention = model.PushAttentionCrit })
	n := newNotifier(e, nil)
	tick := func() { n.Tick(); e.svc.Wait() }

	n.Observe([]model.AttentionItem{item("db", model.SeverityWarn)})
	e.advance(AttentionDelay)
	tick()
	if len(pushed(t, f, warn)) != 1 || len(pushed(t, f, crit)) != 0 {
		t.Fatalf("the warning: warn %d, crit %d pushes", len(pushed(t, f, warn)), len(pushed(t, f, crit)))
	}
	n.Observe([]model.AttentionItem{item("db", model.SeverityCrit)})
	tick()
	got := pushed(t, f, crit)
	if len(got) != 1 || got[0].ID != "db" || len(pushed(t, f, warn)) != 1 {
		t.Fatalf("the escalation: crit %+v, warn %d pushes", got, len(pushed(t, f, warn)))
	}
	n.Observe([]model.AttentionItem{item("db", model.SeverityWarn)})
	tick()
	n.Observe([]model.AttentionItem{item("db", model.SeverityCrit)})
	tick()
	if len(pushed(t, f, crit)) != 1 || len(pushed(t, f, warn)) != 1 {
		t.Errorf("a standing item flapping between warn and crit was pushed again")
	}
}

// The hour's summary reaches each device with the count of the held-back items its threshold
// covers: a crit device hears only of the critical ones.
func TestAttentionSummaryFollowsThresholds(t *testing.T) {
	f := newFakeService(t)
	e := newEnv(t, f, loopback)
	warn := e.subscribe(f, "alice", false, func(in *credentials.PushInput) { in.Attention = model.PushAttentionWarn })
	crit := e.subscribe(f, "alice", false, func(in *credentials.PushInput) { in.Attention = model.PushAttentionCrit })
	n := newNotifier(e, nil)
	var items []model.AttentionItem
	for i := range AttentionHourlyLimit {
		items = append(items, item(fmt.Sprintf("a%02d", i), model.SeverityWarn))
	}
	items = append(items, item("b0", model.SeverityWarn), item("b1", model.SeverityWarn), item("c0", model.SeverityCrit))
	n.Observe(items)
	e.advance(AttentionDelay)
	n.Tick()
	e.svc.Wait() // the hour's pushes arrive first: deliveries run in parallel
	e.advance(time.Hour)
	n.Tick()
	e.svc.Wait()
	last := func(d device) model.PushMessage {
		got := pushed(t, f, d)
		if len(got) == 0 {
			t.Fatal("no push")
		}
		return got[len(got)-1]
	}
	if m := last(warn); !strings.Contains(m.Title, "3 more items need attention") || len(pushed(t, f, warn)) != AttentionHourlyLimit+1 {
		t.Errorf("the warn device's summary = %q", m.Title)
	}
	if got := pushed(t, f, crit); len(got) != 1 || !strings.Contains(got[0].Title, "1 more items need attention") {
		t.Errorf("the crit device got %+v, want only a summary of the one critical item", got)
	}
}

// An hour that held nothing back ends without a summary, and so does it for a device whose
// threshold covers none of the held-back items.
func TestAttentionNoSummaryWithoutExcess(t *testing.T) {
	f := newFakeService(t)
	e := newEnv(t, f, loopback)
	d := e.subscribe(f, "alice", false, nil)
	crit := e.subscribe(f, "bob", false, func(in *credentials.PushInput) { in.Attention = model.PushAttentionCrit })
	e.subscribe(f, "bob", false, func(in *credentials.PushInput) { in.Attention = model.PushAttentionWarn })
	n := newNotifier(e, func(user string) bool { return user != "bob" })
	n.Observe([]model.AttentionItem{item("x", model.SeverityWarn), item("y", model.SeverityCrit)})
	e.advance(AttentionDelay)
	n.Tick()
	e.advance(time.Hour)
	n.Tick()
	e.svc.Wait()
	if got := pushed(t, f, d); len(got) != 2 {
		t.Errorf("%d pushes, want the two items and no summary: %+v", len(got), got)
	}

	// bob may see the overview from now on; more warnings than the cap, no critical item.
	n.o.MayNotify = func(string) bool { return true }
	var items []model.AttentionItem
	for i := range AttentionHourlyLimit + 2 {
		items = append(items, item(fmt.Sprintf("w%02d", i), model.SeverityWarn))
	}
	n.Observe(items)
	e.advance(AttentionDelay)
	n.Tick()
	e.advance(time.Hour)
	n.Tick()
	e.svc.Wait()
	if got := pushed(t, f, crit); len(got) != 0 {
		t.Errorf("the crit device got %+v, want nothing: no critical item was held back", got)
	}
}

// Run reads core.overview from the hub: its snapshot and every later publish.
func TestNotifierFollowsTheOverview(t *testing.T) {
	f := newFakeService(t)
	e := newEnv(t, f, loopback)
	n := newNotifier(e, nil)
	h := hub.New()
	if _, err := h.Publish(model.TopicOverview, model.OverviewData{Attention: []model.AttentionItem{item("a", model.SeverityCrit)}}, nil); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); n.Run(ctx, h) }()
	defer func() { cancel(); <-done }()
	has := func(ids ...string) bool {
		n.mu.Lock()
		defer n.mu.Unlock()
		if len(n.items) != len(ids) {
			return false
		}
		for _, id := range ids {
			if n.items[id] == nil {
				return false
			}
		}
		return true
	}
	waitUntil(t, func() bool { return has("a") })
	if _, err := h.Publish(model.TopicOverview, model.OverviewData{Attention: []model.AttentionItem{item("b", model.SeverityWarn)}}, nil); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, func() bool { return has("b") })
}

func waitUntil(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("timed out")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
