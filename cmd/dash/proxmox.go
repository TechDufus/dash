package main

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/TechDufus/dash/internal/config"
)

// proxmoxDiscoveryBudget bounds how long startup waits for the Proxmox API to name its node.
const proxmoxDiscoveryBudget = 60 * time.Second

// nodeEnv is the variable that sets plugins.proxmox.node, named in errors.
const nodeEnv = "DASH_PLUGINS_PROXMOX_NODE"

// resolveProxmoxNode fills cfg.ProxmoxNode (and cfg.HostNodename when it was left unset) from the
// Proxmox API when plugins.proxmox.node is empty. list returns the cluster's node names. It does
// nothing, and does not call list, unless a token and URL are set and the node is empty.
//
// Failure is fatal, unlike at runtime, where Proxmox errors are soft: topics record the source
// error and keep serving last-good data because a node is known. Without a node every Proxmox
// path (/nodes/{node}/...) and the host series selector are undefined, and cfg is passed by value
// into topics, links and api, so discovering lazily would mean threading a mutable node through
// all of them. A bounded retry (budget, backoff 1s doubling to 8s) absorbs a Proxmox API that is
// briefly unavailable, e.g. restarting together with the cluster. After that, exiting non-zero
// lets Kubernetes restart the pod with backoff and shows the cause in its logs. Setting
// plugins.proxmox.node skips discovery entirely.
//
// An empty or multi-node answer is a configuration problem, not an outage, so it fails at once.
func resolveProxmoxNode(ctx context.Context, cfg *config.Config, list func(context.Context) ([]string, error), budget time.Duration) error {
	if cfg.ProxmoxToken == "" || cfg.ProxmoxURL == "" || cfg.ProxmoxNode != "" {
		return nil
	}

	deadline := time.Now().Add(budget)
	backoff := time.Second
	for attempt := 1; ; attempt++ {
		nodes, err := list(ctx)
		if err == nil {
			return pickProxmoxNode(cfg, nodes)
		}
		if ctx.Err() != nil {
			return fmt.Errorf("proxmox: discover node: %w", ctx.Err())
		}
		slog.Warn("proxmox: node discovery failed", "attempt", attempt, "error", err)

		wait := min(backoff, time.Until(deadline))
		if wait <= 0 {
			return fmt.Errorf("proxmox: discover node at plugins.proxmox.url: %w; set plugins.proxmox.node (%s)", err, nodeEnv)
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return fmt.Errorf("proxmox: discover node: %w", ctx.Err())
		case <-timer.C:
		}
		backoff = min(backoff*2, 8*time.Second)
	}
}

// pickProxmoxNode applies the one-node answer to cfg, or explains why it cannot.
func pickProxmoxNode(cfg *config.Config, nodes []string) error {
	switch len(nodes) {
	case 0:
		return fmt.Errorf("proxmox: API at plugins.proxmox.url lists no nodes; set plugins.proxmox.node (%s)", nodeEnv)
	case 1:
	default:
		sorted := slices.Sorted(slices.Values(nodes))
		return fmt.Errorf("proxmox: cluster has %d nodes (%s); set plugins.proxmox.node (%s) to the one to read",
			len(sorted), strings.Join(sorted, ", "), nodeEnv)
	}
	cfg.ProxmoxNode = nodes[0]
	if cfg.HostNodename == "" { // unset: it falls back to plugins.proxmox.node, which was empty
		cfg.HostNodename = nodes[0]
	}
	slog.Info("proxmox: discovered node; set plugins.proxmox.node to skip discovery",
		"node", cfg.ProxmoxNode, "hostNodename", cfg.HostNodename)
	return nil
}
