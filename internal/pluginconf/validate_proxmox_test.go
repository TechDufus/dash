package pluginconf

import (
	"slices"
	"strings"
	"testing"

	"github.com/TechDufus/dash/internal/secret"
)

// The token has two halves that come together, and a token needs a URL: the rules that lived
// in internal/config before the section moved, with the same paths and messages.
func TestProxmoxTokenRules(t *testing.T) {
	tok := secret.New("s3cret-0123456789", "")
	pending := secret.New("", "k8s:dash/pve#token") // a reference the kubernetes plugin has not read yet
	for _, tc := range []struct {
		name string
		p    Proxmox
		want []string
		msg  string
	}{
		{"nothing set is fine", Proxmox{}, nil, ""},
		{"url alone is fine", Proxmox{URL: "https://pve.example.com:8006"}, nil, ""},
		{"a full token with a url", Proxmox{URL: "https://pve.example.com:8006", TokenID: "dash@pve!ci", TokenSecret: tok}, nil, ""},
		{"an id without a secret", Proxmox{URL: "https://pve.example.com:8006", TokenID: "dash@pve!ci"}, []string{"tokenSecret"}, "tokenID and tokenSecret must be set together"},
		{"a secret without an id", Proxmox{URL: "https://pve.example.com:8006", TokenSecret: tok}, []string{"tokenSecret"}, "tokenID and tokenSecret must be set together"},
		{"a token without a url", Proxmox{TokenID: "dash@pve!ci", TokenSecret: tok}, []string{"url"}, "is required with a Proxmox token"},
		{"an id alone reports both", Proxmox{TokenID: "dash@pve!ci"}, []string{"tokenSecret", "url"}, ""},
		{"a pending secret counts as given", Proxmox{URL: "https://pve.example.com:8006", TokenID: "dash@pve!ci", TokenSecret: pending}, nil, ""},
		{"a pending secret without an id", Proxmox{URL: "https://pve.example.com:8006", TokenSecret: pending}, []string{"tokenSecret"}, "tokenID and tokenSecret must be set together"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.p.Validate()
			if got := paths(err); !slices.Equal(got, tc.want) {
				t.Fatalf("paths = %v, want %v (%v)", got, tc.want, err)
			}
			if tc.msg != "" && (err == nil || !strings.Contains(err.Error(), tc.msg)) {
				t.Errorf("error = %v, want %q", err, tc.msg)
			}
		})
	}
}

// The section is usable through a pointer, which is how the supervisor holds it, and carries no
// action policy or read deny list: it has no action and no read route that takes a namespace.
func TestProxmoxHooks(t *testing.T) {
	var section any = &Proxmox{}
	if _, ok := section.(interface{ Validate() error }); !ok {
		t.Error("*Proxmox has no Validate")
	}
	if _, ok := section.(interface{ ActionsConfig() Actions }); ok {
		t.Error("proxmox has an action policy")
	}
	if _, ok := section.(interface{ ReadDenyConfig() []string }); ok {
		t.Error("proxmox has a read deny list")
	}
}

func TestDefaultProxmoxIsValidAndSelfDescribing(t *testing.T) {
	d := DefaultProxmox()
	if err := d.Validate(); err != nil {
		t.Fatalf("the default section is invalid: %v", err)
	}
	if d.Storage.Source != "api" || d.Series.VMSelector != `id="qemu/{vmid}"` || len(d.CriticalVMs.IDs) != 0 || len(d.CriticalVMs.Tags) != 0 {
		t.Errorf("defaults = %+v", d)
	}
	// The zero value is valid too, and means the defaults.
	var z Proxmox
	if z.StorageSource() != "api" || z.VMSelector() != `id="qemu/{vmid}"` {
		t.Errorf("zero value: %q %q", z.StorageSource(), z.VMSelector())
	}
}

func TestProxmoxStorageSourceRule(t *testing.T) {
	for _, tc := range []struct {
		source string
		ok     bool
	}{{"", true}, {"api", true}, {"prometheus", true}, {"API", false}, {"graphite", false}} {
		err := Proxmox{Storage: ProxmoxStorage{Source: tc.source}}.Validate()
		if (err == nil) != tc.ok {
			t.Errorf("storage.source %q: err = %v", tc.source, err)
		}
		if err != nil && !slices.Equal(paths(err), []string{"storage.source"}) {
			t.Errorf("storage.source %q: paths = %v", tc.source, paths(err))
		}
	}
}

func TestProxmoxCriticalVMsRules(t *testing.T) {
	if err := (Proxmox{CriticalVMs: CriticalVMs{IDs: []int{1, 2020, 999_999_999}, Tags: []string{"prod", "Core"}}}).Validate(); err != nil {
		t.Fatalf("valid ids and tags: %v", err)
	}
	for _, ids := range [][]int{{0}, {-5}, {1_000_000_000}, {7, 0}} {
		err := Proxmox{CriticalVMs: CriticalVMs{IDs: ids}}.Validate()
		if !slices.Equal(paths(err), []string{"criticalVMs.ids"}) {
			t.Errorf("ids %v: paths = %v", ids, paths(err))
		}
	}
	for _, tags := range [][]string{{""}, {"ok", "  "}} {
		err := Proxmox{CriticalVMs: CriticalVMs{Tags: tags}}.Validate()
		if !slices.Equal(paths(err), []string{"criticalVMs.tags"}) {
			t.Errorf("tags %q: paths = %v", tags, paths(err))
		}
	}
}

// The selector goes into PromQL: exactly one matcher, exactly one {vmid}, nothing else.
func TestProxmoxVMSelectorRules(t *testing.T) {
	for _, sel := range []string{
		`id="qemu/{vmid}"`, `id=~"qemu/{vmid}"`, `vmid="{vmid}"`, `pve_id=~".*/{vmid}"`, `_x1="{vmid}-a"`,
	} {
		if err := (Proxmox{Series: ProxmoxSeries{VMSelector: sel}}).Validate(); err != nil {
			t.Errorf("%q rejected: %v", sel, err)
		}
	}
	for _, sel := range []string{
		`id="qemu/{vmid}"} or vector(1) or {id="x`, // a second matcher smuggled through the quotes
		`id="qemu/{vmid}",job="x"`,                 // a second matcher
		`id="{vmid}" or id="y"`,
		`id="qemu/\"{vmid}"`, // a backslash
		`id!="qemu/{vmid}"`,  // only = and =~
		`id!~"qemu/{vmid}"`,
		`{id="qemu/{vmid}"}`, // braces around it
		`id="qemu/{vmid}{vmid}"`,
		`id="qemu/"`,        // no {vmid}
		`id="qemu/{vmid}}"`, // a stray brace
		`id="qemu/{{vmid}"`, // a stray brace
		`id="qemu/{vmid}"x`, // junk after
		`1id="{vmid}"`,      // not a label name
		`id=qemu/{vmid}`,    // unquoted
		`id="{vmid}"` + "\n" + `x`,
	} {
		err := Proxmox{Series: ProxmoxSeries{VMSelector: sel}}.Validate()
		if !slices.Equal(paths(err), []string{"series.vmSelector"}) {
			t.Errorf("%q: paths = %v, want series.vmSelector", sel, paths(err))
		}
	}
	if err := ValidVMSelector(`id="qemu/{vmid}"`); err != nil {
		t.Errorf("ValidVMSelector: %v", err)
	}
}

// tls is the shared section: the same two settings every plugin has.
func TestProxmoxTLSIsTheSharedSection(t *testing.T) {
	var p Proxmox
	p.TLS = TLS{CAFile: "/etc/ssl/pve.pem", InsecureSkipVerify: true}
	if err := p.Validate(); err != nil {
		t.Errorf("tls settings are not validated further: %v", err)
	}
}
