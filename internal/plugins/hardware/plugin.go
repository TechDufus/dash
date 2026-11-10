// Package hardware is the hardware plugin (design F.0, P3.7): the physical hosts behind the
// cluster (their sensors, fans, power, disks and ZFS pools, read from Prometheus-compatible
// node-exporter, IPMI and SMART series) and the machines next to them that are neither VMs nor
// cluster nodes, shown as up or down with their node-exporter stats. It publishes hardware.hosts
// and hardware.machines, contributes the hosts' attention items, KPIs and topology to the
// overview, and registers the host.* series.
//
// It reads the metrics through the prometheus plugin's svc.Querier, the Proxmox nodes from the
// proxmox plugin's svc.NodeName and svc.ClusterNodes, and the reachability of machines with a
// Gatus check from the gatus plugin's svc.EndpointHealth. It requires none of them: whatever half
// of the plugin cannot run says so in its sources and the rest goes on.
//
// Like every plugin it imports neither internal/config nor internal/api, and it cannot import
// another plugin, so the rules it shares with the other plugins that read the same metrics are
// copies.
package hardware

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/pluginconf"
	hwmodel "github.com/TechDufus/dash/internal/plugins/hardware/model"
	"github.com/TechDufus/dash/internal/svc"
)

// ID is the plugin's id and its instance name: plugins.hardware, topics hardware.hosts and
// hardware.machines.
const ID plugin.ID = "hardware"

const (
	// topicHosts and topicMachines are the manifest names of the topics.
	topicHosts    = "hosts"
	topicMachines = "machines"

	// Source names, on the envelopes, in the status and on the overview.
	sourcePrometheus = "prometheus"
	sourceProxmox    = "proxmox"
	sourceGatus      = "gatus"

	// hostEvery and machinesEvery are how often the metrics are read. watchEvery is how often
	// the plugin looks at its dependencies again: a prometheus plugin or a node that appears
	// later starts the work within this, and a Gatus change reaches the machines within it.
	hostEvery     = 15 * time.Second
	machinesEvery = 15 * time.Second
	watchEvery    = time.Second
	// pollTimeout caps one poll, however many queries it holds.
	pollTimeout = 25 * time.Second
)

// timing is the plugin's cadence; tests shorten it.
type timing struct{ host, machines, watch, timeout time.Duration }

func defaultTiming() timing {
	return timing{host: hostEvery, machines: machinesEvery, watch: watchEvery, timeout: pollTimeout}
}

// manifest is the plugin's declaration. series are the host.* series, which a running plugin
// answers from its own state (a running instance's Manifest is the one the supervisor asks).
func manifest(series []plugin.SeriesSpec) plugin.Manifest {
	return plugin.Manifest{
		ID:          ID,
		Name:        "Hardware",
		Description: "The physical hosts behind the cluster.",
		Version:     "1.0.0",
		// Soft: none of them is required, so a Gatus-only list of machines runs without
		// Prometheus and the host half without Gatus.
		Uses:   []plugin.ID{"prometheus", "proxmox", "gatus"},
		Config: func() any { return &pluginconf.Hardware{} },
		Topics: []plugin.TopicSpec{
			{Name: topicHosts, Type: model.HostsData{}, Doc: "Every host's sensors, power, disks and pools."},
			{Name: topicMachines, Type: hwmodel.MachinesData{}, Doc: "Machines outside the cluster: up or down, with their node-exporter stats."},
		},
		Series: series,
		UI: plugin.UISpec{Nav: []model.NavItem{
			{Path: "/hardware", Label: "Hardware", Title: "Hardware", Icon: "Cpu"},
		}},
	}
}

// New is the plugin's factory.
func New() plugin.Plugin { return newPlugin(defaultTiming()) }

func newPlugin(tm timing) *Plugin { return &Plugin{tm: tm} }

// Plugin is the hardware integration. The supervisor makes a fresh one for every start of the
// instance.
type Plugin struct {
	plugin.ObjectStore
	tm timing
	rt atomic.Pointer[runtime] // set by Start; the series closures read it while the plugin runs

	mu     sync.Mutex // guards everything below
	cancel context.CancelFunc
	wg     sync.WaitGroup // the goroutines Start launched

	hosts []hostState // by the host's index in plugins.hardware.hosts; see hostAt
	// hostsPublished is set once the hosts topic has been published; see publishHosts.
	hostsPublished bool
	mach           machineState
	// ran is set when the plugin has published anything. From then on the row is never
	// "unavailable": it holds data, and a row that holds data is visible (design A.9).
	ran bool
}

// runtime is what one Start knows: the host, the normalized settings and the queries they shape.
type runtime struct {
	h   plugin.Host
	cfg pluginconf.Hardware
	q   exprs

	mu sync.Mutex // guards lastNode and lastNodename
	// lastNode is each host's node as last resolved against the cluster's nodes, and lastNodename
	// the nodename the proxmox plugin last named for the host without one, by index; see hostIDs.
	lastNode, lastNodename map[int]string
}

// hostAt is host i's state, growing the list as needed. p.mu is held.
func (p *Plugin) hostAt(i int) *hostState {
	for len(p.hosts) <= i {
		p.hosts = append(p.hosts, hostState{})
	}
	return &p.hosts[i]
}

// querier is the prometheus plugin's service, nil while that plugin does not run.
func (rt *runtime) querier() svc.Querier {
	if v, ok := rt.h.Service("prometheus"); ok {
		if q, ok := v.(svc.Querier); ok {
			return q
		}
	}
	return nil
}

// endpoints is the gatus plugin's service, nil while that plugin does not run.
func (rt *runtime) endpoints() svc.EndpointHealth {
	if v, ok := rt.h.Service("gatus"); ok {
		if e, ok := v.(svc.EndpointHealth); ok {
			return e
		}
	}
	return nil
}

// proxmoxNode is the node the proxmox plugin names: plugins.proxmox.node, or the only node of a
// one-node cluster. It is the node of the host without a nodename (or of the only host).
func (rt *runtime) proxmoxNode() (string, bool) {
	if v, ok := rt.h.Service("proxmox"); ok {
		if n, ok := v.(svc.NodeName); ok {
			if name, ok := n.NodeName(); ok && name != "" {
				return name, true
			}
		}
	}
	return "", false
}

// clusterNodes are the nodes of the Proxmox cluster, once the proxmox plugin knows them.
func (rt *runtime) clusterNodes() ([]string, bool) {
	if v, ok := rt.h.Service("proxmox"); ok {
		if c, ok := v.(svc.ClusterNodes); ok {
			return c.Nodes()
		}
	}
	return nil, false
}

// nodenameHint says what names the node of the host without a nodename while it is unknown
// although the proxmox plugin has read the cluster: plugins.proxmox.node is unset in a cluster of
// several nodes, or names no node of the cluster, which the proxmox row reports. This plugin
// cannot tell the two apart, so the hint covers both. It is empty otherwise, and hosts with a
// nodename never need it.
func (rt *runtime) nodenameHint(ids []hostID) string {
	for _, id := range ids {
		if id.known() {
			continue
		}
		nodes, ok := rt.clusterNodes()
		if !ok || len(nodes) == 0 {
			return ""
		}
		return fmt.Sprintf("nothing names the node of plugins.hardware.hosts[%d] among the Proxmox nodes (%s): plugins.proxmox.node is unset or names none of them (the proxmox row says which), and that host has no nodename",
			id.i, strings.Join(nodes, ", "))
	}
	return ""
}

func (p *Plugin) Manifest() plugin.Manifest { return manifest(seriesSpecs(p.seriesExpr)) }

// settings is the plugin's section as the supervisor hands it over: a pointer, or a value.
func settings(cfg any) (pluginconf.Hardware, error) {
	switch c := cfg.(type) {
	case *pluginconf.Hardware:
		if c != nil {
			return *c, nil
		}
	case pluginconf.Hardware:
		return c, nil
	}
	return pluginconf.Hardware{}, fmt.Errorf("hardware: unexpected configuration type %T", cfg)
}

// Configured is false with neither a host nor a machine: the plugin is then disabled, or
// unconfigured when it was switched on. hosts: [{}] is a host.
func (p *Plugin) Configured(cfg any) bool {
	c, err := settings(cfg)
	return err == nil && (len(c.Hosts) > 0 || len(c.Machines) > 0)
}

// ConfigHint implements plugin.ConfigHinter.
func (p *Plugin) ConfigHint() string { return "plugins.hardware.hosts or machines" }

// Validate applies the section's own rules, so that a configuration the supervisor gets by
// another way than the config loader is refused for the same reasons.
func (p *Plugin) Validate(cfg any) error {
	c, err := settings(cfg)
	if err != nil {
		return err
	}
	return c.Validate()
}

// Start launches the pollers; it does not wait for a poll. Both halves always run: the hosts
// half publishes an empty list when there is no host, the machines half when there is no machine.
func (p *Plugin) Start(_ context.Context, h plugin.Host, cfg any) error {
	h = p.Wrap(h)
	c, err := settings(cfg)
	if err != nil {
		return err
	}
	c = c.Normalized()
	rt := &runtime{h: h, cfg: c, q: newExprs(c.Selectors)}
	ctx, cancel := context.WithCancel(context.Background())

	p.mu.Lock()
	p.cancel = cancel
	p.hosts = make([]hostState, len(c.Hosts))
	p.mu.Unlock()
	p.rt.Store(rt)

	p.spawn(h, "hardware hosts", func() { p.hostLoop(ctx, rt) })
	p.spawn(h, "hardware machines", func() { p.machinesLoop(ctx, rt) })
	return nil
}

// spawn runs fn on Host.Go and counts it, so that Stop can wait for it.
func (p *Plugin) spawn(h plugin.Host, name string, fn func()) {
	p.wg.Add(1)
	h.Go(name, func() {
		defer p.wg.Done()
		fn()
	})
}

// Stop ends the pollers and waits for them, as long as ctx allows.
func (p *Plugin) Stop(ctx context.Context) error {
	p.mu.Lock()
	cancel := p.cancel
	p.mu.Unlock()
	if cancel == nil {
		return nil
	}
	cancel()
	done := make(chan struct{})
	go func() { p.wg.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// availability is which halves of the plugin can run now, from the services it can see.
type availability struct {
	querier bool // the prometheus plugin runs
	gatus   bool // the gatus plugin runs
	// host, prom and gatusMachines are the halves that can run: a host (needs the querier and its
	// nodename), the machines read from node-exporter (the querier), and the machines read from
	// Gatus.
	host, prom, gatusMachines bool
}

// any reports whether something can run.
func (a availability) any() bool { return a.host || a.prom || a.gatusMachines }

func (rt *runtime) availability(ids []hostID) availability {
	var a availability
	a.querier = rt.querier() != nil
	a.gatus = rt.endpoints() != nil
	a.host = a.querier && slices.ContainsFunc(ids, hostID.runnable)
	a.prom = a.querier && slices.ContainsFunc(rt.cfg.Machines, isPrometheus)
	a.gatusMachines = a.gatus && slices.ContainsFunc(rt.cfg.Machines, isGatus)
	return a
}

func isPrometheus(m pluginconf.HardwareMachine) bool { return m.NodeExporterNodename != "" }
func isGatus(m pluginconf.HardwareMachine) bool      { return m.GatusEndpoint != "" }

// notConfigured is the source of a dependency that does not run.
func notConfigured(name string) model.SourceStatus {
	return model.SourceStatus{Name: name, OK: false, Error: "not_configured"}
}

// Status. Unavailable (hidden) while nothing can run and nothing has been published: "not
// configured: set plugins.prometheus.url", "waiting for the Proxmox node name", or, once the
// Proxmox cluster is read and nothing names the node of the host without a nodename among its
// nodes, which settings name it. Otherwise starting until each host and half that can run has
// finished a poll, then healthy, or degraded when some source fails and error when every one
// does. The message names the hosts whose poll failed, says which settings name the node of the
// host without a nodename, and which host is not read because it repeats an earlier one.
func (p *Plugin) Status() plugin.Status {
	rt := p.rt.Load()
	if rt == nil {
		return plugin.Status{State: plugin.StateStarting}
	}
	ids := rt.hostIDs()
	av := rt.availability(ids)

	p.mu.Lock()
	ran, machPolled, machErr := p.ran, p.mach.polled, p.mach.err
	hosts := make([]hostState, len(ids))
	for _, id := range ids {
		hosts[id.i] = *p.hostAt(id.i)
	}
	p.mu.Unlock()

	if !ran && !av.any() {
		return plugin.Status{State: plugin.StateUnavailable, Message: unavailableMessage(rt, ids, av)}
	}

	var srcs []model.SourceStatus
	var hostFails []string // "<name>: <category>" of each host whose last poll failed
	hostsWaiting := false
	// promOK is whether some prometheus poll, of a host or of the machines, went fine.
	promOK := slices.ContainsFunc(rt.cfg.Machines, isPrometheus) && av.querier && machPolled && machErr == ""
	for _, id := range ids {
		s := hosts[id.i]
		switch {
		case !av.querier:
			srcs = mergeSources(srcs, notConfigured(sourcePrometheus))
		case !id.known():
			srcs = mergeSources(srcs, notConfigured(sourceProxmox))
		case id.dupOf >= 0:
		case !s.polled:
			hostsWaiting = true
		default:
			srcs = mergeSources(srcs, model.SourceStatus{Name: sourcePrometheus, OK: s.err == "", Error: s.err})
			if s.err != "" {
				hostFails = append(hostFails, id.name+": "+s.err)
			} else {
				promOK = true
			}
		}
	}
	hasProm, hasGatus := slices.ContainsFunc(rt.cfg.Machines, isPrometheus), slices.ContainsFunc(rt.cfg.Machines, isGatus)
	if hasProm {
		switch {
		case !av.querier:
			srcs = mergeSources(srcs, notConfigured(sourcePrometheus))
		case machPolled:
			srcs = mergeSources(srcs, model.SourceStatus{Name: sourcePrometheus, OK: machErr == "", Error: machErr})
		}
	}
	if hasGatus {
		if e := rt.endpoints(); e == nil {
			srcs = mergeSources(srcs, notConfigured(sourceGatus))
		} else if e.Revision() > 0 { // before its first poll Gatus has said nothing yet
			for _, s := range e.SourceStatuses() {
				srcs = mergeSources(srcs, s)
			}
		}
	}

	// Gatus is polled by its own plugin: its first poll has ended once the revision moved off 0
	// (whatever its outcome), and until then the machines cannot say if they are up.
	gatusWaiting := av.gatusMachines && rt.endpoints().Revision() == 0
	if hostsWaiting || (av.prom && !machPolled) || gatusWaiting {
		return plugin.Status{State: plugin.StateStarting, Message: "waiting for the first poll", Sources: srcs}
	}
	if hasProm && av.querier && machPolled && machErr != "" && len(hostFails) > 0 {
		hostFails = append(hostFails, "machines: "+machErr)
	}
	msg := failures(srcs, hostFails)
	if hint := rt.nodenameHint(ids); hint != "" {
		// The proxmox source reads not_configured because nothing names the host's node: the hint
		// says what to set instead.
		others := failures(slices.DeleteFunc(slices.Clone(srcs), func(s model.SourceStatus) bool { return s.Name == sourceProxmox }), hostFails)
		msg = joinMsg(hint, others)
	}
	state := stateOf(srcs)
	// The merged prometheus source fails when any host or the machines failed; while another of
	// them polled fine, the plugin still shows data from prometheus, so the row is degraded.
	if state == plugin.StateError && promOK {
		state = plugin.StateDegraded
	}
	if dup := rt.dupMessage(ids); dup != "" {
		msg = joinMsg(msg, dup)
		if state == plugin.StateHealthy {
			state = plugin.StateDegraded
		}
	}
	return plugin.Status{State: state, Message: msg, Sources: srcs}
}

// joinMsg joins the non-empty parts of a status message.
func joinMsg(parts ...string) string {
	return strings.Join(slices.DeleteFunc(parts, func(s string) bool { return s == "" }), "; ")
}

// unavailableMessage names what is missing for the plugin to run at all. The first thing it
// would need, in the order hosts, node-exporter machines, Gatus machines.
func unavailableMessage(rt *runtime, ids []hostID, av availability) string {
	switch {
	case len(ids) > 0 && !av.querier:
		return "not configured: set plugins.prometheus.url"
	case slices.ContainsFunc(ids, func(id hostID) bool { return !id.known() }):
		return cmp.Or(rt.nodenameHint(ids), "waiting for the Proxmox node name")
	case slices.ContainsFunc(rt.cfg.Machines, isPrometheus) && !av.querier:
		return "not configured: set plugins.prometheus.url"
	case slices.ContainsFunc(rt.cfg.Machines, isGatus) && !av.gatus:
		return "not configured: set plugins.gatus.url"
	}
	return "not configured"
}

func stateOf(srcs []model.SourceStatus) plugin.State {
	bad := 0
	for _, s := range srcs {
		if !s.OK {
			bad++
		}
	}
	switch {
	case bad == 0:
		return plugin.StateHealthy
	case bad == len(srcs):
		return plugin.StateError
	}
	return plugin.StateDegraded
}

// failures is the status message of failing sources: names and categories, never a URL. When
// host polls failed, the prometheus source names those hosts ("prometheus: pve-2: timeout"), so
// one merged source still says which host is the trouble; the source itself keeps the category.
func failures(srcs []model.SourceStatus, hostFails []string) string {
	var parts []string
	for _, s := range srcs {
		switch {
		case s.OK:
		case s.Name == sourcePrometheus && len(hostFails) > 0:
			for _, f := range hostFails {
				parts = append(parts, s.Name+": "+f)
			}
		default:
			parts = append(parts, s.Name+": "+s.Error)
		}
	}
	return strings.Join(parts, "; ")
}

// mergeSources adds s to base without repeating a name: for a name in both, the failing entry
// wins.
func mergeSources(base []model.SourceStatus, s model.SourceStatus) []model.SourceStatus {
	for i := range base {
		if base[i].Name == s.Name {
			if !s.OK && base[i].OK {
				base[i] = s
			}
			return base
		}
	}
	return append(base, s)
}

// publish sends a topic and records that the plugin holds data.
func (p *Plugin) publish(rt *runtime, topic string, data any, srcs []model.SourceStatus) {
	p.mu.Lock()
	p.ran = true
	p.mu.Unlock()
	rt.h.Publish(topic, data, srcs)
}

var (
	_ plugin.Plugin          = (*Plugin)(nil)
	_ plugin.Configured      = (*Plugin)(nil)
	_ plugin.ConfigHinter    = (*Plugin)(nil)
	_ plugin.Validator       = (*Plugin)(nil)
	_ plugin.AttentionSource = (*Plugin)(nil)
	_ plugin.KPISource       = (*Plugin)(nil)
	_ plugin.TopologySource  = (*Plugin)(nil)
	_ plugin.Demo            = (*Plugin)(nil)
)
