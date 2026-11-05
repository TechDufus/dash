// Package proxmox is the Proxmox VE plugin (design F.0, P3.6): the guests of the node dash reads,
// the node's own facts and its storages as the proxmox.vms, proxmox.node and proxmox.storage
// topics, the detail of one VM, the links into the Proxmox UI, the VM charts, and the VM share of
// the overview (attention items, KPIs, topology).
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

// ID is the plugin's id and its instance name: plugins.proxmox, topics proxmox.vms, proxmox.node
// and proxmox.storage.
const ID plugin.ID = "proxmox"

const (
	// The manifest names of the topics; they are published as "proxmox.<name>".
	topicVMs     = "vms"
	topicNode    = "node"
	topicStorage = "storage"

	// sourceName names Proxmox in the sources of the topics, the status and the overview.
	sourceName = "proxmox"
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
	vms, node, storage time.Duration // the three pollers
	watch              time.Duration // how often the node index is looked at
	// Node discovery: the retry starts at backoff and doubles up to backoffMax; after grace has
	// passed without a node it is retried every steady and the row shows an error.
	backoff, backoffMax, grace, steady time.Duration
}

func defaultTiming() timing {
	return timing{
		vms: 15 * time.Second, node: 30 * time.Second, storage: 60 * time.Second,
		watch:   time.Second,
		backoff: time.Second, backoffMax: 8 * time.Second, grace: time.Minute, steady: 30 * time.Second,
	}
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
			{Name: topicNode, Type: pvemodel.NodeData{}, Doc: "The facts of the Proxmox node: version, kernel, CPU and uptime."},
			{Name: topicStorage, Type: pvemodel.PVEStorageData{}, Doc: "The storages of the Proxmox node with their usage."},
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

// Start builds the clients and launches the pollers; it does not wait for a poll. A configured
// node is known at once, so the service answers from the moment Start returns; an empty one is
// discovered in the background (an error from Start would show the generic "failed to start"
// row and retry only on a configuration change).
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
		opts:      []upstream.Option{upstream.Timeout(hc.Timeout), upstream.CheckRedirect(hc.CheckRedirect), upstream.Cut(h.CutLog)},
		nodeReady: make(chan struct{}),
		logged:    map[string]string{},
	}
	// The nodeless client lists the cluster's nodes and nothing else; the node client is built the
	// moment the node is known, over the same transport, options and token handle, so a rotated
	// secret reaches both.
	r.nodeless = upstream.NewProxmox(c.URL, "", c.TokenID, c.TokenSecret, r.transport, r.opts...)
	p.rt.Store(r)

	if c.Node != "" {
		r.setNode(c.Node)
	} else {
		r.spawn("discover node", r.discover)
	}
	r.spawn("poll vms", func() { r.every(r.t.vms, r.pollVMs) })
	r.spawn("poll node", func() { r.every(r.t.node, r.pollNode) })
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

// Status follows design 5.3: starting until the node is known and the first VM poll has ended,
// then healthy, degraded (some source fails) or error (all of them, or discovery failed). The
// messages carry categories only.
func (p *Plugin) Status() plugin.Status {
	r := p.rt.Load()
	if r == nil {
		return plugin.Status{State: plugin.StateStarting}
	}
	return r.status()
}

// Service exports svc.NodeName; it is nil before Start.
func (p *Plugin) Service() any {
	r := p.rt.Load()
	if r == nil {
		return nil
	}
	return svc.NodeName(nodeName{r})
}

// nodeName is the svc.NodeName of one run.
type nodeName struct{ r *runtime }

func (n nodeName) NodeName() (string, bool) { return n.r.nodeName() }

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
