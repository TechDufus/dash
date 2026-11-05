package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/TechDufus/dash/internal/api"
	"github.com/TechDufus/dash/internal/auth"
	"github.com/TechDufus/dash/internal/config"
	"github.com/TechDufus/dash/internal/model"
)

// leakClock is the demo clock of the leak test. The test runs in a synctest bubble, whose clock
// starts at 2000-01-01T00:00:00Z, so the wall clock the demo must not read is decades from it
// on every day the test runs.
const leakClock = "2026-01-15T12:00:00Z"

// leakWindow is how close to the wall clock a stamp has to be to count as read from it. A month
// also catches a stamp derived from the wall clock (a week ago, a day ahead), and leaves the demo
// world, whose stamps lie within three months of its clock, decades clear.
const leakWindow = 30 * 24 * time.Hour

// demoPayloads is everything a demo run serves that carries data, by request: every topic's
// envelope, every series of every manifest over every range, the plugin list, me and the
// settings answer, the detail of every pod and VM, and the logs of every pod. A request that
// should answer and does not fails the test, so a route that stops answering cannot hide a leak.
func demoPayloads(t *testing.T, fw *framework, cfg config.Config) map[string][]byte {
	t.Helper()
	h := api.New(api.Deps{
		Cfg: cfg, Hub: fw.hub, Plugins: fw.sup, Health: fw.sup,
		Auth: auth.Fixed(auth.Principal{Kind: auth.KindDemo, User: "demo", Role: auth.RoleAdmin}),
	}).Handler()
	out := map[string][]byte{}
	get := func(path string, must bool) []byte {
		t.Helper()
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, cfg.PublicURL+path, nil))
		if must && rec.Code != http.StatusOK {
			t.Fatalf("GET %s = %d %s", path, rec.Code, rec.Body)
		}
		out[path] = rec.Body.Bytes()
		return rec.Body.Bytes()
	}

	for _, topic := range demoTopics {
		get("/api/v1/topics/"+topic, true)
	}
	get("/api/v1/plugins", true)
	get("/api/v1/me", true)
	get("/api/v1/settings", false) // demo mode has no settings API; whatever it answers is scanned

	var cluster model.ClusterData
	topicData(t, fw, model.TopicCluster, &cluster)
	var vms model.VMsData
	topicData(t, fw, model.TopicVMs, &vms)
	if len(cluster.Pods) == 0 || len(cluster.Nodes) == 0 || len(vms.VMs) == 0 {
		t.Fatalf("the demo world has %d pods, %d nodes and %d VMs; the scan needs one of each", len(cluster.Pods), len(cluster.Nodes), len(vms.VMs))
	}
	for _, p := range cluster.Pods {
		get("/api/v1/plugins/kubernetes/pods/"+url.PathEscape(p.Namespace)+"/"+url.PathEscape(p.Name), true)
		get("/api/v1/plugins/victorialogs/logs?"+url.Values{"ns": {p.Namespace}, "pod": {p.Name}}.Encode(), true)
	}
	for _, vm := range vms.VMs {
		get("/api/v1/plugins/proxmox/vms/"+strconv.Itoa(vm.VMID), true)
	}

	pod, node, vmid := cluster.Pods[0], cluster.Nodes[0].Name, strconv.Itoa(vms.VMs[0].VMID)
	series := 0
	for _, m := range fw.sup.Manifests() {
		for _, s := range m.Series {
			q := url.Values{"name": {s.Name}, "vmid": {vmid}, "ns": {pod.Namespace}, "pod": {pod.Name}, "node": {node}}
			for _, rng := range []string{"15m", "1h", "6h", "24h", "7d"} {
				q.Set("range", rng)
				get("/api/v1/series?"+q.Encode(), true)
				series++
			}
		}
	}
	if series == 0 {
		t.Fatal("no series to scan")
	}
	return out
}

// textStamp finds dates, alone or with a time of day, anywhere in a payload: a JSON string of
// its own or a stamp inside a message. It reads RFC 3339 and the forms near it: a space for the
// T, no seconds, a comma before the fraction, no zone, a zone without a colon (time.Time's own
// String form among them). Nothing has to follow it, so a date running into a T or a letter is
// found too.
var textStamp = regexp.MustCompile(`(?:^|\D)(\d{4})-(\d{2})-(\d{2})(?:[T ](\d{2}):(\d{2})(?::(\d{2})(?:[.,](\d{1,9})\d*)?)?(?: ?(Z|[+-]\d{2}(?::?\d{2})?))?)?`)

// textTime is the time a textStamp match names. A missing part is zero; a time with no zone is
// read as UTC, at most 14 hours off, well inside leakWindow.
func textTime(m [][]byte) time.Time {
	num := func(b []byte) int { n, _ := strconv.Atoi(string(b)); return n }
	frac := string(m[7]) + "000000000"
	t := time.Date(num(m[1]), time.Month(num(m[2])), num(m[3]), num(m[4]), num(m[5]), num(m[6]), num([]byte(frac[:9])), time.UTC)
	if z := strings.ReplaceAll(string(m[8]), ":", ""); len(z) > 1 {
		off := time.Duration(num([]byte(z[1:3])))*time.Hour + time.Duration(num([]byte(z[3:])))*time.Minute
		if z[0] == '-' {
			off = -off
		}
		t = t.Add(-off)
	}
	return t
}

// unixUnits are the units a number in a time's place is read in.
var unixUnits = []struct {
	name string
	per  time.Duration
}{{"seconds", time.Second}, {"milliseconds", time.Millisecond}, {"microseconds", time.Microsecond}, {"nanoseconds", time.Nanosecond}}

// wallStamps lists every time in body that lies within leakWindow of now: dates and times in
// the text (textStamp), and numbers in a time's place (timeNumbers), whole or fractional, JSON
// numbers or numerals in strings, that read as a unix time in any of unixUnits.
func wallStamps(body []byte, now time.Time) []string {
	var out []string
	for _, m := range textStamp.FindAllSubmatch(body, -1) {
		if near(textTime(m), now) {
			out = append(out, strings.TrimLeftFunc(string(m[0]), func(r rune) bool { return r < '0' || r > '9' }))
		}
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var v any
	if dec.Decode(&v) != nil {
		return out // not JSON: the text scan above is all there is
	}
	timeNumbers(v, "", func(s string) {
		f, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return
		}
		for _, u := range unixUnits {
			if at := f * float64(u.per); math.Abs(at-float64(now.UnixNano())) < float64(leakWindow) {
				out = append(out, fmt.Sprintf("%s (unix %s: %s)", s, u.name, time.Unix(0, int64(at)).UTC().Format(time.RFC3339Nano)))
				return
			}
		}
	})
	return out
}

func near(t, now time.Time) bool { return t.Sub(now).Abs() < leakWindow }

// timeKey is an object key whose number would be a time: ts, time, expires, createdAt, lastSeen,
// notAfter, lastScrubEnd.
var timeKey = regexp.MustCompile(`^(ts|at|time|timestamp|date|epoch|since|until|start|end|expires|expiry|deadline)$|(At|Time|Timestamp|Date|Epoch|Since|Until|Seen|Start|End|After|Before|Expires|Expiry)$`)

// numeral is a number written in a JSON number or a string.
var numeral = regexp.MustCompile(`^-?\d+(?:\.\d+)?(?:[eE][+-]?\d+)?$`)

// numeralOf is v as a numeral, when it is one.
func numeralOf(v any) (string, bool) {
	switch x := v.(type) {
	case json.Number:
		return x.String(), true
	case string:
		return x, numeral.MatchString(x)
	}
	return "", false
}

// timeNumbers calls visit with every numeral of a decoded JSON value that stands where a time
// would: under a timeKey, or first in a series point (an array of two or more numerals, as
// [time, value] or Prometheus' [time, "value"]). Every other number is left alone: the demo
// world's byte counts span the unix range (a 1.7 TB volume reads as a date in 2027 in
// milliseconds), and one near the wall clock would fail the test for no leak.
func timeNumbers(v any, key string, visit func(string)) {
	switch x := v.(type) {
	case json.Number, string:
		if s, ok := numeralOf(x); ok && timeKey.MatchString(key) {
			visit(s)
		}
	case []any:
		if len(x) >= 2 && !slices.ContainsFunc(x, func(e any) bool { _, ok := numeralOf(e); return !ok }) {
			s, _ := numeralOf(x[0])
			visit(s)
			return
		}
		for _, e := range x {
			timeNumbers(e, key, visit)
		}
	case map[string]any:
		for k, e := range x {
			timeNumbers(e, k, visit)
		}
	}
}

// With DASH_DEMO_CLOCK set, nothing a demo run serves comes from the wall clock: every topic, every
// series, the API's own payloads and the plugins' detail and log routes carry no time near it.
// The demo runs in a synctest bubble, so the wall clock is the bubble's, at 2000-01-01, decades
// from the demo clock whatever the real date: a stamp read from time.Now instead of the host's
// clock is found to the month, however coarse the leak.
func TestDemoClockLeaksNoWallTime(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		fw, cfg := startDemo(t, "DASH_DEMO_CLOCK="+leakClock)
		awaitDemoTopics(t, fw)
		payloads := demoPayloads(t, fw, cfg)
		clock, _ := time.Parse(time.RFC3339, leakClock)
		wall := time.Now() // the bubble's: 2000-01-01T00:00:00Z, plus what the run waited
		for _, path := range slices.Sorted(maps.Keys(payloads)) {
			body := payloads[path]
			if stamps := wallStamps(body, wall); len(stamps) > 0 {
				t.Errorf("GET %s carries %d wall-clock times, first %v:\n%.2000s", path, len(stamps), stamps[:min(len(stamps), 3)], body)
			}
			// The scan sees the stamps the world does carry: read at the demo clock, every envelope
			// (its updatedAt) and every series (its last points, unix seconds) has some.
			if (strings.HasPrefix(path, "/api/v1/topics/") || strings.HasPrefix(path, "/api/v1/series?")) && len(wallStamps(body, clock)) == 0 {
				t.Errorf("GET %s: the scan found no time near the demo clock, so it would miss a leak there:\n%.2000s", path, body)
			}
		}
	})
}

// wallStamps finds a time in every form a leak might take: RFC 3339 and its near forms in text,
// and unix times, whole, fractional or in a string, in a time's place.
func TestWallStampsReadsEveryForm(t *testing.T) {
	now := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	sec := strconv.FormatInt(now.Unix(), 10)
	for _, body := range []string{
		`"2000-01-01T00:00:00Z"`, `"2000-01-01T00:00:00.123456789123Z"`, `"2000-01-01T00:00:00+00:00"`,
		`"2000-01-01T00:00:00+0000"`, `"2000-01-01T00:00:00"`, `"2000-01-01T00:00"`, `"2000-01-01"`,
		`"backup_2000-01-01.tar"`, `"since 2000-01-01 00:00:00 +0000 UTC"`, `"1999-12-31T19:00:00-05:00"`,
		`{"ts":` + sec + `}`, `{"ts":` + sec + `.652}`, `{"ts":"` + sec + `"}`, `{"ts":` + sec + `000}`,
		`{"ts":` + sec + `000000}`, `{"ts":` + sec + `000000000}`, `{"notAfter":` + sec + `}`,
		`{"expires":` + sec + `}`, `{"lastScrubEnd":` + sec + `}`, `{"upSince":` + sec + `}`,
		`{"createdAt":` + sec + `}`, `{"lastTime":` + sec + `}`, `[[` + sec + `.5,"1"]]`, `[[` + sec + `,1,2]]`,
	} {
		if len(wallStamps([]byte(body), now)) == 0 {
			t.Errorf("wallStamps(%s) found no time", body)
		}
	}
	for _, body := range []string{
		`"2026-01-15T12:00:00Z"`, `{"bytes":` + sec + `}`, `{"ts":"x` + sec + `"}`, `"12000-01-01"`,
	} {
		if s := wallStamps([]byte(body), now); len(s) > 0 {
			t.Errorf("wallStamps(%s) = %v, want none", body, s)
		}
	}
}
