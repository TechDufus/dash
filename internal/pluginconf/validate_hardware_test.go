package pluginconf

import (
	"slices"
	"strings"
	"testing"
)

// One host is supported until the plugin handles several; the rule and its message moved here
// from internal/config with the section.
func TestHardwareAtMostOneHost(t *testing.T) {
	if err := (Hardware{Hosts: []HardwareHost{{Nodename: "a"}}}).Validate(); err != nil {
		t.Errorf("one host: %v", err)
	}
	err := Hardware{Hosts: []HardwareHost{{Nodename: "a"}, {Nodename: "b"}}}.Validate()
	if got := paths(err); !slices.Equal(got, []string{"hosts"}) || !strings.Contains(err.Error(), "has 2 entries; one host is supported") {
		t.Errorf("two hosts: %v", err)
	}
}

// An empty scrub prefix is valid (it hides the scrub panel); anything set must be a metric name.
func TestHardwareScrubPrefix(t *testing.T) {
	for _, ok := range []string{"", "node_zfs_scrub", "a:b_c", "_x"} {
		if err := (Hardware{ZFSScrubMetricPrefix: ok}).Validate(); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"a-b", "1abc", "has space", "a.b"} {
		err := Hardware{ZFSScrubMetricPrefix: bad}.Validate()
		if got := paths(err); !slices.Equal(got, []string{"zfsScrubMetricPrefix"}) || !strings.Contains(err.Error(), "is not a metric name") {
			t.Errorf("%q: %v", bad, err)
		}
	}
}

func TestHardwareHooks(t *testing.T) {
	var section any = &Hardware{}
	if _, ok := section.(interface{ Validate() error }); !ok {
		t.Error("*Hardware has no Validate")
	}
	if _, ok := section.(interface{ ActionsConfig() Actions }); ok {
		t.Error("hardware has an action policy")
	}
}
