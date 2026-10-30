package topics

import (
	"context"
	"sort"
	"strconv"
	"sync"

	"github.com/TechDufus/dash/internal/upstream"
)

type qres struct {
	samples []upstream.Sample
	err     error
}

// queryAll runs the named instant queries concurrently.
func (m *Manager) queryAll(ctx context.Context, qs map[string]string) map[string]qres {
	out := make(map[string]qres, len(qs))
	var mu sync.Mutex
	var wg sync.WaitGroup
	for name, expr := range qs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s, err := m.d.Prom.Instant(ctx, expr)
			mu.Lock()
			out[name] = qres{s, err}
			mu.Unlock()
		}()
	}
	wg.Wait()
	return out
}

// firstErr returns the error of the alphabetically first failed query, or nil.
func firstErr(res map[string]qres) error {
	names := make([]string, 0, len(res))
	for n, r := range res {
		if r.err != nil {
			names = append(names, n)
		}
	}
	if len(names) == 0 {
		return nil
	}
	sort.Strings(names)
	return res[names[0]].err
}

func first(s []upstream.Sample) (float64, bool) {
	if len(s) == 0 {
		return 0, false
	}
	return s[0].Value, true
}

func sumValues(s []upstream.Sample) float64 {
	var t float64
	for _, x := range s {
		t += x.Value
	}
	return t
}

// byLabel indexes sample values by one label (later samples of the same label win).
func byLabel(s []upstream.Sample, label string) map[string]float64 {
	out := make(map[string]float64, len(s))
	for _, x := range s {
		out[x.Labels[label]] = x.Value
	}
	return out
}

// byLabels indexes sample values by "<a>/<b>".
func byLabels(s []upstream.Sample, a, b string) map[string]float64 {
	out := make(map[string]float64, len(s))
	for _, x := range s {
		out[x.Labels[a]+"/"+x.Labels[b]] = x.Value
	}
	return out
}

func atoi(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}
