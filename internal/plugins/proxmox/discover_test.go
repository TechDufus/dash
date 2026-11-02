package proxmox

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/pluginconf"
	pvemodel "github.com/TechDufus/dash/internal/plugins/proxmox/model"
	"github.com/TechDufus/dash/internal/svc"
)

func nodesBody(names ...string) string {
	items := make([]string, len(names))
	for i, n := range names {
		items[i] = `{"node":"` + n + `","status":"online"}`
	}
	return `{"data":[` + strings.Join(items, ",") + `]}`
}

// discovering starts the plugin with no node configured, against pve.
func discovering(t *testing.T, pve *fakePVE, tm func(*timing)) *env {
	t.Helper()
	return newEnv(t, envOpts{pve: pve, timing: tm, cfg: func(c *pluginconf.Proxmox) { c.Node = "" }})
}

func nodeNameOf(e *env) (string, bool) {
	return e.current().Service().(svc.NodeName).NodeName()
}

// A one-node cluster is named in the background: the row starts and then runs, the service
// answers once the node is found, and every topic carries it.
func TestDiscoveryTakesTheOnlyNode(t *testing.T) {
	pve := newFakePVE(t, false)
	pve.set("/nodes", nodesBody("pve-1"))
	e := discovering(t, pve, nil)
	e.state(t, plugin.StateHealthy)
	if n, ok := nodeNameOf(e); !ok || n != "pve-1" {
		t.Errorf("NodeName = %q, %v", n, ok)
	}
	var n pvemodel.NodeData
	waitFor(t, "the node topic", func() bool { e.payload(t, "proxmox.node", &n); return n.Node != "" })
	if n.Node != "pve-1" || n.Ref != "proxmox:Node/pve-1" {
		t.Errorf("node = %+v", n)
	}
	if got := pve.count("/nodes"); got != 1 {
		t.Errorf("GET /nodes = %d times, want 1", got)
	}
	if !strings.Contains(e.logs.String(), "discovered the Proxmox node") {
		t.Error("discovery did not log the node it took")
	}
}

// None or several nodes is a configuration problem: no retry, an error row that says what to set,
// and nothing is read from a node.
func TestDiscoveryOfNoneOrSeveralNodesIsAConfigError(t *testing.T) {
	for _, tc := range []struct {
		name, body, want string
	}{
		{"several", nodesBody("pve-b", "pve-a"), "2 nodes (pve-a, pve-b): set plugins.proxmox.node"},
		{"none", `{"data":[]}`, "lists no nodes: set plugins.proxmox.node"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pve := newFakePVE(t, false)
			pve.set("/nodes", tc.body)
			e := discovering(t, pve, nil)
			pi := e.state(t, plugin.StateError)
			if !strings.Contains(pi.Message, tc.want) {
				t.Errorf("message = %q, want it to contain %q", pi.Message, tc.want)
			}
			time.Sleep(100 * time.Millisecond) // many retry intervals at the test timing
			if got := pve.count("/nodes"); got != 1 {
				t.Errorf("GET /nodes = %d times, want 1: a configuration error is not retried", got)
			}
			if n, ok := nodeNameOf(e); ok {
				t.Errorf("NodeName = %q, want it unknown", n)
			}
			if pve.total() != 1 {
				t.Errorf("%d requests, want only the one to /nodes", pve.total())
			}
		})
	}
}

// A transient failure is retried: the row stays starting (the first minute), the nodeless client
// asks /nodes and nothing else, and the first good answer takes the node.
func TestDiscoveryRetriesATransientFailure(t *testing.T) {
	pve := newFakePVE(t, false)
	pve.set("/nodes", nodesBody("pve-1"))
	pve.fail("/nodes", http.StatusInternalServerError)
	e := discovering(t, pve, nil)

	waitFor(t, "a few retries", func() bool { return pve.count("/nodes") >= 3 })
	if st := e.current().Status(); st.State != plugin.StateStarting {
		t.Errorf("state = %s (%q) during the grace period, want starting", st.State, st.Message)
	}
	if pve.total() != pve.count("/nodes") {
		t.Errorf("the nodeless client reached more than /nodes: %d requests, %d to /nodes", pve.total(), pve.count("/nodes"))
	}
	if _, ok := nodeNameOf(e); ok {
		t.Error("NodeName is known before discovery succeeded")
	}
	pve.heal("/nodes")
	e.state(t, plugin.StateHealthy)
	if n, ok := nodeNameOf(e); !ok || n != "pve-1" {
		t.Errorf("NodeName = %q, %v", n, ok)
	}
}

// After the grace period without a node the row is an error with the category, and discovery
// keeps retrying every steady until it succeeds.
func TestDiscoveryFailsVisiblyAfterTheGraceAndKeepsRetrying(t *testing.T) {
	pve := newFakePVE(t, false)
	pve.set("/nodes", nodesBody("pve-1"))
	pve.fail("/nodes", http.StatusForbidden)
	e := discovering(t, pve, func(tm *timing) { tm.grace = 60 * time.Millisecond })

	pi := e.state(t, plugin.StateError)
	if !strings.Contains(pi.Message, "node discovery failed: auth") || !strings.Contains(pi.Message, "plugins.proxmox.node") {
		t.Errorf("message = %q", pi.Message)
	}
	before := pve.count("/nodes")
	waitFor(t, "more retries after the error", func() bool { return pve.count("/nodes") >= before+3 })
	pve.heal("/nodes")
	e.state(t, plugin.StateHealthy)
}

// Stop ends the retry: no request reaches the fake afterwards.
func TestDiscoveryStopsWithThePlugin(t *testing.T) {
	pve := newFakePVE(t, false)
	pve.fail("/nodes", http.StatusInternalServerError)
	e := discovering(t, pve, nil)
	waitFor(t, "the first retries", func() bool { return pve.count("/nodes") >= 2 })
	e.sup.Shutdown(t.Context())
	n := pve.count("/nodes")
	time.Sleep(100 * time.Millisecond)
	if got := pve.count("/nodes"); got != n {
		t.Errorf("GET /nodes went from %d to %d after Stop", n, got)
	}
}

// The node client never serves a request before the node is known: while discovery fails, the
// pollers wait, and nothing is published.
func TestNothingIsReadOrPublishedBeforeTheNodeIsKnown(t *testing.T) {
	pve := newFakePVE(t, false)
	pve.fail("/nodes", http.StatusInternalServerError)
	e := discovering(t, pve, nil)
	waitFor(t, "retries", func() bool { return pve.count("/nodes") >= 3 })
	if pve.total() != pve.count("/nodes") {
		t.Errorf("requests beyond /nodes before the node is known: %d of %d", pve.total()-pve.count("/nodes"), pve.total())
	}
	for _, topic := range []string{"proxmox.vms", "proxmox.node", "proxmox.storage"} {
		var v map[string]any
		if _, ok := e.payload(t, topic, &v); ok {
			t.Errorf("%s was published before the node was known", topic)
		}
	}
}
