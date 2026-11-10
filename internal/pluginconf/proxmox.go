package pluginconf

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/TechDufus/dash/internal/secret"
)

// Storage sources of plugins.proxmox.storage.source.
const (
	ProxmoxStorageAPI        = "api"
	ProxmoxStoragePrometheus = "prometheus"
)

// ProxmoxVMSelectorDefault is the series.vmSelector of a section with nothing set: the id label
// of the pve-exporter, with {vmid} standing for the guest's id.
const ProxmoxVMSelectorDefault = `id="qemu/{vmid}"`

// maxVMID is the largest VMID the series arguments accept (queries.ValidVMID: one to nine digits).
const maxVMID = 999_999_999

// CriticalVMs names the guests whose stopping is an incident rather than a warning.
type CriticalVMs struct {
	IDs  []int    `json:"ids" doc:"VM ids that are critical. A stopped critical VM is a critical attention item; any other stopped VM is a warning. A JSON list of integers in the environment."`
	Tags []string `json:"tags" doc:"VM tags that make a VM critical (compared without regard to case). A JSON list in the environment."`
}

// ProxmoxStorage is where the storage list comes from.
type ProxmoxStorage struct {
	Source string `json:"source" dash:"enum=api|prometheus" doc:"Where the storage list comes from: api reads every node's storages from the Proxmox API (the API token needs the Datastore.Audit privilege); prometheus reads the pve-exporter series pve_disk_usage_bytes, pve_disk_size_bytes, pve_storage_info and pve_storage_shared through the prometheus plugin. A shared storage is listed and counted once, except with prometheus and an exporter without pve_storage_shared, which lists it once per node."`
}

// ProxmoxSeries shapes the metric queries behind the VM charts.
type ProxmoxSeries struct {
	VMSelector string `json:"vmSelector" doc:"The label matcher that selects one VM in the pve-exporter series of the VM charts: one label, = or =~, and a quoted value with {vmid} once, e.g. id=\"qemu/{vmid}\"."`
}

// Proxmox is plugins.proxmox: the Proxmox API. TokenID and TokenSecret are the two halves of an
// API token ("user@realm!name" and its secret), so that only the secret is secret.
type Proxmox struct {
	PluginBase
	URL         string         `json:"url" dash:"url" doc:"Base URL of the Proxmox API, e.g. https://pve.example.com:8006. Required with a token; a URL alone leaves Proxmox disabled."`
	Node        string         `json:"node" doc:"The Proxmox node the Hardware page's host is. The page's Proxmox facts, its shell link and its place in the topology come from that node, and its series use the node as their nodename unless plugins.hardware.hosts[0].nodename names another. Empty takes the only node of a one-node cluster; a cluster of several nodes needs it for the Hardware page. dash reads every node of the cluster either way."`
	TokenID     string         `json:"tokenID" doc:"API token ID, user@realm!tokenname. Proxmox runs only with url, tokenID and tokenSecret all set and is disabled with none of the token set. A token ID without tokenSecret, a tokenSecret without tokenID, or a token without url is a configuration error that stops dash at startup."`
	TokenSecret secret.Value   `json:"tokenSecret" dash:"secret" doc:"API token secret: a literal, env:NAME, file:/path or k8s:<namespace>/<name>#<key> (read from that Kubernetes Secret by the kubernetes plugin, which must run and be allowed to read it). Set together with tokenID: one without the other is a configuration error."`
	TLS         TLS            `json:"tls" doc:"How dash verifies the Proxmox API's certificate."`
	CriticalVMs CriticalVMs    `json:"criticalVMs" doc:"Which VMs are critical. No VM is critical by default."`
	Storage     ProxmoxStorage `json:"storage" doc:"Where the Proxmox storage list comes from."`
	Series      ProxmoxSeries  `json:"series" doc:"How the VM chart queries select a VM."`
}

// DefaultProxmox is the section with nothing set.
func DefaultProxmox() Proxmox {
	return Proxmox{
		Storage: ProxmoxStorage{Source: ProxmoxStorageAPI},
		Series:  ProxmoxSeries{VMSelector: ProxmoxVMSelectorDefault},
	}
}

// StorageSource is the configured source, api when none is set.
func (p Proxmox) StorageSource() string {
	if p.Storage.Source == "" {
		return ProxmoxStorageAPI
	}
	return p.Storage.Source
}

// VMSelector is the configured selector, the default when none is set.
func (p Proxmox) VMSelector() string {
	if p.Series.VMSelector == "" {
		return ProxmoxVMSelectorDefault
	}
	return p.Series.VMSelector
}

// selectorRE splits one label matcher: a Prometheus label name, = or =~, and a quoted value.
var selectorRE = regexp.MustCompile(`^([A-Za-z_][A-Za-z0-9_]*)(=~|=)"(.*)"$`)

// vmidMark is where the guest's id goes in a selector.
const vmidMark = "{vmid}"

// ValidVMSelector reports why s is not one label matcher with a single {vmid}. The selector is
// put into PromQL, so it must not be able to carry more than that matcher: the value may not
// hold a quote, a backslash or a brace besides the one {vmid}. The reason never repeats s.
func ValidVMSelector(s string) error {
	m := selectorRE.FindStringSubmatch(s)
	if m == nil {
		return errors.New(`is not one label matcher: write <label>="<value>" or <label>=~"<value>"`)
	}
	value := m[3]
	if strings.Count(value, vmidMark) != 1 {
		return fmt.Errorf("the value must contain %s exactly once", vmidMark)
	}
	if strings.ContainsAny(strings.Replace(value, vmidMark, "", 1), `"\{}`) {
		return fmt.Errorf(`the value may not contain ", \, { or } besides %s`, vmidMark)
	}
	return nil
}

// Validate checks that the two halves of the token come together and that a token has a URL,
// then the rules of the settings the schema cannot express.
func (p Proxmox) Validate() error {
	var pr problems
	if (p.TokenID == "") != !p.TokenSecret.Present() {
		pr.add("tokenSecret", "tokenID and tokenSecret must be set together")
	}
	if p.TokenID != "" && p.URL == "" {
		pr.add("url", "is required with a Proxmox token")
	}
	switch p.Storage.Source {
	case "", ProxmoxStorageAPI, ProxmoxStoragePrometheus:
	default:
		pr.add("storage.source", "must be one of: %s, %s", ProxmoxStorageAPI, ProxmoxStoragePrometheus)
	}
	for i, id := range p.CriticalVMs.IDs {
		if id < 1 || id > maxVMID {
			pr.add("criticalVMs.ids", "entry %d is not a VM id (1 to %d)", i, maxVMID)
		}
	}
	for i, t := range p.CriticalVMs.Tags {
		if strings.TrimSpace(t) == "" {
			pr.add("criticalVMs.tags", "entry %d is empty", i)
		}
	}
	if p.Series.VMSelector != "" {
		if err := ValidVMSelector(p.Series.VMSelector); err != nil {
			pr.add("series.vmSelector", "%v", err)
		}
	}
	return pr.err()
}
