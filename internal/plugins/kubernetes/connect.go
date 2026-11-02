package kubernetes

import (
	"os"

	"k8s.io/client-go/rest"

	"github.com/TechDufus/dash/internal/kube"
	"github.com/TechDufus/dash/internal/pluginconf"
)

// Connection is what reaching the cluster yields: the informer-backed Cluster and the REST
// config it was built from (nil for a test's fake clients), which upstreamViaAPIServer needs.
type Connection struct {
	Cluster *kube.Cluster
	REST    *rest.Config
}

// Connect reaches the cluster a section describes. kube.ErrNoCluster means there is none to
// reach (the plugin is unavailable); any other error is a mode that cannot run.
type Connect func(cfg pluginconf.Kubernetes) (*Connection, error)

// connectCluster is the production Connect: the kubeconfig named by the section or by the
// standard KUBECONFIG variable, or the in-cluster service account, as the mode says.
func connectCluster(cfg pluginconf.Kubernetes) (*Connection, error) {
	mode := cfg.ConnectionMode()
	path := cfg.Kubeconfig
	if path == "" && mode != pluginconf.KubernetesModeInCluster {
		path = os.Getenv("KUBECONFIG")
	}
	rc, err := kube.LoadConfig(mode, path, cfg.Context, float32(cfg.EffectiveQPS()), cfg.EffectiveBurst())
	if err != nil {
		return nil, err
	}
	cl, err := kube.New(rc)
	if err != nil {
		return nil, err
	}
	return &Connection{Cluster: cl, REST: rc}, nil
}
