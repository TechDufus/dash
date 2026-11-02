package kubernetes

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	kubefake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/TechDufus/dash/internal/kube"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/pluginconf"
)

// dtHost is the part of plugin.Host the pod detail reads.
type dtHost struct {
	plugin.Host
	now time.Time
}

func (h dtHost) Links() plugin.LinkResolver { return clStubLinks{} }
func (h dtHost) Now() time.Time             { return h.now }

// dtPlugin is a running plugin over c, as Start leaves it for the routes.
func dtPlugin(c *kube.Cluster, usage usageSnap) *Plugin {
	p := &Plugin{}
	p.rt.Store(&runtime{h: dtHost{now: clNow}, cl: c, cfg: pluginconf.Kubernetes{Conventions: clConv}, usage: usage})
	return p
}

// dtGet calls the pod route as the API does: the path values set, no core in front.
func dtGet(t *testing.T, p *Plugin, ns, name string) *httptest.ResponseRecorder {
	t.Helper()
	routes := plugin.CollectRoutes(p)
	if len(routes) != 1 {
		t.Fatalf("routes = %d, want one", len(routes))
	}
	req := httptest.NewRequest(http.MethodGet, "/pods/ns/name", nil)
	req.SetPathValue("ns", ns)
	req.SetPathValue("name", name)
	w := httptest.NewRecorder()
	routes[0].Handler(w, req)
	return w
}

func dtError(t *testing.T, w *httptest.ResponseRecorder) model.APIError {
	t.Helper()
	var e model.APIError
	if err := json.Unmarshal(w.Body.Bytes(), &e); err != nil {
		t.Fatalf("body %q is not an error envelope: %v", w.Body.String(), err)
	}
	return e
}

// dtEventsAPI makes the fake API server answer event lists the way a real one does: by the field
// selector of the request. It returns the selector of the last list, as text.
func dtEventsAPI(c *kube.Cluster, events []corev1.Event, failWith error) func() string {
	var last string
	c.Client.(*kubefake.Clientset).PrependReactor("list", "events", func(a k8stesting.Action) (bool, k8sruntime.Object, error) {
		sel := a.(k8stesting.ListAction).GetListRestrictions().Fields
		last = sel.String()
		if failWith != nil {
			return true, nil, failWith
		}
		out := &corev1.EventList{}
		for _, e := range events {
			if sel.Matches(fields.Set{"involvedObject.kind": e.InvolvedObject.Kind, "involvedObject.name": e.InvolvedObject.Name}) {
				out.Items = append(out.Items, e)
			}
		}
		return true, out, nil
	})
	return func() string { return last }
}

func dtEvent(name, pod, reason string, ago time.Duration) corev1.Event {
	return corev1.Event{
		ObjectMeta:     metav1.ObjectMeta{Namespace: "media", Name: name},
		InvolvedObject: corev1.ObjectReference{Kind: "Pod", Name: pod},
		Reason:         reason, Message: reason + " happened", Type: "Normal",
		LastTimestamp: metav1.NewTime(clNow.Add(-ago)),
	}
}

func dtPod() *corev1.Pod {
	p := runningPod("media", "app-abc-1", "cp-1")
	p.OwnerReferences = controllerOf("ReplicaSet", "app-abc")
	p.Status.PodIP = "198.51.100.7"
	p.Status.Conditions = []corev1.PodCondition{
		{Type: corev1.PodReady, Status: corev1.ConditionTrue},
		{Type: corev1.ContainersReady, Status: corev1.ConditionFalse},
	}
	p.Status.ContainerStatuses = []corev1.ContainerStatus{{
		Name: "app", Ready: false, RestartCount: 5,
		State:                corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}},
		LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "Error", ExitCode: 137}},
	}}
	return p
}

func dtRS() *appsv1.ReplicaSet {
	return &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "app-abc", OwnerReferences: controllerOf("Deployment", "app")}}
}

func TestRouteIsDeclaredForTheAPI(t *testing.T) {
	routes := plugin.CollectRoutes(&Plugin{})
	if len(routes) != 1 {
		t.Fatalf("routes = %d, want one", len(routes))
	}
	r := routes[0]
	if r.Pattern != "/pods/{ns}/{name}" || r.Stream != nil || r.Handler == nil {
		t.Errorf("route = %+v", r)
	}
	if r.Spec.Scope != "read" || r.Spec.Summary != "The detail of one pod: containers, conditions, owner chain and events" {
		t.Errorf("spec = %+v", r.Spec)
	}
	if r.Spec.Namespace != nil {
		t.Error("the route names a namespace: the pod sheet would be refused in a namespace the restart action denies")
	}
	if _, ok := r.Spec.Response.(model.PodDetail); !ok {
		t.Errorf("response = %T", r.Spec.Response)
	}
}

func TestPodDetailAnswers(t *testing.T) {
	c := testCluster(t, nil, dtPod(), dtRS(), deployment("media", "app", 1, 0))
	selector := dtEventsAPI(c, []corev1.Event{
		dtEvent("old", "app-abc-1", "Pulled", 10*time.Minute),
		dtEvent("new", "app-abc-1", "BackOff", time.Minute),
		dtEvent("other", "another-pod", "Killing", 2*time.Minute),
	}, nil)
	usage := usageSnap{podCPU: map[string]float64{"media/app-abc-1": 0.5}}

	w := dtGet(t, dtPlugin(c, usage), "media", "app-abc-1")
	if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("status %d, content type %q: %s", w.Code, w.Header().Get("Content-Type"), w.Body)
	}
	var d model.PodDetail
	if err := json.Unmarshal(w.Body.Bytes(), &d); err != nil {
		t.Fatal(err)
	}
	if d.Pod.Ref != "kubernetes:Pod/media/app-abc-1" || d.Pod.Health != model.HealthCrit || d.Pod.Reason != "CrashLoopBackOff" ||
		d.Pod.Owner == nil || d.Pod.Owner.Name != "app" || d.Pod.CPUCores == nil || *d.Pod.CPUCores != 0.5 {
		t.Errorf("pod = %+v", d.Pod)
	}
	if len(d.Pod.Links) != 1 || d.Pod.Links[0].Href != "https://tools.example.com/Pod/media/app-abc-1" {
		t.Errorf("pod links = %+v", d.Pod.Links)
	}
	if want := []string{"198.51.100.7"}; !reflect.DeepEqual(d.IPs, want) {
		t.Errorf("ips = %v, want %v", d.IPs, want)
	}
	if want := []model.Condition{{Type: "Ready", Status: "True"}, {Type: "ContainersReady", Status: "False"}}; !reflect.DeepEqual(d.Conditions, want) {
		t.Errorf("conditions = %+v", d.Conditions)
	}
	want := []model.Container{{Name: "app", Image: "example.com/app:1", State: "waiting: CrashLoopBackOff", Restarts: 5, LastTerminationReason: "Error (exit 137)"}}
	if !reflect.DeepEqual(d.Containers, want) {
		t.Errorf("containers = %+v, want %+v", d.Containers, want)
	}
	if want := []model.OwnerRef{{Kind: "ReplicaSet", Name: "app-abc"}, {Kind: "Deployment", Name: "app"}}; !reflect.DeepEqual(d.OwnerChain, want) {
		t.Errorf("owner chain = %+v, want %+v", d.OwnerChain, want)
	}
	if len(d.Events) != 2 || d.Events[0].Reason != "BackOff" || d.Events[1].Reason != "Pulled" || d.Events[0].Count != 1 {
		t.Errorf("events = %+v, want this pod's two events, newest first, Normal ones included", d.Events)
	}
	if got := selector(); got != "involvedObject.kind=Pod,involvedObject.name=app-abc-1" {
		t.Errorf("field selector = %q", got)
	}
}

func TestPodDetailPodIPsWinOverPodIP(t *testing.T) {
	p := runningPod("media", "dual", "cp-1")
	p.Status.PodIP = "198.51.100.1"
	p.Status.PodIPs = []corev1.PodIP{{IP: "198.51.100.1"}, {IP: "2001:db8::1"}}
	c := testCluster(t, nil, p)
	dtEventsAPI(c, nil, nil)
	d, err := podDetailOf(t.Context(), clusterInput{c: c, now: clNow}, "media", "dual")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"198.51.100.1", "2001:db8::1"}; !reflect.DeepEqual(d.IPs, want) {
		t.Errorf("ips = %v, want %v", d.IPs, want)
	}
}

func TestPodDetailOfABarePodHasEmptyLists(t *testing.T) {
	c := testCluster(t, nil, runningPod("media", "bare", "cp-1"))
	dtEventsAPI(c, nil, nil)
	w := dtGet(t, dtPlugin(c, usageSnap{}), "media", "bare")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"ips", "conditions", "ownerChain", "events"} {
		if string(raw[k]) != "[]" {
			t.Errorf("%s = %s, want []", k, raw[k])
		}
	}
}

func TestPodDetailWithoutEventsStillAnswers(t *testing.T) {
	c := testCluster(t, nil, dtPod(), dtRS())
	dtEventsAPI(c, nil, errors.New("events are forbidden"))
	w := dtGet(t, dtPlugin(c, usageSnap{}), "media", "app-abc-1")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	var d model.PodDetail
	if err := json.Unmarshal(w.Body.Bytes(), &d); err != nil {
		t.Fatal(err)
	}
	if d.Events == nil || len(d.Events) != 0 || d.Pod.Name != "app-abc-1" {
		t.Errorf("detail = %+v, want the pod and an empty event list", d)
	}
}

func TestPodDetailUnknownPodIs404(t *testing.T) {
	c := testCluster(t, nil, runningPod("media", "here", "cp-1"))
	p := dtPlugin(c, usageSnap{})
	for _, tc := range []struct{ ns, name string }{{"media", "gone"}, {"nowhere", "here"}} {
		w := dtGet(t, p, tc.ns, tc.name)
		if e := dtError(t, w); w.Code != http.StatusNotFound || e.Code != model.ErrCodeNotFound || e.Error != "pod not found" {
			t.Errorf("%s/%s: status %d, %+v", tc.ns, tc.name, w.Code, e)
		}
	}
}

func TestPodDetailRejectsBadNamesBeforeReadingAnything(t *testing.T) {
	for _, tc := range []struct{ ns, name string }{
		{"Media", "pod"}, {"media", "Pod_1"}, {"media", "-x"}, {"me dia", "pod"}, {"media", "a_b"},
	} {
		// A plugin that does not run answers 400 too: the names are checked first.
		for _, p := range []*Plugin{{}, dtPlugin(testCluster(t, nil), usageSnap{})} {
			w := dtGet(t, p, tc.ns, tc.name)
			if e := dtError(t, w); w.Code != http.StatusBadRequest || e.Code != model.ErrCodeInvalidInput || e.Error != "invalid namespace or pod name" {
				t.Errorf("%q/%q: status %d, %+v", tc.ns, tc.name, w.Code, e)
			}
		}
	}
}

func TestPodDetailWithoutARunningPluginIs503(t *testing.T) {
	w := dtGet(t, &Plugin{}, "media", "pod")
	if e := dtError(t, w); w.Code != http.StatusServiceUnavailable || e.Code != model.ErrCodeUpstreamUnavailable {
		t.Errorf("status %d, %+v", w.Code, e)
	}
}
