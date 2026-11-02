package kubernetes

import (
	"fmt"
	"reflect"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/TechDufus/dash/internal/kube"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/plugin/plugintest"
	"github.com/TechDufus/dash/internal/pluginconf"
)

var clNow = time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)

var clConv = pluginconf.KubernetesConventions{
	ProtectedLabel: "example.com/protected", TenantLabel: "example.com/tenant", AppsFluxPrefix: "app-",
}

// clStubLinks resolves every request to one link naming it, so a test sees what was asked.
type clStubLinks struct{}

func (clStubLinks) Resolve(r plugin.LinkRequest) []model.Link {
	return []model.Link{{Label: r.Kind, Href: "https://tools.example.com/" + r.Kind + "/" + r.Namespace + "/" + r.Name}}
}

// clBuild is buildCluster over a fake cluster, at clNow.
func clBuild(t *testing.T, conv pluginconf.KubernetesConventions, usage usageSnap, objs ...k8sruntime.Object) model.ClusterData {
	t.Helper()
	c := testCluster(t, nil, objs...)
	return buildCluster(clusterInput{c: c, conv: conv, usage: usage, links: clStubLinks{}, now: clNow})
}

func clPod(d model.ClusterData, ns, name string) (model.Pod, bool) {
	for _, p := range d.Pods {
		if p.Namespace == ns && p.Name == name {
			return p, true
		}
	}
	return model.Pod{}, false
}

func TestClassifyNamespace(t *testing.T) {
	tests := []struct {
		name   string
		conv   pluginconf.KubernetesConventions
		labels map[string]string
		want   nsInfo
	}{
		{"protected and privileged platform", clConv,
			map[string]string{"kustomize.toolkit.fluxcd.io/name": "monitoring", "example.com/protected": "true", "pod-security.kubernetes.io/enforce": "privileged"},
			nsInfo{group: model.GroupPlatform, protected: true, privileged: true}},
		{"protected label must say true", clConv, map[string]string{"example.com/protected": "false"}, nsInfo{group: model.GroupPlatform}},
		{"flux prefix is an app", clConv, map[string]string{"kustomize.toolkit.fluxcd.io/name": "app-homepage"}, nsInfo{group: model.GroupApps}},
		{"another flux name is platform", clConv, map[string]string{"kustomize.toolkit.fluxcd.io/name": "infra-core"}, nsInfo{group: model.GroupPlatform}},
		{"tenant label wins over the flux prefix", clConv,
			map[string]string{"example.com/tenant": "acme", "kustomize.toolkit.fluxcd.io/name": "app-x"},
			nsInfo{group: model.GroupTenants, tenant: "acme"}},
		{"unlabelled is platform", clConv, nil, nsInfo{group: model.GroupPlatform}},
		{"privileged without conventions", pluginconf.KubernetesConventions{},
			map[string]string{"pod-security.kubernetes.io/enforce": "privileged"}, nsInfo{group: model.GroupPlatform, privileged: true}},
		{"empty conventions classify nothing", pluginconf.KubernetesConventions{},
			map[string]string{"example.com/protected": "true", "example.com/tenant": "acme", "kustomize.toolkit.fluxcd.io/name": "app-x"},
			nsInfo{group: model.GroupPlatform}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyNamespace(tc.labels, tc.conv); got != tc.want {
				t.Errorf("classifyNamespace = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestEmptyConventionsHaveNoClassification(t *testing.T) {
	d := clBuild(t, pluginconf.KubernetesConventions{}, usageSnap{},
		namespace("a", map[string]string{"example.com/protected": "true"}),
		namespace("b", map[string]string{"example.com/tenant": "acme"}),
		namespace("c", map[string]string{"kustomize.toolkit.fluxcd.io/name": "app-c"}),
		deployment("a", "web", 1, 1),
	)
	if len(d.Namespaces) != 3 {
		t.Fatalf("namespaces = %+v", d.Namespaces)
	}
	for _, ns := range d.Namespaces {
		if ns.Group != model.GroupPlatform || ns.Protected || ns.Tenant != "" {
			t.Errorf("namespace %s = %+v, want an unclassified platform namespace", ns.Name, ns)
		}
	}
	if len(d.Workloads) != 1 || d.Workloads[0].Protected {
		t.Errorf("workloads = %+v, want one that is not protected", d.Workloads)
	}
}

func TestNamespacesAreClassifiedAndRated(t *testing.T) {
	crash := runningPod("tenant-a", "crash", "cp-1")
	crash.Status.ContainerStatuses = []corev1.ContainerStatus{{
		Name: "app", RestartCount: 3, State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}},
	}}
	done := runningPod("tenant-a", "done", "cp-1")
	done.Status.Phase = corev1.PodSucceeded
	d := clBuild(t, clConv, usageSnap{},
		namespace("tenant-a", map[string]string{"example.com/tenant": "acme"}),
		namespace("app-home", map[string]string{"kustomize.toolkit.fluxcd.io/name": "app-home"}),
		namespace("kube-system", map[string]string{"example.com/protected": "true", "pod-security.kubernetes.io/enforce": "privileged"}),
		runningPod("tenant-a", "ok", "cp-1"), crash, done,
		deployment("app-home", "web", 2, 0),
	)
	by := map[string]model.Namespace{}
	for _, ns := range d.Namespaces {
		by[ns.Name] = ns
	}
	if got := d.Namespaces[0].Name; got != "app-home" {
		t.Errorf("namespaces are not sorted by name: first is %q", got)
	}
	tenant := by["tenant-a"]
	if tenant.Group != model.GroupTenants || tenant.Tenant != "acme" || tenant.PodsTotal != 2 || tenant.PodsRunning != 2 ||
		tenant.Restarts != 3 || tenant.Health != model.HealthCrit || tenant.Ref != "kubernetes:Namespace/tenant-a" {
		t.Errorf("tenant namespace = %+v: want 2 of 3 pods counted (the completed one is not), 3 restarts, crit", tenant)
	}
	home := by["app-home"]
	if home.Group != model.GroupApps || home.WorkloadsNotReady != 1 || home.Health != model.HealthCrit {
		t.Errorf("apps namespace = %+v: want one workload not ready and crit", home)
	}
	sys := by["kube-system"]
	if sys.Group != model.GroupPlatform || !sys.Protected || !sys.Privileged || sys.Health != model.HealthOK {
		t.Errorf("platform namespace = %+v", sys)
	}
}

func TestBuildClusterRefsAreUnique(t *testing.T) {
	cert := obj(kube.GVRCertificates, "gateway", "wild")
	es := obj(kube.GVRExternalSecrets, "gateway", "creds")
	d := clBuild(t, clConv, usageSnap{},
		namespace("media", nil),
		node("cp-1", "192.0.2.11", true),
		deployment("media", "app", 1, 1), statefulSet("media", "app", 1, 1), cronJob("media", "app"),
		runningPod("media", "app-0", "cp-1"),
		pvc("media", "app", 10),
		cert, es,
	)
	var kinds []string
	for _, w := range d.Workloads {
		kinds = append(kinds, w.Ref)
	}
	want := []string{
		"kubernetes:Workload/CronJob/media/app", "kubernetes:Workload/Deployment/media/app", "kubernetes:Workload/StatefulSet/media/app",
	}
	if !reflect.DeepEqual(kinds, want) {
		t.Errorf("workload refs = %v, want %v", kinds, want)
	}
	plugintest.AssertRefs(t, nil, ID, d, model.Node{}, model.Namespace{}, model.Workload{}, model.Pod{}, model.Cert{}, model.ExtSecret{})
	if d.Nodes[0].Ref != "kubernetes:Node/cp-1" || d.Pods[0].Ref != "kubernetes:Pod/media/app-0" ||
		d.Certificates[0].Ref != "kubernetes:Certificate/gateway/wild" || d.ExternalSecrets[0].Ref != "kubernetes:ExternalSecret/gateway/creds" {
		t.Errorf("refs: node %q pod %q cert %q external secret %q", d.Nodes[0].Ref, d.Pods[0].Ref, d.Certificates[0].Ref, d.ExternalSecrets[0].Ref)
	}
}

func TestBuildClusterOfAnEmptyClusterHasNoNilSlices(t *testing.T) {
	d := clBuild(t, clConv, usageSnap{})
	for name, v := range map[string]any{
		"nodes": d.Nodes, "namespaces": d.Namespaces, "workloads": d.Workloads, "pods": d.Pods,
		"events": d.Events, "certificates": d.Certificates, "externalSecrets": d.ExternalSecrets,
	} {
		if rv := reflect.ValueOf(v); rv.IsNil() || rv.Len() != 0 {
			t.Errorf("%s = %#v, want an empty non-nil slice", name, v)
		}
	}
}

func TestPodReasonAndHealth(t *testing.T) {
	waiting := func(reason string) corev1.ContainerState {
		return corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: reason}}
	}
	terminated := func(reason string) corev1.ContainerState {
		return corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: reason}}
	}
	pod := func(phase corev1.PodPhase, age time.Duration, mod func(*corev1.Pod)) *corev1.Pod {
		p := runningPod("ns", "p", "cp-1")
		p.Status.Phase = phase
		p.CreationTimestamp = metav1.NewTime(clNow.Add(-age))
		if mod != nil {
			mod(p)
		}
		return p
	}
	tests := []struct {
		name       string
		pod        *corev1.Pod
		wantReason string
		wantHealth model.Health
	}{
		{"running", pod(corev1.PodRunning, time.Hour, nil), "", model.HealthOK},
		{"crash loop", pod(corev1.PodRunning, time.Hour, func(p *corev1.Pod) {
			p.Status.ContainerStatuses[0].State = waiting("CrashLoopBackOff")
		}), "CrashLoopBackOff", model.HealthCrit},
		{"image pull", pod(corev1.PodPending, time.Minute, func(p *corev1.Pod) {
			p.Status.ContainerStatuses[0].State = waiting("ImagePullBackOff")
		}), "ImagePullBackOff", model.HealthCrit},
		{"an init container waiting comes first", pod(corev1.PodPending, time.Minute, func(p *corev1.Pod) {
			p.Status.InitContainerStatuses = []corev1.ContainerStatus{{Name: "init", State: waiting("CreateContainerConfigError")}}
			p.Status.ContainerStatuses[0].State = waiting("PodInitializing")
		}), "CreateContainerConfigError", model.HealthCrit},
		{"container creating is fine", pod(corev1.PodPending, time.Minute, func(p *corev1.Pod) {
			p.Status.ContainerStatuses[0].State = waiting("ContainerCreating")
		}), "ContainerCreating", model.HealthOK},
		{"pending for exactly five minutes", pod(corev1.PodPending, 5*time.Minute, nil), "", model.HealthOK},
		{"pending over five minutes", pod(corev1.PodPending, 5*time.Minute+time.Second, nil), "", model.HealthWarn},
		{"a failed container of a failed pod", pod(corev1.PodFailed, time.Hour, func(p *corev1.Pod) {
			p.Status.ContainerStatuses[0].State = terminated("OOMKilled")
		}), "OOMKilled", model.HealthOK},
		{"a terminated container of a running pod is not a reason", pod(corev1.PodRunning, time.Hour, func(p *corev1.Pod) {
			p.Status.ContainerStatuses[0].State = terminated("Error")
		}), "", model.HealthOK},
		{"the pod's own reason", pod(corev1.PodFailed, time.Hour, func(p *corev1.Pod) { p.Status.Reason = "Evicted" }), "Evicted", model.HealthOK},
		{"completed", pod(corev1.PodSucceeded, time.Hour, nil), "Completed", model.HealthOK},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := podReason(tc.pod); got != tc.wantReason {
				t.Errorf("podReason = %q, want %q", got, tc.wantReason)
			}
			got := buildPod(tc.pod, clusterInput{now: clNow}, kube.Resolver{})
			if got.Health != tc.wantHealth {
				t.Errorf("health = %s, want %s", got.Health, tc.wantHealth)
			}
		})
	}
}

func TestBuildPodRestartsOwnerAndUsage(t *testing.T) {
	multi := runningPod("media", "app-abc-1", "cp-1")
	multi.OwnerReferences = controllerOf("ReplicaSet", "app-abc")
	multi.Spec.Containers = append(multi.Spec.Containers, corev1.Container{Name: "side", Image: "example.com/side:1"})
	multi.Status.ContainerStatuses = []corev1.ContainerStatus{
		{Name: "app", Ready: true, RestartCount: 2},
		{Name: "side", Ready: false, RestartCount: 1},
	}
	multi.Status.InitContainerStatuses = []corev1.ContainerStatus{{Name: "init", RestartCount: 4}}
	rs := &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "app-abc", OwnerReferences: controllerOf("Deployment", "app")}}
	bare := runningPod("media", "bare", "cp-2")

	usage := usageSnap{podCPU: map[string]float64{"media/app-abc-1": 0.25}, podMem: map[string]float64{"media/app-abc-1": 1 << 20}}
	d := clBuild(t, clConv, usage, multi, bare, rs, deployment("media", "app", 1, 1))

	p, ok := clPod(d, "media", "app-abc-1")
	if !ok {
		t.Fatalf("pods = %+v", d.Pods)
	}
	if p.ReadyContainers != 1 || p.TotalContainers != 2 || p.Restarts != 7 {
		t.Errorf("ready %d of %d, restarts %d; want 1 of 2 with 7 restarts (init containers count)", p.ReadyContainers, p.TotalContainers, p.Restarts)
	}
	if p.Owner == nil || *p.Owner != (model.OwnerRef{Kind: "Deployment", Name: "app"}) {
		t.Errorf("owner = %+v, want the Deployment above the ReplicaSet", p.Owner)
	}
	if p.CPUCores == nil || *p.CPUCores != 0.25 || p.MemBytes == nil || *p.MemBytes != 1<<20 {
		t.Errorf("usage = %v, %v", p.CPUCores, p.MemBytes)
	}
	if p.Node != "cp-1" || p.Phase != "Running" || !p.CreatedAt.Equal(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("pod = %+v", p)
	}
	if len(p.Links) != 1 || p.Links[0].Href != "https://tools.example.com/Pod/media/app-abc-1" {
		t.Errorf("links = %+v", p.Links)
	}
	b, _ := clPod(d, "media", "bare")
	if b.Owner != nil || b.CPUCores != nil || b.MemBytes != nil {
		t.Errorf("bare pod = %+v, want no owner and no usage", b)
	}
}

func TestBuildClusterSortsPodsAndNodes(t *testing.T) {
	d := clBuild(t, clConv, usageSnap{},
		node("cp-2", "192.0.2.12", true), node("cp-1", "192.0.2.11", true),
		runningPod("b", "z", "cp-1"), runningPod("a", "y", "cp-1"), runningPod("a", "x", "cp-1"),
	)
	var pods []string
	for _, p := range d.Pods {
		pods = append(pods, p.Namespace+"/"+p.Name)
	}
	if want := []string{"a/x", "a/y", "b/z"}; !reflect.DeepEqual(pods, want) {
		t.Errorf("pods = %v, want %v", pods, want)
	}
	if d.Nodes[0].Name != "cp-1" || d.Nodes[1].Name != "cp-2" {
		t.Errorf("nodes = %+v", d.Nodes)
	}
}

func TestNodesCarryIPUsageAndPodCounts(t *testing.T) {
	succeeded := runningPod("a", "job", "cp-1")
	succeeded.Status.Phase = corev1.PodSucceeded
	pending := runningPod("a", "wait", "cp-1")
	pending.Status.Phase = corev1.PodPending
	unscheduled := runningPod("a", "none", "")
	usage := usageSnap{
		nodeCPU: map[string]float64{kube.InstanceOf("192.0.2.11"): 0.4},
		nodeMem: map[string]float64{kube.InstanceOf("192.0.2.11"): 0.6},
	}
	d := clBuild(t, clConv, usage,
		node("cp-1", "192.0.2.11", true), node("cp-2", "192.0.2.12", false),
		runningPod("a", "one", "cp-1"), pending, succeeded, unscheduled, runningPod("a", "two", "cp-2"),
	)
	cp1, cp2 := d.Nodes[0], d.Nodes[1]
	if cp1.IP != "192.0.2.11" || !cp1.Ready || cp1.KubeletVersion != "v1.34.1" || cp1.CPURatio != 0.4 || cp1.MemRatio != 0.6 {
		t.Errorf("cp-1 = %+v", cp1)
	}
	if cp1.Pods != 2 {
		t.Errorf("cp-1 pods = %d, want 2: running and pending count, a completed or unscheduled pod does not", cp1.Pods)
	}
	if cp2.IP != "192.0.2.12" || cp2.Ready || cp2.Pods != 1 || cp2.CPURatio != 0 || cp2.MemRatio != 0 {
		t.Errorf("cp-2 = %+v: not ready, one pod, no usage found for it", cp2)
	}
	if len(cp1.Links) != 1 || cp1.Links[0].Href != "https://tools.example.com/Node//cp-1" {
		t.Errorf("links = %+v: a node has no namespace", cp1.Links)
	}
}

func TestWorkloadsOfEveryKind(t *testing.T) {
	sts := statefulSet("a", "db", 3, 1)
	ds := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{Namespace: "a", Name: "agent"},
		Spec:       appsv1.DaemonSetSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "x", Image: "example.com/agent:2"}}}}},
		Status:     appsv1.DaemonSetStatus{NumberReady: 0, DesiredNumberScheduled: 2},
	}
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Namespace: "a", Name: "migrate"},
		Spec:       batchv1.JobSpec{Completions: new(int32(3))},
		Status:     batchv1.JobStatus{Succeeded: 1},
	}
	cron := &batchv1.CronJob{
		ObjectMeta: metav1.ObjectMeta{Namespace: "a", Name: "nightly"},
		Status:     batchv1.CronJobStatus{Active: []corev1.ObjectReference{{Name: "nightly-1"}}},
	}
	d := clBuild(t, clConv, usageSnap{}, namespace("a", map[string]string{"example.com/protected": "true"}),
		deployment("a", "web", 2, 2), sts, ds, job, cron)
	want := []model.Workload{
		{Ref: "kubernetes:Workload/CronJob/a/nightly", Kind: model.WorkloadKindCronJob, Namespace: "a", Name: "nightly", Ready: 1, Desired: 0, Images: []string{}, Health: model.HealthOK, Protected: true},
		{Ref: "kubernetes:Workload/DaemonSet/a/agent", Kind: model.WorkloadKindDaemonSet, Namespace: "a", Name: "agent", Ready: 0, Desired: 2, Images: []string{"example.com/agent:2"}, Health: model.HealthCrit, Restartable: true, Protected: true},
		{Ref: "kubernetes:Workload/Deployment/a/web", Kind: model.WorkloadKindDeployment, Namespace: "a", Name: "web", Ready: 2, Desired: 2, Images: []string{"example.com/app:1"}, Health: model.HealthOK, Restartable: true, Protected: true},
		{Ref: "kubernetes:Workload/Job/a/migrate", Kind: model.WorkloadKindJob, Namespace: "a", Name: "migrate", Ready: 1, Desired: 3, Images: []string{}, Health: model.HealthOK, Protected: true},
		{Ref: "kubernetes:Workload/StatefulSet/a/db", Kind: model.WorkloadKindStatefulSet, Namespace: "a", Name: "db", Ready: 1, Desired: 3, Images: []string{"example.com/app:1"}, Health: model.HealthWarn, Restartable: true, Protected: true},
	}
	for i := range d.Workloads {
		d.Workloads[i].Links = nil // asserted on its own below
	}
	if !reflect.DeepEqual(d.Workloads, want) {
		t.Errorf("workloads = %+v\nwant %+v", d.Workloads, want)
	}
	got := buildWorkloads(clusterInput{c: testCluster(t, nil, deployment("a", "web", 1, 1)), links: clStubLinks{}}, nil)
	if len(got) != 1 || len(got[0].Links) != 1 || got[0].Links[0].Href != "https://tools.example.com/Deployment/a/web" {
		t.Errorf("links = %+v", got)
	}
}

func TestEventsNewestFirstAndCapped(t *testing.T) {
	var objs []k8sruntime.Object
	for i := range maxEvents + 20 {
		objs = append(objs, &corev1.Event{
			ObjectMeta:     metav1.ObjectMeta{Namespace: "a", Name: fmt.Sprintf("e%03d", i)},
			InvolvedObject: corev1.ObjectReference{Kind: "Pod", Name: fmt.Sprintf("p%03d", i)},
			Reason:         "BackOff", Message: "restarting", Count: 1,
			LastTimestamp: metav1.NewTime(clNow.Add(-time.Duration(i) * time.Minute)),
		})
	}
	d := clBuild(t, clConv, usageSnap{}, objs...)
	if len(d.Events) != maxEvents {
		t.Fatalf("events = %d, want %d", len(d.Events), maxEvents)
	}
	if d.Events[0].Object != "Pod/p000" || d.Events[maxEvents-1].Object != fmt.Sprintf("Pod/p%03d", maxEvents-1) {
		t.Errorf("first %q, last %q: want the newest 100, newest first", d.Events[0].Object, d.Events[maxEvents-1].Object)
	}
	for i := 1; i < len(d.Events); i++ {
		if d.Events[i].LastSeen.After(d.Events[i-1].LastSeen) {
			t.Fatalf("event %d is newer than the one before it", i)
		}
	}
}

func TestEventTimeAndCountFallbacks(t *testing.T) {
	created := clNow.Add(-time.Hour)
	at := clNow.Add(-time.Minute)
	d := clBuild(t, clConv, usageSnap{},
		&corev1.Event{ // last timestamp, count from the series
			ObjectMeta: metav1.ObjectMeta{Namespace: "a", Name: "series"}, InvolvedObject: corev1.ObjectReference{Kind: "Node", Name: "cp-1"},
			Reason: "Rebooted", Message: "m", Count: 2, LastTimestamp: metav1.NewTime(clNow.Add(-2 * time.Minute)),
			Series: &corev1.EventSeries{Count: 9},
		},
		&corev1.Event{ // event time, no count
			ObjectMeta: metav1.ObjectMeta{Namespace: "a", Name: "evtime"}, InvolvedObject: corev1.ObjectReference{Kind: "Pod", Name: "p"},
			EventTime: metav1.NewMicroTime(at),
		},
		&corev1.Event{ // creation time only
			ObjectMeta:     metav1.ObjectMeta{Namespace: "a", Name: "created", CreationTimestamp: metav1.NewTime(created)},
			InvolvedObject: corev1.ObjectReference{Kind: "Pod", Name: "q"},
		},
	)
	if len(d.Events) != 3 {
		t.Fatalf("events = %+v", d.Events)
	}
	if e := d.Events[0]; e.Object != "Pod/p" || !e.LastSeen.Equal(at) || e.Count != 1 {
		t.Errorf("event time event = %+v", e)
	}
	if e := d.Events[1]; e.Object != "Node/cp-1" || e.Count != 9 || e.Reason != "Rebooted" || e.Namespace != "a" {
		t.Errorf("series event = %+v", e)
	}
	if e := d.Events[2]; e.Object != "Pod/q" || !e.LastSeen.Equal(created) {
		t.Errorf("created event = %+v", e)
	}
}

func TestCertificatesAndExternalSecrets(t *testing.T) {
	ready := obj(kube.GVRCertificates, "gateway", "wild")
	_ = unstructured.SetNestedField(ready.Object, "2027-01-02T03:04:05Z", "status", "notAfter")
	_ = unstructured.SetNestedSlice(ready.Object, []any{map[string]any{"type": "Ready", "status": "True"}}, "status", "conditions")
	broken := obj(kube.GVRCertificates, "apps", "bad")
	_ = unstructured.SetNestedField(broken.Object, "not a time", "status", "notAfter")
	es := obj(kube.GVRExternalSecrets, "media", "creds")
	_ = unstructured.SetNestedSlice(es.Object, []any{map[string]any{"type": "Ready", "status": "False", "message": "store unreachable"}}, "status", "conditions")
	okES := obj(kube.GVRExternalSecrets, "apps", "token")
	_ = unstructured.SetNestedSlice(okES.Object, []any{map[string]any{"type": "Ready", "status": "True", "message": "Secret was synced"}}, "status", "conditions")

	d := clBuild(t, clConv, usageSnap{}, ready, broken, es, okES)
	if len(d.Certificates) != 2 || d.Certificates[0].Name != "bad" || d.Certificates[1].Name != "wild" {
		t.Fatalf("certificates = %+v, want sorted by namespace/name", d.Certificates)
	}
	if c := d.Certificates[0]; c.Ready || c.NotAfter != nil || c.Namespace != "apps" {
		t.Errorf("bad certificate = %+v: not ready, an unparsable expiry is none", c)
	}
	if c := d.Certificates[1]; !c.Ready || c.NotAfter == nil || !c.NotAfter.Equal(time.Date(2027, 1, 2, 3, 4, 5, 0, time.UTC)) {
		t.Errorf("wild certificate = %+v", c)
	}
	if len(d.Certificates[1].Links) != 1 || d.Certificates[1].Links[0].Href != "https://tools.example.com/Certificate/gateway/wild" {
		t.Errorf("certificate links = %+v", d.Certificates[1].Links)
	}
	want := []model.ExtSecret{
		{Ref: "kubernetes:ExternalSecret/apps/token", Namespace: "apps", Name: "token", Ready: true, Message: "Secret was synced"},
		{Ref: "kubernetes:ExternalSecret/media/creds", Namespace: "media", Name: "creds", Ready: false, Message: "store unreachable"},
	}
	if !reflect.DeepEqual(d.ExternalSecrets, want) {
		t.Errorf("external secrets = %+v, want %+v", d.ExternalSecrets, want)
	}
}

func TestCustomResourcesThatAreNotServedLeaveEmptySlices(t *testing.T) {
	c := testCluster(t, []schema.GroupVersionResource{kube.GVRHTTPRoutes})
	d := buildCluster(clusterInput{c: c, conv: clConv, now: clNow})
	if d.Certificates == nil || len(d.Certificates) != 0 || d.ExternalSecrets == nil || len(d.ExternalSecrets) != 0 {
		t.Errorf("certificates = %#v, external secrets = %#v, want empty non-nil slices", d.Certificates, d.ExternalSecrets)
	}
}
