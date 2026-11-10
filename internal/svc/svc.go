// Package svc holds the interfaces plugins export to each other and to the core's own code (design
// A.5). They live apart from every plugin so that a consumer never imports a producer: a provider
// implements one of them in its plugin.Provider.Service, a consumer asks for it through
// Host.Service (a plugin) or Supervisor.Service (code that is not a plugin) and gets nothing when
// the provider is not running, which it reports as a source that is not configured.
package svc

import (
	"context"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

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

// NodeIndex is what the kubernetes plugin exports: the cluster's nodes, for the code that ties
// its own objects to them (a VM that is a node, the node series). The plugin's one service
// object implements it together with Cluster; the plugin exports no service while it is
// unavailable or in error, so a consumer sees an absent provider then, not an empty index.
type NodeIndex interface {
	// K8sNode maps a VM name to the cluster node of the same name. ok is false when there is no
	// such node, and for every name until Synced is true.
	K8sNode(vmName string) (node string, ready, ok bool)
	// NodeIP is the internal IP of a node, for the node.* series.
	NodeIP(node string) (ip string, ok bool)
	// Synced reports that the informers have synced; before that K8sNode answers ok=false for
	// every name.
	Synced() bool
	// Revision changes when the node set or a node's readiness changes (it never repeats for
	// the life of the process). A consumer polls it on its own tick and republishes when it
	// differs from the value it last saw; Synced turning true alone is a change the consumer
	// watches separately.
	Revision() uint64
}

// NodeName is what the proxmox plugin exports: the node that is the Hardware page's host, for the
// plugins that need the Proxmox node's name (the host half of the hardware plugin).
type NodeName interface {
	// NodeName is plugins.proxmox.node when it is set, else the only node of a one-node cluster.
	// ok is false until it is known, and for a cluster of several nodes without the setting. The
	// service is non-nil from the plugin's Start, so ok is the only signal.
	NodeName() (name string, ok bool)
}

// ClusterNodes is what the proxmox plugin's service object also implements: every node of the
// cluster it reads, so a consumer that needs one node can say which ones there are.
type ClusterNodes interface {
	// Nodes are the cluster's node names, sorted. ok is false until the first cluster poll has
	// succeeded.
	Nodes() (names []string, ok bool)
}

// Cluster is what the kubernetes plugin exports: the informer caches and the dynamic client, for
// code that reads or patches resources the plugin does not own (Flux and CNPG objects). One
// object also implements NodeIndex, so this method is GVRRevision: NodeIndex already has a
// Revision() uint64 of its own.
type Cluster interface {
	// Synced reports that the typed informers have synced: the readiness gate. A dynamic informer
	// never gates it; Listed is its gate.
	Synced() bool
	// Has reports that the API server serves gvr: the last successful discovery of its group
	// listed it.
	Has(gvr schema.GroupVersionResource) bool
	// Listed reports that the first list of gvr has completed, or that a discovery has evaluated
	// gvr and found it absent (or failed for its group). It is false for a gvr no discovery has
	// evaluated yet, including one registered after the last discovery, unless the discovery
	// failed altogether (nothing will be known, so nothing waits for ever). A builder publishes
	// once its own GVRs are listed or absent, so it never computes from a partial list.
	Listed(gvr schema.GroupVersionResource) bool
	// FailedGroups lists the group versions of the watched resources whose discovery failed at the
	// last attempt (an aggregated API that is down), sorted. Listed counts such a resource as
	// listed so a builder can publish without it, and a resource of a failed group is neither
	// present nor absent: a consumer that would otherwise show its objects as missing reads this
	// to say so.
	FailedGroups() []schema.GroupVersion
	// Refused reports a served resource whose first list has not completed after the sync timeout
	// (60 s) of API-server up-time: the clock starts when the resource appeared, never before
	// dash started, and it pauses while the API server does not answer, so an outage never
	// counts. category is the error category of the watch (auth for a list the client may not
	// make, say), or unreachable when the watch reported none. A resource that is Refused is
	// neither listed nor absent: a consumer's publish gate is Listed(gvr) || Refused(gvr), and it
	// says the category in its source note instead of waiting for ever.
	Refused(gvr schema.GroupVersionResource) (category string, ok bool)
	// Watch asks for informers of gvrs. It is idempotent; the informer of a CRD that is not served
	// starts when the CRD appears (an immediate discovery runs).
	Watch(gvrs ...schema.GroupVersionResource)
	// List returns the cached objects of a watched resource, all namespaces; nil while gvr is not
	// served or not watched.
	List(gvr schema.GroupVersionResource) []*unstructured.Unstructured
	// GVRRevision is bumped when a watched resource in gvrs (all of them when none are given) has an
	// object added, changed or deleted, or when its CRD appears or goes. A consumer polls it on its
	// own tick and republishes when it differs from the value it last saw.
	GVRRevision(gvrs ...schema.GroupVersionResource) uint64
	// Patch sends a JSON merge patch through the dynamic client under the field manager "dash".
	// The error is the API server's, unwrapped.
	Patch(ctx context.Context, gvr schema.GroupVersionResource, namespace, name string, merge []byte) error
}
