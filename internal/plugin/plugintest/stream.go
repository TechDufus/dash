package plugintest

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	"github.com/TechDufus/dash/internal/hub"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
)

// streamDeadline is how long AssertTopicsReachOpenStream waits for the topics after the row
// turned visible.
const streamDeadline = 2 * time.Second

// AssertTopicsReachOpenStream checks visibility rule (a) (design A.9) for one plugin, against the
// real hub.Hub (not Hub): the plugin starts with before, which must leave its row hidden
// (disabled, unavailable, unconfigured or blocked: leave the configuration unset, or point the
// plugin at a fake upstream that makes its Status unavailable), a subscriber opens, and the
// configuration changes to after, which must make the row visible. Every topic the plugin
// declares that has a stored event by then must reach the open subscriber after the core.plugins
// event that made the row visible, within two seconds. "core.plugins first" would be wrong: on
// the restart path the plugin publishes in Start, before its row flips.
//
// before and after are the plugin's instance configuration (Enabled, Config, ActionsEnabled,
// …); Name and Type default to the manifest's ID. A plugin with no topics passes vacuously, so
// a plugin without topics tests that its row turns visible instead.
func AssertTopicsReachOpenStream(t *testing.T, factory plugin.Factory, before, after plugin.InstanceConfig) {
	t.Helper()
	id := factory().Manifest().ID
	for _, ic := range []*plugin.InstanceConfig{&before, &after} {
		if ic.Name == "" {
			ic.Name = id
		}
		if ic.Type == "" {
			ic.Type = ic.Name
		}
	}

	h := hub.New()
	set := &Settings{}
	set.Set(before)
	sup := plugin.NewSupervisor(plugin.Options{
		Publisher:   h,
		Settings:    set,
		StatusEvery: 10 * time.Millisecond,
		StopTimeout: time.Second,
		Log:         slog.New(slog.DiscardHandler),
	})
	if err := sup.Add(factory); err != nil {
		t.Fatalf("Add: %v", err)
	}
	h.SetOrder(sup.TopicOrder)
	ctx, cancel := context.WithCancel(context.Background())
	if err := sup.Run(ctx); err != nil {
		cancel()
		t.Fatalf("Run: %v", err)
	}
	t.Cleanup(func() { cancel(); sup.Shutdown(context.Background()) })

	row := func() model.PluginInfo {
		for _, pi := range sup.Infos() {
			if pi.ID == string(id) {
				return pi
			}
		}
		t.Fatalf("plugin %q is not registered", id)
		return model.PluginInfo{}
	}
	if pi := row(); plugin.Visible(pi.State) {
		t.Fatalf("the configuration before leaves %q %s, which is visible: rule (a) needs a hidden start", id, pi.State)
	}

	_, ch, cancelSub := h.Subscribe("", nil)
	defer cancelSub()

	set.Set(after)
	sup.Reload()

	// visibleAt is set when the core.plugins event that shows the row has been read from ch.
	visibleAt := false
	seen := map[string]bool{}
	deadline := time.NewTimer(10 * streamDeadline) // for the row to flip at all
	defer deadline.Stop()
	var expiry <-chan time.Time
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for {
		if visibleAt {
			if missing := missingTopics(h, row().Topics, seen); len(missing) == 0 {
				return
			}
		}
		select {
		case ev, ok := <-ch:
			if !ok {
				t.Fatalf("the subscriber was dropped before %q's topics arrived", id)
			}
			if ev.Topic == string(model.TopicPlugins) {
				if !visibleAt && rowVisibleIn(t, ev.Data, id) {
					visibleAt = true
					expiry = time.After(streamDeadline)
					clear(seen) // only what arrives after this event counts
				}
				continue
			}
			if visibleAt {
				seen[ev.Topic] = true
			}
		case <-tick.C:
		case <-expiry:
			t.Fatalf("topics %v of %q never reached the open stream after its row turned visible", missingTopics(h, row().Topics, seen), id)
		case <-deadline.C:
			t.Fatalf("%q never turned visible after the configuration changed (state %s)", id, row().State)
		}
	}
}

// missingTopics lists the topics with a stored event that seen does not hold.
func missingTopics(h *hub.Hub, topics []string, seen map[string]bool) []string {
	var out []string
	for _, topic := range topics {
		if _, stored := h.Latest("", model.Topic(topic)); stored && !seen[topic] {
			out = append(out, topic)
		}
	}
	return out
}

// rowVisibleIn reports whether the core.plugins envelope body shows id's row in a visible state.
func rowVisibleIn(t *testing.T, body []byte, id plugin.ID) bool {
	t.Helper()
	var env model.Envelope
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("core.plugins envelope: %v", err)
	}
	var data model.PluginsData
	if err := json.Unmarshal(env.Data, &data); err != nil {
		t.Fatalf("core.plugins payload: %v", err)
	}
	for _, pi := range data.Plugins {
		if pi.ID == string(id) {
			return plugin.Visible(pi.State)
		}
	}
	return false
}
