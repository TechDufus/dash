package victorialogs

import (
	"net/url"
	"sort"
	"strings"
	"time"
)

// maxResumePoints bounds the containers one log tail remembers (and so the SSE id size).
const maxResumePoints = 16

// resumePoints maps a container name to the time of the newest line delivered from it.
// VictoriaLogs keeps lines in order only within one stream (one per container), so a resumed
// tail needs a point per container rather than one timestamp for the pod.
//
// The wire form, used as the SSE id and echoed back as Last-Event-ID, is the entries sorted by
// container name, each `<url.QueryEscape(container)>=<RFC3339Nano UTC>`, joined by ",". The
// container may be empty.
type resumePoints map[string]time.Time

// parseResumePoints decodes an id written by String. Anything malformed, including an empty
// id, yields ok=false: the caller then starts a plain tail.
func parseResumePoints(id string) (p resumePoints, ok bool) {
	if id == "" {
		return nil, false
	}
	// SplitN stops one piece past the cap, so a header full of commas cannot force a large split.
	entries := strings.SplitN(id, ",", maxResumePoints+1)
	if len(entries) > maxResumePoints {
		return nil, false
	}
	p = make(resumePoints, len(entries))
	for _, e := range entries {
		k, v, found := strings.Cut(e, "=")
		if !found {
			return nil, false
		}
		name, err := url.QueryUnescape(k)
		if err != nil {
			return nil, false
		}
		t, err := time.Parse(time.RFC3339Nano, v)
		if err != nil {
			return nil, false
		}
		if _, dup := p[name]; dup {
			return nil, false
		}
		p[name] = t
	}
	return p, true
}

// String renders p as an SSE id.
func (p resumePoints) String() string {
	names := make([]string, 0, len(p))
	for n := range p {
		names = append(names, n)
	}
	sort.Strings(names)
	var b strings.Builder
	for i, n := range names {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(url.QueryEscape(n))
		b.WriteByte('=')
		b.WriteString(p[n].UTC().Format(time.RFC3339Nano))
	}
	return b.String()
}

func (p resumePoints) clone() resumePoints {
	c := make(resumePoints, len(p))
	for k, v := range p {
		c[k] = v
	}
	return c
}

// oldest is the earliest point; the zero time for an empty set.
func (p resumePoints) oldest() time.Time {
	var out time.Time
	for _, t := range p {
		if out.IsZero() || t.Before(out) {
			out = t
		}
	}
	return out
}

// note records a line from container at t, keeping the newest time per container. Past
// maxResumePoints entries it evicts the oldest, so that container may repeat lines on a
// resume but never loses any.
func (p resumePoints) note(container string, t time.Time) {
	if cur, ok := p[container]; ok && !t.After(cur) {
		return
	}
	p[container] = t
	if len(p) <= maxResumePoints {
		return
	}
	victim, vt := "", time.Time{}
	for n, pt := range p {
		if vt.IsZero() || pt.Before(vt) || (pt.Equal(vt) && n < victim) {
			victim, vt = n, pt
		}
	}
	delete(p, victim)
}
