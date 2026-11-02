package kube

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/kubernetes"

	"github.com/TechDufus/dash/internal/upstream"
)

// ProbeTimeout is how long one probe of the API server may take.
const ProbeTimeout = 3 * time.Second

// Probe asks the API server for its version (GET /version): the cheapest request that proves it
// is reachable and answering. It gives up after ProbeTimeout.
func Probe(ctx context.Context, client kubernetes.Interface) error {
	ctx, cancel := context.WithTimeout(ctx, ProbeTimeout)
	defer cancel()
	_, err := client.Discovery().ServerVersionWithContext(ctx)
	return err
}

// Category reduces an error from the API server to a category a viewer may see: auth for 401 and
// 403, http_<code> for any other status the server sent, timeout, tls or unreachable otherwise.
// The categories are those of internal/upstream, so a row reads the same whatever the source.
func Category(err error) string {
	var st apierrors.APIStatus
	var ne net.Error
	switch {
	case err == nil:
		return ""
	case apierrors.IsUnauthorized(err) || apierrors.IsForbidden(err):
		return upstream.CategoryAuth
	case errors.As(err, &st) && st.Status().Code != 0:
		return upstream.HTTPCategory(int(st.Status().Code))
	case errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout()):
		return upstream.CategoryTimeout
	}
	return upstream.Category(err)
}

// NodeExporterPort is the node-exporter port; Kubernetes node series carry instance="<ip>:9100".
const NodeExporterPort = 9100

// InstanceOf is the node-exporter instance label of a node address.
func InstanceOf(ip string) string { return net.JoinHostPort(ip, fmt.Sprint(NodeExporterPort)) }
