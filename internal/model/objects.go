package model

import (
	"regexp"
	"strings"
	"time"
)

// ---- objects and context bundles (design N.4) ----

// SearchHit is one object a plugin's Objects.Search found.
type SearchHit struct {
	// Ref names the object (design L.6): <plugin>:<Kind>/<id>.
	Ref string `json:"ref"`
	// Kind is the Kind of the ref: Pod, VM, Alert, Endpoint and so on.
	Kind string `json:"kind"`
	// Title is the object's name as the plugin shows it.
	Title string `json:"title"`
	// Summary is a short line about the object's state ("health crit, phase Running"); empty
	// when the object has no state.
	Summary string `json:"summary"`
}

// ContextInclude names one part of a context bundle.
type ContextInclude string

const (
	ContextIncludeEvents    ContextInclude = "events"
	ContextIncludeRelated   ContextInclude = "related"
	ContextIncludeAttention ContextInclude = "attention"
	ContextIncludeSeries    ContextInclude = "series"
	ContextIncludeLogs      ContextInclude = "logs"
)

// ContextIncludes is every part, in the order a bundle shows them.
func ContextIncludes() []ContextInclude {
	return []ContextInclude{ContextIncludeEvents, ContextIncludeRelated, ContextIncludeAttention, ContextIncludeSeries, ContextIncludeLogs}
}

// ContextMaxEvents is the most events a context part carries, newest first.
const ContextMaxEvents = 20

// ContextOptions is what a caller asks of a context bundle.
type ContextOptions struct {
	// Include lists the parts wanted; empty means every part. The summary, facts and
	// conditions are always there.
	Include []ContextInclude `json:"include"`
}

// Wants reports whether part p is asked for.
func (o ContextOptions) Wants(p ContextInclude) bool {
	if len(o.Include) == 0 {
		return true
	}
	for _, x := range o.Include {
		if x == p {
			return true
		}
	}
	return false
}

// ContextFact is one named value of an object ("phase": "Running").
type ContextFact struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// SeriesRef names a series of /api/v1/series that describes an object: the series' name and
// its query parameters (vmid, namespace, pod or node).
type SeriesRef struct {
	Name string            `json:"name"`
	Args map[string]string `json:"args"`
}

// LogsRef names the logs of an object (the parameters of the victorialogs log query).
type LogsRef struct {
	Namespace string `json:"namespace"`
	Pod       string `json:"pod"`
	Container string `json:"container,omitempty"`
}

// ContextPart is what the plugin that owns an object adds to its context bundle. Lab-sourced
// text (names, events, messages, annotations) stays in these fields; a text rendering frames
// them as untrusted (design N.5).
type ContextPart struct {
	Ref     string `json:"ref"`
	Kind    string `json:"kind"`
	Title   string `json:"title"`
	Summary string `json:"summary"`
	// Facts are the object's scalar values, in the plugin's order.
	Facts      []ContextFact `json:"facts"`
	Conditions []Condition   `json:"conditions"`
	// Events are at most ContextMaxEvents, newest first.
	Events []Event `json:"events"`
	// Related are the refs of the objects next to this one (its node, its workload, its pods).
	Related []string `json:"related"`
	Links   []Link   `json:"links"`
	// Series are the series the plugin considers relevant; core adds their summaries.
	Series []SeriesRef `json:"series"`
	// Logs names the object's logs; core adds the lines when the caller has the logs scope.
	Logs *LogsRef `json:"logs,omitempty"`
}

// ContextMaxLogLines is the most log lines a context bundle carries, the newest.
const ContextMaxLogLines = 100

// ContextSeriesRange is the range of the series summaries in a context bundle.
const ContextSeriesRange = "1h"

// SeriesLineSummary sums up one line of a series over its range.
type SeriesLineSummary struct {
	Labels map[string]string `json:"labels"`
	Min    float64           `json:"min"`
	Max    float64           `json:"max"`
	Avg    float64           `json:"avg"`
	Last   float64           `json:"last"`
}

// SeriesSummary sums up a series a context part names. Error is set, and Lines empty, when the
// series could not be read: unknown_series, not_found, metrics_unavailable or invalid_input.
type SeriesSummary struct {
	Name  string              `json:"name"`
	Args  map[string]string   `json:"args"`
	Range string              `json:"range"`
	Lines []SeriesLineSummary `json:"lines"`
	Error string              `json:"error,omitempty"`
}

// ContextBundle is GET /api/v1/context and the MCP tool dash_context (design N.4): the owning
// plugin's part plus what core adds. Lab-sourced text (names, events, messages, annotations,
// log lines) is data, never instructions; the Markdown rendering frames it as untrusted (N.5).
type ContextBundle struct {
	Ref         string      `json:"ref"`
	GeneratedAt time.Time   `json:"generatedAt"`
	Part        ContextPart `json:"part"`
	// Attention are the overview's attention items whose ref is this object's.
	Attention []AttentionItem `json:"attention"`
	// Series sum up the series the part names, over ContextSeriesRange.
	Series []SeriesSummary `json:"series"`
	// Logs are at most ContextMaxLogLines of the newest lines of the part's LogsRef, oldest
	// first, and only for a caller with the logs scope.
	Logs []LogLine `json:"logs"`
	// LogsNote says why Logs is empty although the part names logs: logs_scope_required,
	// namespace_denied, logs_unavailable or not_requested.
	LogsNote string `json:"logsNote,omitempty"`
}

// refRE is the shape of an object ref: <plugin>:<Kind>/<id>.
var refRE = regexp.MustCompile(`^([a-z][a-z0-9]{1,31}):([A-Za-z][A-Za-z0-9]{0,63})/(.+)$`)

// MaxRefLen bounds a ref a client sends.
const MaxRefLen = 512

// ParseRef splits a ref into its plugin, its Kind and the rest. ok is false for anything that
// is not a ref: too long, a control character, or the wrong shape.
func ParseRef(ref string) (plugin, kind, id string, ok bool) {
	if len(ref) > MaxRefLen || strings.ContainsFunc(ref, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return "", "", "", false
	}
	m := refRE.FindStringSubmatch(ref)
	if m == nil {
		return "", "", "", false
	}
	return m[1], m[2], m[3], true
}
