package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/TechDufus/dash/internal/config"
)

// healthcheckUsage is the usage line of `dash healthcheck`.
const healthcheckUsage = "dash healthcheck"

// healthcheckTimeout bounds the whole request, below the image's HEALTHCHECK --timeout (3s).
const healthcheckTimeout = 2 * time.Second

// errUnhealthy makes `dash healthcheck` exit 1 whatever went wrong: a container runtime reads
// 1 as unhealthy and reserves 2, so even a usage error is 1 here.
var errUnhealthy = errors.New("healthcheck")

// healthcheckBodyLimit bounds what is read of the /readyz body ("ok" or "not ready").
const healthcheckBodyLimit = 4096

// dialFunc opens the connection the healthcheck sends its request over.
type dialFunc func(ctx context.Context, network, addr string) (net.Conn, error)

// healthcheckCommand runs `dash healthcheck`: it GETs /readyz on the health listener of the dash
// configured by this process's environment and config file, as a container HEALTHCHECK.
func healthcheckCommand(args []string) error {
	if len(args) != 0 {
		return fmt.Errorf("%w: takes no arguments; usage: %s", errUnhealthy, healthcheckUsage)
	}
	src, err := config.ProcessSources(nil)
	if err != nil {
		return unhealthy(err)
	}
	return healthcheck(src, (&net.Dialer{}).DialContext, healthcheckTimeout)
}

// healthcheck resolves server.healthListen from src as startup does (the defaults, the file and
// the environment; the UI may not set server.*), GETs /readyz there through dial within timeout,
// and returns nil on 200. It reads no secret and validates nothing beyond layering, so it needs
// neither the cluster, the data directory nor a secret key; no proxy is used, no redirect is
// followed and at most healthcheckBodyLimit bytes of the body are read. Every error wraps
// errUnhealthy and is one line that names the URL, never a configured value.
func healthcheck(src config.Sources, dial dialFunc, timeout time.Duration) error {
	listen, err := config.HealthListenOf(src)
	if err != nil {
		return unhealthy(err)
	}
	u, err := readyzURL(listen)
	if err != nil {
		return unhealthy(err)
	}
	client := &http.Client{
		Timeout:   timeout,
		Transport: &http.Transport{Proxy: nil, DialContext: dial, DisableKeepAlives: true},
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Get(u)
	if err != nil {
		return unhealthy(err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, healthcheckBodyLimit))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%w: GET %s: %s", errUnhealthy, u, resp.Status)
	}
	return nil
}

// readyzURL is the /readyz URL of a health listener address. An unspecified host (empty,
// 0.0.0.0 or ::) binds every interface, so it is reached on loopback of the same family.
func readyzURL(listen string) (string, error) {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return "", fmt.Errorf("server.healthListen: %w", err)
	}
	if ip := net.ParseIP(host); host == "" || ip != nil && ip.IsUnspecified() {
		host = "127.0.0.1"
		if ip != nil && ip.To4() == nil {
			host = "::1"
		}
	}
	return "http://" + net.JoinHostPort(host, port) + "/readyz", nil
}

// unhealthy wraps err in errUnhealthy as one line: a joined layering error has several.
func unhealthy(err error) error {
	return fmt.Errorf("%w: %s", errUnhealthy, strings.Join(strings.Fields(err.Error()), " "))
}
