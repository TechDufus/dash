// Package proxmox is the Proxmox VE plugin (design F.0, P3.6): the guests of the cluster dash reads
// through one connection, the cluster with every node's usage and facts, and every node's storages
// as the proxmox.vms, proxmox.cluster and proxmox.storage topics, the detail of one VM, the links
// into the Proxmox UI, the VM charts, and the Proxmox share of the overview (attention items, KPIs,
// topology). A standalone server is a cluster of one and takes the same path.
//
// It reads Proxmox through internal/upstream and the settings through internal/pluginconf; like
// every plugin it imports neither internal/config nor internal/api, nor another plugin.
package proxmox

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/pluginconf"
	pvemodel "github.com/TechDufus/dash/internal/plugins/proxmox/model"
	"github.com/TechDufus/dash/internal/svc"
	"github.com/TechDufus/dash/internal/upstream"
)

// ID is the plugin's id and its instance name: plugins.proxmox, topics proxmox.vms, proxmox.cluster
// and proxmox.storage.
const ID plugin.ID = "proxmox"

const (
	// The manifest names of the topics; they are published as "proxmox.<name>".
	topicVMs     = "vms"
	topicCluster = "cluster"
	topicStorage = "storage"

	// sourceName names Proxmox in the sources of the topics, the status and the overview.
	sourceName = "proxmox"
	// sourceClusterStatus names /cluster/status when it fails on its own: the token lacks Sys.Audit
	// on /, so the cluster's name and quorum are unknown while everything else is read.
	sourceClusterStatus = "cluster status"
	// sourcePrometheus names the metrics source of proxmox.storage when it reads the pve-exporter.
	sourcePrometheus = "prometheus"
	// sourceInsecure is the note that certificate verification is off: it shows in the row's
	// sources and draws no chip, because chips are drawn for sources that are not ok only.
	sourceInsecure = "tls: verification disabled"

	// requestTimeout caps one request to Proxmox.
	requestTimeout = 5 * time.Second
)

// timing holds the intervals of the plugin; tests shorten them.
type timing struct {
	vms, cluster, storage time.Duration // the three pollers
	watch                 time.Duration // how often the node index is looked at
}

func defaultTiming() timing {
	return timing{vms: 15 * time.Second, cluster: 30 * time.Second, storage: 60 * time.Second, watch: time.Second}
}

// Plugin reads Proxmox. The supervisor makes a fresh one for every start of the instance.
type Plugin struct {
	plugin.ObjectStore
	t  timing
	rt atomic.Pointer[runtime] // set by Start
}

// New is the plugin's factory.
func New() plugin.Plugin { return &Plugin{t: defaultTiming()} }

func (p *Plugin) timing() timing {
	if p.t == (timing{}) {
		return defaultTiming()
	}
	return p.t
}

// Manifest is the plugin's declaration. Its series close over p, so the series the API answers
// are those of the instance that runs.
func (p *Plugin) Manifest() plugin.Manifest {
	return plugin.Manifest{
		ID:          ID,
		Name:        "Proxmox VE",
		Description: "Virtual machines, containers and the hypervisor.",
		Version:     "1.0.0",
		Uses:        []plugin.ID{"kubernetes", "prometheus"},
		Config:      func() any { return &pluginconf.Proxmox{} },
		Topics: []plugin.TopicSpec{
			{Name: topicVMs, Type: model.VMsData{}, Doc: "Virtual machines and containers, and the recent tasks."},
			{Name: topicCluster, Type: pvemodel.PVEClusterData{}, Doc: "The Proxmox cluster: its name and quorum, every node with its usage and facts, and the totals over the online nodes."},
			{Name: topicStorage, Type: pvemodel.PVEStorageData{}, Doc: "The storages of every node with their usage, each shared storage once, and their totals."},
		},
		Series: p.seriesSpecs(),
		UI: plugin.UISpec{Nav: []model.NavItem{
			{Path: "/vms", Label: "VMs", Title: "Virtual machines", Icon: "Server"},
		}},
	}
}

// settings is the plugin's section as the supervisor hands it over: a pointer, or a value.
func settings(cfg any) (pluginconf.Proxmox, error) {
	switch c := cfg.(type) {
	case *pluginconf.Proxmox:
		if c != nil {
			return *c, nil
		}
	case pluginconf.Proxmox:
		return c, nil
	}
	return pluginconf.Proxmox{}, fmt.Errorf("proxmox: unexpected configuration type %T", cfg)
}

// Configured needs the URL and both halves of the token: without them the plugin is disabled, or
// unconfigured when it was switched on.
func (p *Plugin) Configured(cfg any) bool {
	c, err := settings(cfg)
	return err == nil && c.URL != "" && c.TokenID != "" && c.TokenSecret.IsSet()
}

// ConfigHint implements plugin.ConfigHinter.
func (p *Plugin) ConfigHint() string { return "plugins.proxmox.url, tokenID and tokenSecret" }

// Validate applies the section's own rules, so that a configuration the supervisor gets by
// another way than the config loader is refused for the same reasons.
func (p *Plugin) Validate(cfg any) error {
	c, err := settings(cfg)
	if err != nil {
		return err
	}
	return c.Validate()
}

// Start builds the client and launches the pollers; it does not wait for a poll. One client reads
// the whole cluster through whichever node the URL reaches, and addresses a node by name where a
// call needs one (a node's facts, a VM's detail).
func (p *Plugin) Start(ctx context.Context, h plugin.Host, cfg any) error {
	h = p.Wrap(h)
	c, err := settings(cfg)
	if err != nil {
		return err
	}
	hc, err := h.HTTPClient(plugin.HTTPConfig{
		Timeout: requestTimeout, CAFile: c.TLS.CAFile, InsecureSkipVerify: c.TLS.InsecureSkipVerify,
	})
	if err != nil {
		return err
	}
	rctx, cancel := context.WithCancel(ctx)
	r := &runtime{
		h: h, cfg: c, t: p.timing(), ctx: rctx, cancel: cancel,
		transport: hc.Transport,
		// The transport carries TLS and the dial guard; the timeout and the redirect policy of hc
		// ride as options, because a client built from the transport alone loses them (design D7).
		opts:   []upstream.Option{upstream.Timeout(hc.Timeout), upstream.CheckRedirect(hc.CheckRedirect), upstream.Cut(h.CutLog)},
		facts:  map[string]upstream.PVEHost{},
		logged: map[string]string{},
	}
	r.client = upstream.NewProxmox(c.URL, c.TokenID, c.TokenSecret, r.transport, r.opts...)
	r.detail = r.client
	p.rt.Store(r)

	r.spawn("poll vms", func() { r.every(r.t.vms, r.pollVMs) })
	r.spawn("poll cluster", func() { r.every(r.t.cluster, r.pollCluster) })
	r.spawn("poll storage", r.storageLoop)
	r.spawn("watch node index", r.watchIndex)
	return nil
}

// Stop ends every goroutine Start launched, as long as ctx allows.
func (p *Plugin) Stop(ctx context.Context) error {
	r := p.rt.Load()
	if r == nil {
		return nil
	}
	r.cancel()
	done := make(chan struct{})
	go func() { r.wg.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Status follows design 5.3: starting until the first VM poll has ended, then healthy, degraded
// (some source fails) or error (all of them). The messages carry categories only.
func (p *Plugin) Status() plugin.Status {
	r := p.rt.Load()
	if r == nil {
		return plugin.Status{State: plugin.StateStarting}
	}
	return r.status()
}

// Service exports svc.NodeName and svc.ClusterNodes (one object); it is nil before Start.
func (p *Plugin) Service() any {
	r := p.rt.Load()
	if r == nil {
		return nil
	}
	return service{r}
}

// service is the svc.NodeName and svc.ClusterNodes of one run.
type service struct{ r *runtime }

func (s service) NodeName() (string, bool) { return s.r.nodeName() }
func (s service) Nodes() ([]string, bool)  { return s.r.clusterNodes() }

var (
	_ svc.NodeName     = service{}
	_ svc.ClusterNodes = service{}
)

// Demo implements plugin.Demo.
func (p *Plugin) Demo() plugin.Plugin { return newTwin() }

// spawn runs fn on Host.Go and counts it, so Stop can wait for it.
func (r *runtime) spawn(name string, fn func()) {
	r.wg.Add(1)
	r.h.Go(name, func() {
		defer r.wg.Done()
		fn()
	})
}

var (
	_ plugin.Plugin          = (*Plugin)(nil)
	_ plugin.Configured      = (*Plugin)(nil)
	_ plugin.ConfigHinter    = (*Plugin)(nil)
	_ plugin.Validator       = (*Plugin)(nil)
	_ plugin.Provider        = (*Plugin)(nil)
	_ plugin.Router          = (*Plugin)(nil)
	_ plugin.LinkProvider    = (*Plugin)(nil)
	_ plugin.AttentionSource = (*Plugin)(nil)
	_ plugin.KPISource       = (*Plugin)(nil)
	_ plugin.TopologySource  = (*Plugin)(nil)
	_ plugin.Demo            = (*Plugin)(nil)
)
