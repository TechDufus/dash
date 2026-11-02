package proxmox

import (
	"context"

	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/upstream"
)

// pollNode reads the node's facts and publishes them. A failed poll publishes the last good facts
// (none, on the first poll) with the failing source.
func (r *runtime) pollNode(ctx context.Context) {
	client := r.nodeClient()
	if client == nil {
		return
	}
	st, err := client.NodeStatus(ctx)
	if ctx.Err() != nil {
		return
	}
	r.mu.Lock()
	r.nodeOut = outcome{polled: true, cat: upstream.Category(err), at: r.h.Now()}
	if err == nil {
		r.nodeData.Version, r.nodeData.Kernel = st.PVEVersion, st.Kernel
		r.nodeData.CPUModel, r.nodeData.UptimeSeconds = st.CPUModel, st.UptimeSeconds
	}
	data, out := r.nodeData, r.nodeOut
	r.mu.Unlock()
	r.note("node", err)
	r.h.Publish(topicNode, data, []model.SourceStatus{src(sourceName, out)})
}
