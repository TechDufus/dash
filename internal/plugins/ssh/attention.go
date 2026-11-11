package ssh

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	sshmodel "github.com/TechDufus/dash/internal/plugins/ssh/model"
)

// The file system thresholds, of the used share of a file system's size.
const (
	fsWarnRatio = 0.90
	fsCritRatio = 0.95
)

// sourceSSH is the label of the plugin's attention items.
const sourceSSH = "ssh"

// hostRoute is the page of one host.
func hostRoute(name string) string { return routeHosts + "/" + url.PathEscape(name) }

// stateHealth is the health of a host's state alone.
func stateHealth(s sshmodel.HostState) model.Health {
	switch s {
	case sshmodel.StateUp:
		return model.HealthOK
	case sshmodel.StateKeyChanged:
		return model.HealthCrit
	case sshmodel.StateUnreachable, sshmodel.StateUnauthorized:
		return model.HealthWarn
	}
	return model.HealthUnknown
}

// fsHealth is the health of a file system's used share.
func fsHealth(f sshmodel.Filesystem) model.Health {
	if f.TotalBytes <= 0 {
		return model.HealthOK
	}
	switch r := float64(f.UsedBytes) / float64(f.TotalBytes); {
	case r >= fsCritRatio:
		return model.HealthCrit
	case r >= fsWarnRatio:
		return model.HealthWarn
	}
	return model.HealthOK
}

func poolHealthy(p sshmodel.Pool) bool { return strings.EqualFold(p.Health, "ONLINE") }

// rank orders healths from best to worst; unknown is between ok and warn, so a host that is up
// and fine is ok and a host that is not known yet stays unknown.
func rank(h model.Health) int {
	switch h {
	case model.HealthOK:
		return 0
	case model.HealthUnknown:
		return 1
	case model.HealthWarn:
		return 2
	case model.HealthCrit:
		return 3
	}
	return 1
}

func worse(a, b model.Health) model.Health {
	if rank(b) > rank(a) {
		return b
	}
	return a
}

// healthOf is the worst of the host's state and, while it is up, what it reports: a full file
// system, a failed unit, a pool that is not online. Unknown while connecting or unverified.
func healthOf(h sshmodel.Host) model.Health {
	health := stateHealth(h.State)
	if h.State != sshmodel.StateUp || h.Info == nil {
		return health
	}
	for _, f := range h.Info.Filesystems {
		health = worse(health, fsHealth(f))
	}
	if s := h.Info.Services; s != nil && len(s.Failed) > 0 {
		health = worse(health, model.HealthWarn)
	}
	for _, p := range h.Info.Pools {
		if !poolHealthy(p) {
			health = worse(health, model.HealthCrit)
		}
	}
	return health
}

// attentionOf raises, per host: ssh:host:<name> for a host that is unreachable or refuses dash's
// key (warn) or presents another key (crit); and while it is up ssh:fs:<name>/<mount> for a file
// system at 90 % (warn) or 95 % (crit), ssh:units:<name> for failed systemd units (warn) and
// ssh:pool:<name>/<pool> for a ZFS pool that is not online (crit). An unverified host raises
// nothing: the Hosts page asks to trust it. Every item points at the host's page. The host's
// own item carries the time it entered its state; the others are stamped by the supervisor.
func attentionOf(hosts []sshmodel.Host) []model.AttentionItem {
	items := []model.AttentionItem{}
	for _, h := range hosts {
		add := func(id string, sev model.Health, title, detail string, since time.Time) {
			items = append(items, model.AttentionItem{
				ID: id, Severity: model.Severity(sev), Source: sourceSSH, Plugin: string(ID), Title: title, Detail: detail,
				Since: since, Route: hostRoute(h.Name), Links: []model.Link{}, Ref: h.Ref,
			})
		}
		switch h.State {
		case sshmodel.StateUnreachable:
			add("ssh:host:"+h.Name, model.HealthWarn, fmt.Sprintf("%s is unreachable over SSH", h.Name), "", h.Since)
		case sshmodel.StateUnauthorized:
			add("ssh:host:"+h.Name, model.HealthWarn, fmt.Sprintf("%s refuses dash's SSH key", h.Name), "", h.Since)
		case sshmodel.StateKeyChanged:
			add("ssh:host:"+h.Name, model.HealthCrit, fmt.Sprintf("%s presents another SSH host key", h.Name), "dash refuses the connection until the key is checked", h.Since)
		}
		if h.State != sshmodel.StateUp || h.Info == nil {
			continue
		}
		for _, f := range h.Info.Filesystems {
			if sev := fsHealth(f); sev != model.HealthOK {
				pct := float64(f.UsedBytes) / float64(f.TotalBytes) * 100
				add("ssh:fs:"+h.Name+"/"+f.Mount, sev, fmt.Sprintf("%s on %s is %.0f%% full", f.Mount, h.Name, pct), "", time.Time{})
			}
		}
		if s := h.Info.Services; s != nil && len(s.Failed) > 0 {
			title := fmt.Sprintf("%d failed systemd units on %s", len(s.Failed), h.Name)
			if len(s.Failed) == 1 {
				title = "A failed systemd unit on " + h.Name
			}
			add("ssh:units:"+h.Name, model.HealthWarn, title, strings.Join(s.Failed, ", "), time.Time{})
		}
		for _, p := range h.Info.Pools {
			if !poolHealthy(p) {
				add("ssh:pool:"+h.Name+"/"+p.Name, model.HealthCrit, fmt.Sprintf("ZFS pool %s on %s is %s", p.Name, h.Name, p.Health), "", time.Time{})
			}
		}
	}
	return items
}

// historyRoute registers GET /hosts/{name}/history: a host's samples of the last hour, 404 for a
// name that is no configured host. of answers for a host name.
func historyRoute(r plugin.Routes, of func(name string) (sshmodel.History, bool)) {
	r.Get("/hosts/{name}/history", func(w http.ResponseWriter, req *http.Request) {
		h, ok := of(req.PathValue("name"))
		if !ok {
			plugin.Fail(w, req, errUnknownHost)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(h)
	}, plugin.RouteSpec{
		Summary:  "A host's samples of the last hour, oldest first (kept in memory only)",
		Scope:    "read",
		Response: sshmodel.History{},
	})
}

var errUnknownHost = &plugin.ActionError{Status: http.StatusNotFound, Code: model.ErrCodeNotFound, Message: "unknown host"}

// Context implements plugin.Objects: a host's facts add its inventory and its latest sample, as
// info.<name> and stats.<name>, which the store's facts leave out.
func (p *Plugin) Context(_ context.Context, ref string, _ model.ContextOptions) (model.ContextPart, error) {
	return hostContext(&p.ObjectStore, ref)
}

func hostContext(s *plugin.ObjectStore, ref string) (model.ContextPart, error) {
	part, obj, err := s.Part(ref)
	if err != nil {
		return part, err
	}
	h, ok := obj.(sshmodel.Host)
	if !ok {
		return part, nil
	}
	if i := h.Info; i != nil {
		for _, f := range []struct{ name, value string }{
			{"hostname", i.Hostname}, {"os", i.OS}, {"kernel", i.Kernel}, {"arch", i.Arch}, {"vendor", i.Vendor},
			{"product", i.Product}, {"virtualization", i.Virtualization}, {"cpuModel", i.CPUModel},
		} {
			if f.value != "" {
				part.Facts = append(part.Facts, model.ContextFact{Name: "info." + f.name, Value: f.value})
			}
		}
		if i.CPUs > 0 {
			part.Facts = append(part.Facts, model.ContextFact{Name: "info.cpus", Value: strconv.Itoa(i.CPUs)})
		}
		for _, f := range i.Filesystems {
			part.Facts = append(part.Facts, model.ContextFact{Name: "info.filesystem." + f.Mount, Value: usage(f.UsedBytes, f.TotalBytes)})
		}
		for _, p := range i.Pools {
			part.Facts = append(part.Facts, model.ContextFact{Name: "info.pool." + p.Name, Value: p.Health + ", " + usage(p.AllocBytes, p.SizeBytes)})
		}
		if c := i.Containers; c != nil {
			v := strconv.Itoa(len(c.Items))
			if c.Denied {
				v = "denied"
			}
			part.Facts = append(part.Facts, model.ContextFact{Name: "info.containers." + c.Runtime, Value: v})
		}
		if sv := i.Services; sv != nil {
			part.Facts = append(part.Facts,
				model.ContextFact{Name: "info.services.running", Value: strconv.Itoa(sv.Running)},
				model.ContextFact{Name: "info.services.failed", Value: strings.Join(sv.Failed, ", ")})
		}
	}
	if st := h.Stats; st != nil {
		f := func(name string, v float64) {
			part.Facts = append(part.Facts, model.ContextFact{Name: "stats." + name, Value: strconv.FormatFloat(v, 'g', 6, 64)})
		}
		f("cpuRatio", st.CPURatio)
		f("ioWaitRatio", st.IOWaitRatio)
		f("load1", st.Load1)
		f("load5", st.Load5)
		f("load15", st.Load15)
		f("memUsedBytes", float64(st.MemUsedBytes))
		f("memTotalBytes", float64(st.MemTotalBytes))
		f("swapUsedBytes", float64(st.SwapUsedBytes))
		f("uptimeSeconds", float64(st.UptimeSeconds))
		f("netRxBytesPerSec", st.NetRxBytesPerSec)
		f("netTxBytesPerSec", st.NetTxBytesPerSec)
		f("diskReadBytesPerSec", st.DiskReadBytesPerSec)
		f("diskWriteBytesPerSec", st.DiskWriteBytesPerSec)
		if st.CPUTempC != nil {
			f("cpuTempC", *st.CPUTempC)
		}
	}
	return part, nil
}

// usage is "<used> of <total> bytes (<n>%)".
func usage(used, total int64) string {
	if total <= 0 {
		return strconv.FormatInt(used, 10) + " bytes"
	}
	return fmt.Sprintf("%d of %d bytes (%.0f%%)", used, total, float64(used)/float64(total)*100)
}
