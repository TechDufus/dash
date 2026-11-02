// Package queries holds every PromQL expression dash sends to VictoriaMetrics, so the topics
// (instant values) and /api/v1/series (ranges) share one definition.
package queries

import (
	"errors"
	"fmt"
	"regexp"
	"time"
)

// Args are the validated parameters of a /api/v1/series request.
type Args struct {
	VMID      string // digits only
	Instance  string // the node's name (DNS-1123): a series of a plugin resolves it to the instance
	Namespace string // DNS-1123
	Pod       string // DNS-1123
}

// ErrBadSeries is returned for an unknown series name or a missing argument.
var ErrBadSeries = errors.New("bad series request")

var (
	vmidRE = regexp.MustCompile(`^[0-9]{1,9}$`)
	dnsRE  = regexp.MustCompile(`^[a-z0-9]([-a-z0-9.]{0,251}[a-z0-9])?$`)
)

// ValidVMID reports whether s is an acceptable vmid argument.
func ValidVMID(s string) bool { return vmidRE.MatchString(s) }

// ValidDNS reports whether s is an acceptable namespace or pod name argument.
func ValidDNS(s string) bool { return dnsRE.MatchString(s) }

// Series returns the PromQL expression of a named /api/v1/series that dash itself defines. The
// series of the plugins come from their manifests (plugin.SeriesSpec), so none is built in.
func Series(name string, _ Args) (string, error) {
	return "", fmt.Errorf("%w: unknown series %q", ErrBadSeries, name)
}

// Range maps a range name to its window and step.
func Range(name string) (window, step time.Duration, ok bool) {
	switch name {
	case "15m":
		return 15 * time.Minute, 15 * time.Second, true
	case "1h":
		return time.Hour, 30 * time.Second, true
	case "6h":
		return 6 * time.Hour, 2 * time.Minute, true
	case "24h":
		return 24 * time.Hour, 5 * time.Minute, true
	case "7d":
		return 7 * 24 * time.Hour, 30 * time.Minute, true
	}
	return 0, 0, false
}
