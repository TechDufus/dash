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
