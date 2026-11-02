// Package demo is dash's demo mode. DASH_DEMO=true runs every plugin's demo twin (plugin.Demo)
// over the fake homelab of package world, and the overview is composed from what the twins
// contribute, exactly as in production; nothing here talks to Kubernetes, Proxmox or any
// monitoring service. The demo's viewer is the fixed identity auth.Fixed gives it in cmd/dash:
// it ignores identity headers, so this package holds no sign-in of its own.
package demo
