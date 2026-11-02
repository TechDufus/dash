package api

import (
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/TechDufus/dash/internal/model"
)

// maxStreamTopics bounds the ?topics= filter of the event stream.
const maxStreamTopics = 32

// topicNameRE is a topic's SSE name: "<instance>.<topic>", as every plugin and core topic is.
var topicNameRE = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}\.[a-z][a-z0-9_]{0,31}$`)

// topic serves the stored envelope of one topic, for clients that poll instead of holding the
// stream. The ETag is the hub's content digest, so If-None-Match answers 304 until the data or a
// source's state changes.
func (s *Server) topic(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("topic")
	if !topicNameRE.MatchString(name) {
		writeErr(w, http.StatusBadRequest, model.ErrCodeInvalidInput, "invalid topic name")
		return
	}
	// Another user's scoped copy is invisible: Latest only returns the caller's own.
	ev, ok := s.d.Hub.Latest(userFrom(r.Context()), model.Topic(name))
	if !ok {
		writeErr(w, http.StatusNotFound, model.ErrCodeNotFound, "not found")
		return
	}
	h := w.Header()
	h.Set("ETag", `"`+ev.ETag+`"`)
	h.Set("Cache-Control", "private, no-cache")
	if etagMatches(r.Header.Get("If-None-Match"), ev.ETag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	h.Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(ev.Data)
}

// etagMatches implements If-None-Match's weak comparison (RFC 9110 §13.1.2): a comma-separated
// list of entity tags, optionally weak ("W/"), or "*".
func etagMatches(header, etag string) bool {
	for _, tag := range strings.Split(header, ",") {
		tag = strings.TrimSpace(tag)
		if tag == "*" {
			return true
		}
		tag = strings.TrimPrefix(tag, "W/")
		if len(tag) >= 2 && tag[0] == '"' && tag[len(tag)-1] == '"' && tag[1:len(tag)-1] == etag {
			return true
		}
	}
	return false
}

// topicFilter parses the stream's ?topics=a,b value into a Hub.Subscribe filter. Empty means every
// topic (nil). Names that are valid but unknown are fine: the plugin behind one may appear later.
func topicFilter(raw string) (func(topic string) bool, error) {
	if raw == "" {
		return nil, nil
	}
	names := strings.Split(raw, ",")
	if len(names) > maxStreamTopics {
		return nil, fmt.Errorf("at most %d topics", maxStreamTopics)
	}
	want := make(map[string]struct{}, len(names))
	for _, n := range names {
		if !topicNameRE.MatchString(n) {
			return nil, errors.New("topics must be comma-separated topic names")
		}
		want[n] = struct{}{}
	}
	return func(topic string) bool { _, ok := want[topic]; return ok }, nil
}
