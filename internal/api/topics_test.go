package api

import (
	"encoding/json"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/TechDufus/dash/internal/model"
)

func TestTopicSnapshotETagAndNotModified(t *testing.T) {
	s := newServer(nil)
	h := s.Handler()
	s.d.Hub.Publish("core.things", map[string]int{"n": 1}, nil)

	get := func(inm string) (code int, etag, body string, hdr http.Header) {
		t.Helper()
		r := req{path: "/api/v1/topics/core.things"}
		if inm != "" {
			r.header = map[string]string{"If-None-Match": inm}
		}
		w := r.do(h)
		return w.Code, w.Header().Get("ETag"), w.Body.String(), w.Header()
	}

	code, etag, body, hdr := get("")
	if code != 200 {
		t.Fatalf("status = %d: %s", code, body)
	}
	if len(etag) != 18 || etag[0] != '"' || etag[17] != '"' {
		t.Fatalf(`ETag = %s, want "<16 hex>"`, etag)
	}
	if cc := hdr.Get("Cache-Control"); cc != "private, no-cache" {
		t.Errorf("Cache-Control = %q", cc)
	}
	if ct := hdr.Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q", ct)
	}
	var env model.Envelope
	if err := json.Unmarshal([]byte(body), &env); err != nil || env.Topic != "core.things" || string(env.Data) != `{"n":1}` {
		t.Fatalf("body = %s (%v)", body, err)
	}
	tag := etag[1:17]
	if ev, _ := s.d.Hub.Latest("alice", "core.things"); ev.ETag != tag {
		t.Errorf("served tag %s differs from the hub's %s", tag, ev.ETag)
	}

	// Every If-None-Match form that names the current tag, or "*", is a 304 with no body.
	for _, inm := range []string{etag, "W/" + etag, `"zzz", ` + etag, `W/"zzz",W/` + etag, "*"} {
		code, got, body, hdr := get(inm)
		if code != http.StatusNotModified || body != "" {
			t.Errorf("If-None-Match %s: status %d body %q, want 304 and none", inm, code, body)
		}
		if got != etag || hdr.Get("Cache-Control") != "private, no-cache" {
			t.Errorf("If-None-Match %s: 304 lost its validators: ETag %q, Cache-Control %q", inm, got, hdr.Get("Cache-Control"))
		}
	}
	// Tags that do not name it are a full response.
	for _, inm := range []string{`"0000000000000000"`, tag, `W/"` + tag + `x"`, `"zzz"`} {
		if code, got, _, _ := get(inm); code != 200 || got != etag {
			t.Errorf("If-None-Match %s: status %d ETag %s, want 200 %s", inm, code, got, etag)
		}
	}

	// Same content: still a hit. A change: a new tag and a body again.
	s.d.Hub.Publish("core.things", map[string]int{"n": 1}, nil)
	if code, _, _, _ := get(etag); code != http.StatusNotModified {
		t.Errorf("unchanged republish: status %d, want 304", code)
	}
	s.d.Hub.Publish("core.things", map[string]int{"n": 2}, nil)
	code, next, body, _ := get(etag)
	if code != 200 || next == etag || !strings.Contains(body, `"n":2`) {
		t.Errorf("after a change: status %d ETag %s (was %s) body %s", code, next, etag, body)
	}
	// The new tag works in turn.
	if code, _, _, _ := get(next); code != http.StatusNotModified {
		t.Errorf("new tag: status %d, want 304", code)
	}
}

func TestTopicSnapshotUnknownIs404AndBadNameIs400(t *testing.T) {
	s := newServer(nil)
	h := s.Handler()
	s.d.Hub.Publish("cluster", 1, nil) // a legacy name has no dot
	s.d.Hub.PublishFor("alice", "core.approvals", "mine", nil)

	for path, want := range map[string]int{
		"/api/v1/topics/cluster":                      200,
		"/api/v1/topics/nope":                         404, // valid name, nothing published
		"/api/v1/topics/core.nope":                    404,
		"/api/v1/topics/core.approvals":               200, // alice's own scoped topic
		"/api/v1/topics/Core":                         400,
		"/api/v1/topics/core.":                        400,
		"/api/v1/topics/.core":                        400,
		"/api/v1/topics/a.b.c":                        400,
		"/api/v1/topics/1abc":                         400,
		"/api/v1/topics/a-b":                          400,
		"/api/v1/topics/a%20b":                        400,
		"/api/v1/topics/" + strings.Repeat("a", 33):   400,
		"/api/v1/topics/a." + strings.Repeat("b", 33): 400,
		"/api/v1/topics/" + strings.Repeat("a", 32) + "." + strings.Repeat("b", 32): 404, // longest valid name
	} {
		w := req{path: path}.do(h)
		if w.Code != want {
			t.Errorf("GET %s = %d, want %d (%s)", path, w.Code, want, w.Body.String())
			continue
		}
		if want >= 400 && decodeErr(t, w) == "" {
			t.Errorf("GET %s: no error message in %s", path, w.Body.String())
		}
	}

	// Another user's scoped topic does not exist for bob: 404, not 403, and no tag leaks.
	w := req{path: "/api/v1/topics/core.approvals", header: map[string]string{"X-Forwarded-Preferred-Username": "bob"}}.do(h)
	if w.Code != http.StatusNotFound || w.Header().Get("ETag") != "" || strings.Contains(w.Body.String(), "mine") {
		t.Errorf("bob: %d %q ETag=%q", w.Code, w.Body.String(), w.Header().Get("ETag"))
	}
	// A removed topic is gone.
	s.d.Hub.Remove("cluster")
	if w := (req{path: "/api/v1/topics/cluster"}).do(h); w.Code != http.StatusNotFound {
		t.Errorf("removed topic = %d, want 404", w.Code)
	}
	// Reads only.
	if w := (req{method: http.MethodPost, path: "/api/v1/topics/core.approvals"}).do(h); w.Code == 200 {
		t.Errorf("POST was served")
	}
}

// eventNames returns the event names from SSE lines until n of them were seen.
func eventNames(t *testing.T, lines <-chan string, n int) []string {
	t.Helper()
	var got []string
	timeout := time.After(3 * time.Second)
	for len(got) < n {
		select {
		case l, ok := <-lines:
			if !ok {
				t.Fatalf("stream ended after %v, want %d events", got, n)
			}
			if name, ok := strings.CutPrefix(l, "event: "); ok {
				got = append(got, name)
			}
		case <-timeout:
			t.Fatalf("timed out after %v, want %d events", got, n)
		}
	}
	return got
}

func TestStreamTopicsFilter(t *testing.T) {
	s := newServer(nil)
	s.heartbeat = time.Hour
	for _, name := range []model.Topic{"a", "b", "core.c"} {
		s.d.Hub.Publish(name, 1, nil)
	}
	_, lines := sseClient(t, s.Handler(), "/api/v1/stream?topics=b,core.c,later")
	if got := eventNames(t, lines, 2); !slices.Equal(got, []string{"b", "core.c"}) {
		t.Fatalf("snapshot = %v, want only the listed topics, in name order", got)
	}
	// "a" is filtered, "later" was unknown at connect time and still arrives once published.
	s.d.Hub.Publish("a", 2, nil)
	s.d.Hub.Publish("later", 1, nil)
	s.d.Hub.Publish("b", 2, nil)
	if got := eventNames(t, lines, 2); !slices.Equal(got, []string{"later", "b"}) {
		t.Errorf("live = %v, want [later b]", got)
	}
}

func TestStreamStillReplaysFullStateOnConnect(t *testing.T) {
	for _, path := range []string{"/api/v1/stream", "/api/v1/stream?topics="} {
		s := newServer(nil)
		s.heartbeat = time.Hour
		s.d.Hub.Publish("zeta", 1, nil)
		s.d.Hub.Publish("alpha", 1, nil)
		s.d.Hub.Publish("core.plugins", 1, nil)
		s.d.Hub.SetOrder(func() []model.Topic { return []model.Topic{"core.plugins", "zeta"} })
		_, lines := sseClient(t, s.Handler(), path)
		if got := eventNames(t, lines, 3); !slices.Equal(got, []string{"core.plugins", "zeta", "alpha"}) {
			t.Errorf("%s: snapshot = %v, want every topic in registry order", path, got)
		}
		s.d.Hub.Publish("alpha", 2, nil)
		if got := eventNames(t, lines, 1); !slices.Equal(got, []string{"alpha"}) {
			t.Errorf("%s: live = %v", path, got)
		}
	}
}

func TestStreamSendsUserScopedTopicsOnlyToTheirOwner(t *testing.T) {
	s := newServer(nil)
	s.heartbeat = time.Hour
	s.d.Hub.PublishFor("alice", "core.approvals", "a-secret", nil)
	s.d.Hub.PublishFor("bob", "core.approvals", "b-secret", nil)
	s.d.Hub.Publish("shared", 1, nil)
	h := s.Handler()

	for _, tc := range []struct{ user, own, other, otherUser string }{
		{"alice", "a-secret", "b-secret", "bob"},
		{"bob", "b-secret", "a-secret", "alice"},
	} {
		_, lines := sseClientWith(t, h, "/api/v1/stream", map[string]string{"X-Forwarded-Preferred-Username": tc.user})
		// Snapshot: the user's own copy and the shared topic, never the other user's.
		var got []string
		var sawOwn bool
		collect := func(n int) {
			t.Helper()
			timeout := time.After(3 * time.Second)
			for len(got) < n {
				select {
				case l := <-lines:
					if strings.Contains(l, tc.other) {
						t.Fatalf("%s received another user's event: %s", tc.user, l)
					}
					if name, ok := strings.CutPrefix(l, "event: "); ok {
						got = append(got, name)
					}
					sawOwn = sawOwn || strings.Contains(l, tc.own)
				case <-timeout:
					t.Fatalf("%s: timed out after %v", tc.user, got)
				}
			}
		}
		collect(2)
		if !sawOwn {
			t.Errorf("%s: snapshot lacks the user's own copy", tc.user)
		}
		// Live: the other user's update comes first; if it leaked it would show before the marker.
		s.d.Hub.PublishFor(tc.otherUser, "core.approvals", tc.other+"-x", nil)
		s.d.Hub.PublishFor(tc.user, "core.approvals", tc.own+"-live", nil)
		s.d.Hub.Publish(model.Topic("marker_"+tc.user), 1, nil)
		collect(4)
		if want := []string{"core.approvals", "shared", "core.approvals", "marker_" + tc.user}; !slices.Equal(got, want) {
			t.Errorf("%s: events %v, want %v", tc.user, got, want)
		}
		s.d.Hub.Remove(model.Topic("marker_" + tc.user))
	}
}

func TestStreamRejectsBadTopicsFilter(t *testing.T) {
	s := newServer(nil)
	h := s.Handler()
	var tooMany []string
	for i := range maxStreamTopics + 1 {
		tooMany = append(tooMany, "t"+strconv.Itoa(i))
	}
	for name, q := range map[string]string{
		"uppercase":  "A",
		"empty item": "a,,b",
		"trailing":   "a,",
		"space":      "a, b",
		"nested":     "a.b.c",
		"too many":   strings.Join(tooMany, ","),
	} {
		w := req{path: "/api/v1/stream?topics=" + strings.ReplaceAll(q, " ", "%20")}.do(h)
		if w.Code != http.StatusBadRequest || decodeErr(t, w) == "" {
			t.Errorf("%s: status %d body %s, want 400 with an error", name, w.Code, w.Body.String())
		}
	}
	// The limit is inclusive: exactly 32 is fine (checked on the parser, the stream never ends).
	if want, err := topicFilter(strings.Join(tooMany[:maxStreamTopics], ",")); err != nil || !want("t31") || want("t32") {
		t.Errorf("32 topics: err=%v", err)
	}
	// A rejected request holds no stream slot.
	if got := s.events.total; got != 0 {
		t.Errorf("open streams after rejects = %d", got)
	}
}
