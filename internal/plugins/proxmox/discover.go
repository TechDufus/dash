package proxmox

import (
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/TechDufus/dash/internal/upstream"
)

// discover names the node when plugins.proxmox.node is empty: GET /nodes, and exactly one node
// is taken. None or several is a configuration problem no retry fixes, so it ends discovery with
// an error row that says what to set. Any other failure is transient: it retries from backoff,
// doubling up to backoffMax, shows the row as starting for the first grace and then as an error,
// and keeps retrying every steady until the run ends.
func (r *runtime) discover() {
	start := r.h.Now()
	r.mu.Lock()
	r.disc.since = start
	r.mu.Unlock()

	backoff := r.t.backoff
	for attempt := 1; ; attempt++ {
		nodes, err := r.nodeless.Nodes(r.ctx)
		if r.ctx.Err() != nil {
			return
		}
		if err == nil {
			switch len(nodes) {
			case 1:
				r.h.Logger().Info("discovered the Proxmox node; set plugins.proxmox.node to skip discovery", "node", nodes[0])
				r.setNode(nodes[0])
				return
			case 0:
				r.discoveryFailed("the Proxmox API lists no nodes: set plugins.proxmox.node")
			default:
				names := slices.Sorted(slices.Values(nodes))
				r.discoveryFailed("the Proxmox cluster has " + strconv.Itoa(len(names)) + " nodes (" + strings.Join(names, ", ") + "): set plugins.proxmox.node to the one to read")
			}
			return
		}

		cat := upstream.Category(err)
		r.mu.Lock()
		r.disc.cat = cat
		r.mu.Unlock()
		r.h.Logger().Warn("proxmox node discovery failed", "attempt", attempt, "category", cat, "error", r.h.ScrubLog(err.Error()))

		wait := min(backoff, r.t.backoffMax)
		if r.h.Now().Sub(start) >= r.t.grace {
			wait = r.t.steady
		}
		backoff *= 2
		timer := time.NewTimer(wait)
		select {
		case <-r.ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

// discoveryFailed records a failure no retry can fix.
func (r *runtime) discoveryFailed(msg string) {
	r.h.Logger().Error("proxmox node discovery failed", "error", msg)
	r.mu.Lock()
	r.disc.cfgErr = msg
	r.mu.Unlock()
}
