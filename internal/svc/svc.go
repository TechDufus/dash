// Package svc holds the interfaces plugins export to each other and to the core's own code (design
// A.5). They live apart from every plugin so that a consumer never imports a producer: a provider
// implements one of them in its plugin.Provider.Service, a consumer asks for it through
// Host.Service (a plugin) or Supervisor.Service (code that is not a plugin) and gets nothing when
// the provider is not running, which it reports as a source that is not configured.
package svc

import (
	"context"
	"time"

	"github.com/TechDufus/dash/internal/model"
)

// Sample is one instant-vector element of a Prometheus-compatible query.
type Sample struct {
	Labels map[string]string
	Value  float64
}

// Querier is what the prometheus plugin exports: instant and range queries against the metrics
// backend. Errors keep the upstream package's categories (unreachable, timeout, tls, auth,
// http_<code>, invalid_response).
type Querier interface {
	// Instant runs an instant query; samples whose value is NaN or Inf are dropped.
	Instant(ctx context.Context, expr string) ([]Sample, error)
	// Range runs a range query; NaN and Inf points are skipped.
	Range(ctx context.Context, expr string, start, end time.Time, step time.Duration) ([]model.SeriesLine, error)
}

// EndpointHealth is what the gatus plugin exports: the health of the checked endpoints, for the
// code that decorates its own objects with it (the apps list, machines with a gatusEndpoint).
type EndpointHealth interface {
	// ByHost is the health of the endpoint whose URL has this (lowercased) host name; the last
	// endpoint wins when several share it. known is false when none matches.
	ByHost(host string) (state model.Health, known bool)
	// ByName reports whether the endpoint called name (compared without regard to case) is up.
	ByName(name string) (up bool, known bool)
	// Revision is 0 until the first poll has finished, success or failure, and then increases
	// whenever the endpoint list, a health value or the source status changed. It never
	// decreases or repeats across service instances (a reconfigured plugin builds a new one), so a
	// consumer that polls it on its own tick, and republishes when it differs from the value it
	// last saw, cannot miss a change.
	Revision() uint64
	// SourceStatuses are the plugin's sources, for the consumer to append to its envelope so the
	// gatus chip stays on the pages that show its data.
	SourceStatuses() []model.SourceStatus
}
