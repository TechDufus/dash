package jsonschema

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/TechDufus/dash/internal/model"
	hwmodel "github.com/TechDufus/dash/internal/plugins/hardware/model"
	sshmodel "github.com/TechDufus/dash/internal/plugins/ssh/model"
)

// Two types of one Go name in two packages named model are told apart by the shortest tail of
// their package paths, never by the whole path, which would carry the module's owner into every
// generated document. A name that is unique stays the bare Go name.
func TestCollidingNamesTakeTheShortestPackageTail(t *testing.T) {
	r := New(WithRefPrefix("#/$defs/"))
	r.Type(reflect.TypeFor[struct {
		A model.HostsData        `json:"a"`
		B sshmodel.HostsData     `json:"b"`
		C hwmodel.MachinesData   `json:"c"`
		D sshmodel.Info          `json:"d"`
		E []sshmodel.Container   `json:"e"`
		F []model.SourceStatus   `json:"f"`
		G sshmodel.History       `json:"g"`
		H []hwmodel.MachineStats `json:"h"`
	}]())
	names := slices.Sorted(func(yield func(string) bool) {
		for n := range r.Defs() {
			if !yield(n) {
				return
			}
		}
	})
	for _, want := range []string{"internal.model.HostsData", "ssh.model.HostsData", "MachinesData", "History"} {
		if !slices.Contains(names, want) {
			t.Errorf("no definition %q in %v", want, names)
		}
	}
	for _, n := range names {
		if strings.Contains(strings.ToLower(n), "github.com") {
			t.Errorf("definition %q carries the module path", n)
		}
	}
}
