package overview

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/TechDufus/dash/internal/hub"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
)

// eventually polls cond until it holds or five seconds pass.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// latest is the decoded envelope of topic, or ok=false.
func latest(h *hub.Hub, topic model.Topic) (model.Envelope, bool) {
	ev, ok := h.Latest("", topic)
	if !ok {
		return model.Envelope{}, false
	}
	var env model.Envelope
	if json.Unmarshal(ev.Data, &env) != nil {
		return model.Envelope{}, false
	}
	return env, true
}

func mustEnvelope(t *testing.T, h *hub.Hub, topic model.Topic) model.Envelope {
	t.Helper()
	env, ok := latest(h, topic)
	if !ok {
		t.Fatalf("%s is not published", topic)
	}
	return env
}

// testPublisherWith is a Publisher over the given hosts and the plugins that contribute to the
// overview (nil: none does).
func testPublisherWith(hosts plugin.HostProvider, contributors func() []plugin.Contributor) *Publisher {
	return New(Deps{Hosts: hosts, Contributors: contributors})
}
