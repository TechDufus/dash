package api

import (
	"context"
	"testing"
	"time"

	"github.com/TechDufus/dash/internal/model"
)

// When the hub drops the MCP server's subscriber for falling behind, Changes subscribes again
// and reports every topic once, since any may have changed in the gap: a topic published only
// before the drop comes through again.
func TestMCPChangesReplayEveryTopicAfterTheHubDropsIt(t *testing.T) {
	s := newServer(nil)
	if _, err := s.d.Hub.Publish(model.Topic("x.quiet"), 1, nil); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := mcpBackend{s}.Changes(ctx)
	// Subscribed once a change comes through.
	for n, subscribed := -1, false; !subscribed; n-- {
		if _, err := s.d.Hub.Publish(model.Topic("x.busy"), n, nil); err != nil {
			t.Fatal(err)
		}
		select {
		case topic := <-ch:
			subscribed = topic == "x.busy"
		case <-time.After(20 * time.Millisecond):
		}
	}
	// Nobody reads: Changes' 64 and the hub's 32 fill up, and the hub drops the subscriber.
	for i := range 200 {
		if _, err := s.d.Hub.Publish(model.Topic("x.busy"), i, nil); err != nil {
			t.Fatal(err)
		}
	}
	timeout := time.After(5 * time.Second)
	for {
		select {
		case topic := <-ch:
			if topic == "x.quiet" {
				return
			}
		case <-timeout:
			t.Fatal("x.quiet was never reported again after the hub dropped the subscriber")
		}
	}
}
