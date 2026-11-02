package proxmox

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
)

func TestRouteIsDeclaredForTheAPI(t *testing.T) {
	routes := plugin.CollectRoutes(New().(*Plugin))
	if len(routes) != 1 {
		t.Fatalf("routes = %d, want one", len(routes))
	}
	r := routes[0]
	if r.Pattern != "/vms/{vmid}" || r.Stream != nil || r.Handler == nil {
		t.Errorf("route = %+v", r)
	}
	if r.Spec.Scope != "read" || r.Spec.Summary == "" || r.Spec.Namespace != nil {
		t.Errorf("spec = %+v: want the read scope, a summary and no namespace (a VM has none)", r.Spec)
	}
	if _, ok := r.Spec.Response.(model.VMDetail); !ok {
		t.Errorf("response = %T", r.Spec.Response)
	}
}

type apiError struct {
	Error string `json:"error"`
	Code  string `json:"code"`
}

func errBody(t *testing.T, w *httptest.ResponseRecorder) apiError {
	t.Helper()
	var e apiError
	if err := json.Unmarshal(w.Body.Bytes(), &e); err != nil {
		t.Fatalf("body %q is not an error envelope: %v", w.Body.String(), err)
	}
	return e
}

func TestVMDetailAnswers(t *testing.T) {
	e := newEnv(t, envOpts{})
	e.vms(t, "the guests", func(d model.VMsData) bool { return len(d.VMs) == 4 })
	w := e.get(t, "/vms/101")
	if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("status %d, content type %q: %s", w.Code, w.Header().Get("Content-Type"), w.Body)
	}
	var d model.VMDetail
	if err := json.Unmarshal(w.Body.Bytes(), &d); err != nil {
		t.Fatal(err)
	}
	if d.VM.Ref != "proxmox:VM/101" || d.VM.Name != "k8s-cp-1" || d.VM.K8sNode == nil {
		t.Errorf("vm = %+v", d.VM)
	}
	if d.Config.Cores != 4 || d.Config.MemoryMiB != 8192 || len(d.Config.Disks) != 1 || len(d.Config.NICs) != 1 {
		t.Errorf("config = %+v", d.Config)
	}
	if len(d.IPs) != 1 || d.IPs[0].Address != "192.0.2.21" {
		t.Errorf("ips = %+v: the loopback address is not listed", d.IPs)
	}
	if len(d.Snapshots) != 1 || d.Snapshots[0].Name != "pre-upgrade" || d.Snapshots[0].Time == nil {
		t.Errorf("snapshots = %+v: the current pseudo entry is not listed", d.Snapshots)
	}
}

// The body is what v0.7.0 answered at /api/vms/{vmid}, once vm.ref and vm.critical, the two fields
// the leaf adds, are dropped. The golden file is that answer for the fake cluster.
func TestVMDetailBodyMatchesV07(t *testing.T) {
	e := newEnv(t, envOpts{})
	e.vms(t, "the guests", func(d model.VMsData) bool { return len(d.VMs) == 4 })
	w := e.get(t, "/vms/101")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	golden, err := os.ReadFile("testdata/vmdetail_v07.json")
	if err != nil {
		t.Fatal(err)
	}
	var want, got map[string]any
	if err := json.Unmarshal([]byte(strings.ReplaceAll(string(golden), "{{URL}}", e.pve.URL)), &want); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	vm := got["vm"].(map[string]any)
	for _, added := range []string{"ref", "critical"} {
		if _, ok := vm[added]; !ok {
			t.Errorf("vm.%s is missing from the body", added)
		}
		delete(vm, added)
	}
	if !reflect.DeepEqual(got, want) {
		g, _ := json.MarshalIndent(got, "", " ")
		wn, _ := json.MarshalIndent(want, "", " ")
		t.Errorf("body differs from v0.7.0's:\n%s\nwant\n%s", g, wn)
	}
}

func TestVMDetailErrors(t *testing.T) {
	pve := newFakePVE(t, false)
	e := newEnv(t, envOpts{pve: pve})
	e.vms(t, "the guests", func(d model.VMsData) bool { return len(d.VMs) == 4 })

	for _, tc := range []struct {
		name, path string
		status     int
		code       string
	}{
		{"not digits", "/vms/abc", 400, "invalid_input"},
		{"ten digits", "/vms/1234567890", 400, "invalid_input"},
		{"an injected id", "/vms/1%3Bdrop", 400, "invalid_input"},
		{"an unknown id", "/vms/999", 404, "not_found"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := e.get(t, tc.path)
			if w.Code != tc.status || errBody(t, w).Code != tc.code {
				t.Errorf("status %d, body %s; want %d %s", w.Code, w.Body, tc.status, tc.code)
			}
		})
	}
	// Nothing was asked of Proxmox about a VM: the polls address no single guest.
	if paths := pve.pathsWith("/qemu/"); len(paths) != 0 {
		t.Errorf("Proxmox was asked for %v", paths)
	}

	// A failure of Proxmox is a 502 and never carries its detail.
	pve.fail("/nodes/pve-1/qemu/101/config", http.StatusInternalServerError)
	w := e.get(t, "/vms/101")
	if w.Code != http.StatusBadGateway {
		t.Errorf("a failing Proxmox answered %d: %s", w.Code, w.Body)
	}
	if strings.Contains(w.Body.String(), pve.URL) || strings.Contains(w.Body.String(), "127.0.0.1") {
		t.Errorf("the 502 names the upstream: %s", w.Body)
	}
	// A guest the poll has never seen is still not asked about.
	if paths := pve.pathsWith("/qemu/999"); len(paths) != 0 {
		t.Errorf("Proxmox was asked about an unknown VM: %v", paths)
	}
}

// Before the first poll there is no VM to describe: 404, not a hang.
func TestVMDetailBeforeAnyPollIsNotFound(t *testing.T) {
	pve := newFakePVE(t, false)
	pve.fail("/cluster/resources", http.StatusServiceUnavailable)
	e := newEnv(t, envOpts{pve: pve})
	e.state(t, plugin.StateDegraded)
	if w := e.get(t, "/vms/101"); w.Code != http.StatusNotFound || errBody(t, w).Code != "not_found" {
		t.Errorf("status %d: %s", w.Code, w.Body)
	}
}

// The route follows the instance: a request that races a stopped plugin gets no answer from it.
func TestVMDetailOnAPluginThatIsNotRunning(t *testing.T) {
	p := New().(*Plugin)
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/vms/101", nil)
	r.SetPathValue("vmid", "101")
	p.vmDetail(w, r)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status %d: %s", w.Code, w.Body)
	}
}

// No audit record: the harness of the API writes one for an action or a stream, and this route is
// neither. Through the real guard-less router the handler writes nothing but its answer.
func TestVMDetailWritesNothingButItsAnswer(t *testing.T) {
	e := newEnv(t, envOpts{})
	e.vms(t, "the guests", func(d model.VMsData) bool { return len(d.VMs) == 4 })
	h, _ := e.sup.Router(ID, plugin.RouteGuard{})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/plugins/proxmox/vms/101", nil))
	if w.Code != http.StatusNotFound && w.Code != http.StatusMethodNotAllowed {
		t.Errorf("a POST answered %d", w.Code)
	}
}
