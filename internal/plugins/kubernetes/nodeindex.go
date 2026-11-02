package kubernetes

import (
	corev1 "k8s.io/api/core/v1"
)

func nodeReady(conds []corev1.NodeCondition) bool {
	for _, c := range conds {
		if c.Type == corev1.NodeReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}

func nodeIP(n *corev1.Node) string {
	for _, a := range n.Status.Addresses {
		if a.Type == corev1.NodeInternalIP {
			return a.Address
		}
	}
	return ""
}

// k8sNode maps a VM name to the cluster node of the same name and says whether it is Ready.
// Before the typed informers have synced it answers ok=false for every name: a VM is linked to
// its node only once the nodes are known.
func (r *runtime) k8sNode(vmName string) (node string, ready, ok bool) {
	if !r.cl.HasSynced() {
		return "", false, false
	}
	n, err := r.cl.Nodes.Get(vmName)
	if err != nil {
		return "", false, false
	}
	return n.Name, nodeReady(n.Status.Conditions), true
}

// nodeAddress is a node's InternalIP, from the plugin's own node cache.
func (r *runtime) nodeAddress(name string) (string, bool) {
	if r.twin != nil {
		return r.twin.nodeIP(name)
	}
	n, err := r.cl.Nodes.Get(name)
	if err != nil {
		return "", false
	}
	ip := nodeIP(n)
	return ip, ip != ""
}
