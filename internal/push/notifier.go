package push

import (
	"cmp"
	"context"
	"encoding/json"
	"log/slog"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/TechDufus/dash/internal/credentials"
	"github.com/TechDufus/dash/internal/hub"
	"github.com/TechDufus/dash/internal/model"
)

// The attention rule (design P.3, contract D43).
const (
	// AttentionDelay is how long an item must last before it is pushed.
	AttentionDelay = 2 * time.Minute
	// AttentionHourlyLimit is how many attention pushes one user receives in an hour; the rest
	// become one summary push when the hour ends.
	AttentionHourlyLimit = 30
	// attentionTick is how often the notifier looks for items that reached AttentionDelay: the
	// overview is published only when it changes, so time alone must make an item due.
	attentionTick = 15 * time.Second
)

// NotifierOptions build a Notifier.
type NotifierOptions struct {
	Service *Service
	Store   *credentials.Store
	// MayNotify rechecks at send time that user may still see the overview (the user may have
	// been removed or demoted since subscribing); nil allows everyone.
	MayNotify func(user string) bool
	Now       func() time.Time // nil: time.Now
	Log       *slog.Logger     // nil: slog.Default()
}

// Notifier turns the overview's attention items into pushes: an item at or above a
// subscription's threshold that is still present AttentionDelay after dash first saw it is
// pushed once to that subscription; it is pushed again only after it cleared and came back. A
// warning pushed as such that turns critical is pushed once more, to the devices that take
// critical items only. Each user receives at most AttentionHourlyLimit attention pushes an hour,
// then one summary per device of the items its threshold covers.
type Notifier struct {
	o   NotifierOptions
	log *slog.Logger

	mu    sync.Mutex
	items map[string]*tracked
	users map[string]*hourWindow
}

// tracked is one attention item present in the overview.
type tracked struct {
	item      model.AttentionItem
	firstSeen time.Time
	pushed    model.Severity // the highest severity pushed; "" before the first push
}

// hourWindow counts one user's attention pushes from the first one of the hour, and the items
// held back past the cap: forWarn those a warn device would have received, forCrit those a crit
// device would have.
type hourWindow struct {
	start            time.Time
	sent             int
	forWarn, forCrit int
}

// rank orders severities: none, warn, crit.
func rank(s model.Severity) int {
	switch s {
	case "":
		return 0
	case model.SeverityCrit:
		return 2
	}
	return 1
}

// NewNotifier builds the notifier; Run feeds it.
func NewNotifier(o NotifierOptions) *Notifier {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.MayNotify == nil {
		o.MayNotify = func(string) bool { return true }
	}
	log := o.Log
	if log == nil {
		log = slog.Default()
	}
	return &Notifier{o: o, log: log, items: map[string]*tracked{}, users: map[string]*hourWindow{}}
}

// Observe takes the overview's current attention items: a new ID is first seen now, an ID no
// longer there has cleared and is forgotten, so its return counts as new.
func (n *Notifier) Observe(items []model.AttentionItem) {
	now := n.o.Now()
	n.mu.Lock()
	defer n.mu.Unlock()
	present := make(map[string]bool, len(items))
	for _, it := range items {
		if it.ID == "" {
			continue
		}
		present[it.ID] = true
		if t, ok := n.items[it.ID]; ok {
			t.item = it // the latest title, detail and severity
			continue
		}
		n.items[it.ID] = &tracked{item: it, firstSeen: now}
	}
	for id := range n.items {
		if !present[id] {
			delete(n.items, id)
		}
	}
}

// Tick sends what is due at now: the summaries of the hours that ended, then the items that
// lasted AttentionDelay.
func (n *Notifier) Tick() {
	now := n.o.Now()
	n.mu.Lock()
	defer n.mu.Unlock()
	subs := n.subscribers()
	for user, w := range n.users {
		if now.Before(w.start.Add(time.Hour)) {
			continue
		}
		delete(n.users, user)
		if n.o.MayNotify(user) {
			n.summary(user, w, subs[user])
		}
	}
	var due []*tracked
	for _, t := range n.items {
		if rank(t.item.Severity) > rank(t.pushed) && !now.Before(t.firstSeen.Add(AttentionDelay)) {
			due = append(due, t)
		}
	}
	slices.SortFunc(due, func(a, b *tracked) int {
		return cmp.Or(a.firstSeen.Compare(b.firstSeen), cmp.Compare(a.item.ID, b.item.ID))
	})
	users := make([]string, 0, len(subs))
	for u := range subs {
		users = append(users, u)
	}
	slices.Sort(users)
	for _, t := range due {
		// An escalation goes only to the devices the earlier push skipped: those taking crit.
		escalation := t.pushed != ""
		t.pushed = t.item.Severity
		crit := t.item.Severity == model.SeverityCrit
		for _, user := range users {
			var targets []credentials.PushSubscription
			toWarn, toCrit := false, false
			for _, s := range subs[user] {
				switch {
				case s.Attention == model.PushAttentionWarn && !escalation:
					toWarn = true
				case s.Attention == model.PushAttentionCrit && crit:
					toCrit = true
				default:
					continue
				}
				targets = append(targets, s)
			}
			if len(targets) == 0 || !n.o.MayNotify(user) {
				continue
			}
			w := n.users[user]
			if w == nil {
				w = &hourWindow{start: now}
				n.users[user] = w
			}
			if w.sent >= AttentionHourlyLimit {
				if toWarn {
					w.forWarn++
				}
				if toCrit {
					w.forCrit++
				}
				continue
			}
			w.sent++
			msg := n.message(t.item)
			for _, s := range targets {
				n.o.Service.Attention(s.ID, msg, crit)
			}
		}
	}
}

// subscribers are the verified subscriptions that take attention pushes, by user.
func (n *Notifier) subscribers() map[string][]credentials.PushSubscription {
	out := map[string][]credentials.PushSubscription{}
	for _, s := range n.o.Store.PushSubscriptions("") {
		if s.State == credentials.PushVerified && s.Attention != model.PushAttentionOff {
			out[s.User] = append(out[s.User], s)
		}
	}
	return out
}

func (n *Notifier) message(it model.AttentionItem) model.PushMessage {
	body := it.Detail
	switch {
	case it.Source != "" && body != "":
		body = it.Source + ": " + body
	case it.Source != "":
		body = it.Source
	}
	return model.PushMessage{
		Type: model.PushTypeAttention, ID: it.ID, Title: it.Title, Body: body,
		URL: n.o.Service.URL(cmp.Or(it.Route, "/")),
	}
}

// summary is the push that closes a user's hour: each device hears how many of the items its
// threshold covers did not get their own push.
func (n *Notifier) summary(user string, w *hourWindow, subs []credentials.PushSubscription) {
	devices := 0
	for _, s := range subs {
		count := w.forWarn
		if s.Attention == model.PushAttentionCrit {
			count = w.forCrit
		}
		if count == 0 {
			continue
		}
		n.o.Service.Attention(s.ID, model.PushMessage{
			Type: model.PushTypeAttention, ID: "attention-summary", Title: "dash: " + strconv.Itoa(count) + " more items need attention",
			Body: "Open dash for the whole list.", URL: n.o.Service.URL("/"),
		}, false)
		devices++
	}
	if devices > 0 {
		n.log.Info("push: attention summary", "user", user, "items", max(w.forWarn, w.forCrit), "devices", devices)
	}
}

// Run follows core.overview on h until ctx ends, and ticks every 15 seconds. A subscription
// the hub closes (the notifier fell behind) is opened again; its snapshot is the overview then.
func (n *Notifier) Run(ctx context.Context, h *hub.Hub) {
	tick := time.NewTicker(attentionTick)
	defer tick.Stop()
	want := func(topic string) bool { return topic == string(model.TopicOverview) }
	for {
		snapshot, events, cancel := h.Subscribe("", want)
		for _, ev := range snapshot {
			n.event(ev)
		}
		open := true
		for open {
			select {
			case <-ctx.Done():
				cancel()
				return
			case ev, ok := <-events:
				if !ok {
					open = false
					break
				}
				n.event(ev)
			case <-tick.C:
				n.Tick()
			}
		}
		cancel()
	}
}

// event reads an overview envelope; one that does not parse changes nothing.
func (n *Notifier) event(ev hub.Event) {
	var env model.Envelope
	var data model.OverviewData
	if json.Unmarshal(ev.Data, &env) != nil || json.Unmarshal(env.Data, &data) != nil {
		n.log.Warn("push: an overview event did not parse; attention pushes wait for the next one")
		return
	}
	n.Observe(data.Attention)
}
