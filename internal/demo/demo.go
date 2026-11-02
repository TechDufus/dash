// Package demo is dash's demo mode. DASH_DEMO=true runs every plugin's demo twin (plugin.Demo)
// over the fake homelab of package world, and the overview is composed from what the twins
// contribute, exactly as in production; nothing here talks to Kubernetes, Proxmox or any
// monitoring service. What is left of this package is the sign-in the demo's viewer needs.
package demo

import "net/http"

// SignIn signs every request in as the user "demo" unless it already names a user. Everything
// else is the API's own.
func SignIn(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Forwarded-User") == "" {
			r.Header.Set("X-Forwarded-User", "demo")
		}
		next.ServeHTTP(w, r)
	})
}
