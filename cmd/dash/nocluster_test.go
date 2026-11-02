package main

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/secret"
)

// dash starts and serves Proxmox with no cluster (the epic's acceptance, contract 6.3): run() with
// no kubeconfig and outside a cluster (rest.ErrNotInCluster), the real proxmox plugin on a fake
// Proxmox API and the real kubernetes plugin. The process serves and is ready, kubernetes is
// unavailable and proxmox healthy, proxmox publishes its VMs although the node index is absent
// (an unavailable plugin exports no service), flux is unavailable and publishes nothing, and a
// viewer can ask who they are. SIGTERM ends run() cleanly.
func TestStartsWithoutACluster(t *testing.T) {
	pve := newFakePVE(t)
	apiAddr, healthAddr := freeAddr(t), freeAddr(t)
	startEnv(t, t.TempDir(),
		// No kubeconfig, and no in-cluster environment: client-go answers rest.ErrNotInCluster.
		"KUBECONFIG=", "KUBERNETES_SERVICE_HOST=", "KUBERNETES_SERVICE_PORT=",
		"DASH_SERVER_LISTEN="+apiAddr, "DASH_SERVER_HEALTH_LISTEN="+healthAddr,
		`DASH_SERVER_READY_REQUIRES=["kubernetes","proxmox"]`,
		"DASH_PLUGINS_PROXMOX_URL="+pve.URL, "DASH_PLUGINS_PROXMOX_TOKEN_ID=u@pve!t",
		"DASH_PLUGINS_PROXMOX_TOKEN_SECRET=token-secret-0123456789")

	done := make(chan error, 1)
	go func() { done <- run(secret.NewRegistry()) }()
	stopped := false
	stop := func() {
		if stopped {
			return
		}
		stopped = true
		if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
			t.Error(err)
			return
		}
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("run returned %v, want the exit-0 path", err)
			}
		case <-time.After(15 * time.Second):
			t.Error("run did not return after SIGTERM")
		}
	}
	// run installs its signal handler before it serves: stop only once /readyz has answered.
	defer func() {
		if !stopped {
			waitFor(t, func() bool { c, _ := fetch(healthAddr, "/healthz", false); return c == http.StatusOK })
			stop()
		}
	}()

	// /readyz is 200 although kubernetes cannot start: an unavailable plugin is settled.
	waitFor(t, func() bool {
		select {
		case err := <-done:
			t.Fatalf("run returned early: %v", err)
		default:
		}
		code, _ := fetch(healthAddr, "/readyz", false)
		return code == http.StatusOK
	})

	rows := func() map[string]model.PluginInfo {
		code, body := fetch(apiAddr, "/api/v1/plugins", true)
		var list []model.PluginInfo
		if code != http.StatusOK || json.Unmarshal(body, &list) != nil {
			return nil
		}
		out := map[string]model.PluginInfo{}
		for _, p := range list {
			out[p.ID] = p
		}
		return out
	}
	waitFor(t, func() bool {
		r := rows()
		return r["proxmox"].State == model.PluginHealthy && r["kubernetes"].State == model.PluginUnavailable
	})
	r := rows()
	if got := r["flux"].State; got != model.PluginUnavailable {
		t.Errorf("flux is %q, want unavailable: it needs the cluster", got)
	}
	if k := r["kubernetes"]; k.Message == "" {
		t.Errorf("the kubernetes row says nothing about why it is unavailable: %+v", k)
	}

	// proxmox.vms is published without the node index.
	var vms model.VMsData
	waitFor(t, func() bool {
		code, body := fetch(apiAddr, "/api/v1/topics/proxmox.vms", true)
		var env model.Envelope
		return code == http.StatusOK && json.Unmarshal(body, &env) == nil && json.Unmarshal(env.Data, &vms) == nil && len(vms.VMs) == 1
	})
	if vms.VMs[0].K8sNode != nil {
		t.Errorf("a VM is a Kubernetes node without a cluster: %+v", vms.VMs[0].K8sNode)
	}
	// The cluster's topics are not published, and the flux shim's is hidden with its plugin.
	for _, topic := range []string{"kubernetes.cluster", "kubernetes.storage", "kubernetes.apps", "flux"} {
		if code, _ := fetch(apiAddr, "/api/v1/topics/"+topic, true); code != http.StatusNotFound {
			t.Errorf("topic %s answers %d, want 404", topic, code)
		}
	}

	code, body := fetch(apiAddr, "/api/v1/me", true)
	var me model.Me
	if code != http.StatusOK || json.Unmarshal(body, &me) != nil || me.User != "alice" {
		t.Errorf("/api/v1/me = %d %s, want the signed-in user", code, body)
	}

	stop()
}

// fetch is a GET against a listener of the running dash. The API listener answers for the public
// URL's host, and an API request is signed in as alice by the header the proxy sets.
func fetch(addr, path string, signedIn bool) (int, []byte) {
	req, err := http.NewRequest(http.MethodGet, "http://"+addr+path, nil)
	if err != nil {
		return 0, nil
	}
	req.Host = "dash.example.com"
	if signedIn {
		req.Header.Set("X-Forwarded-Preferred-Username", "alice")
	}
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		return 0, nil
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, body
}
