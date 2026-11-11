package ssh

import (
	"strings"
	"sync"
	"time"

	"github.com/TechDufus/dash/internal/pluginconf"
	sshmodel "github.com/TechDufus/dash/internal/plugins/ssh/model"
)

// historyWindow is how far back a host's history goes.
const historyWindow = time.Hour

// historyCap is the most samples a ring holds: an hour at the shortest interval.
const historyCap = int(historyWindow/time.Second) / pluginconf.MinSSHInterval

// ring is one host's recent samples, oldest first, in a fixed buffer.
type ring struct {
	buf        []sshmodel.Sample
	start, len int
}

func (r *ring) add(s sshmodel.Sample) {
	if r.buf == nil {
		r.buf = make([]sshmodel.Sample, historyCap)
	}
	// A sample older than the newest (a clock that stepped back) would break the order: the
	// history drops what it would put after it.
	for r.len > 0 && r.buf[(r.start+r.len-1)%len(r.buf)].T >= s.T {
		r.len--
	}
	if r.len == len(r.buf) {
		r.start, r.len = (r.start+1)%len(r.buf), r.len-1
	}
	r.buf[(r.start+r.len)%len(r.buf)] = s
	r.len++
}

// since returns the samples from cutoff on, oldest first, and forgets the older ones.
func (r *ring) since(cutoff int64) []sshmodel.Sample {
	for r.len > 0 && r.buf[r.start].T < cutoff {
		r.start, r.len = (r.start+1)%len(r.buf), r.len-1
	}
	out := make([]sshmodel.Sample, r.len)
	for i := range r.len {
		out[i] = r.buf[(r.start+i)%len(r.buf)]
	}
	return out
}

// history is the samples of every host, by name ignoring case. It belongs to the plugin's
// factory, not to an instance: a settings change restarts the instance and the charts keep their
// hour. It is in memory only, so a restart of dash starts it again.
type history struct {
	mu    sync.Mutex
	hosts map[string]*ring
}

func newHistory() *history { return &history{hosts: map[string]*ring{}} }

func historyKey(name string) string { return strings.ToLower(name) }

// add records a sample of host name.
func (h *history) add(name string, s sshmodel.Sample) {
	h.mu.Lock()
	defer h.mu.Unlock()
	k := historyKey(name)
	r := h.hosts[k]
	if r == nil {
		r = &ring{}
		h.hosts[k] = r
	}
	r.add(s)
}

// samples are host name's samples of the last hour before now; never nil.
func (h *history) samples(name string, now time.Time) []sshmodel.Sample {
	h.mu.Lock()
	defer h.mu.Unlock()
	r := h.hosts[historyKey(name)]
	if r == nil {
		return []sshmodel.Sample{}
	}
	return r.since(now.Add(-historyWindow).Unix())
}

// keep forgets every host whose name is not in names: a host removed from the settings.
func (h *history) keep(names []string) {
	want := map[string]bool{}
	for _, n := range names {
		want[historyKey(n)] = true
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for k := range h.hosts {
		if !want[k] {
			delete(h.hosts, k)
		}
	}
}
