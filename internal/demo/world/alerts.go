package world

import (
	"time"

	"github.com/TechDufus/dash/internal/model"
)

// Alerts is the Alertmanager payload: two firing alerts, the always-on Watchdog and the silence
// that mutes it.
func (w *World) Alerts() model.AlertsData { return w.alerts(w.clock()) }

func (w *World) alerts(now time.Time) model.AlertsData {
	crashPod := crashPodName()
	return model.AlertsData{
		Alerts: []model.Alert{
			{Fingerprint: "a1b2c3d4e5f60001", Name: "KubePodCrashLooping", Severity: "warning", State: "active",
				Summary:     "Pod acme/" + crashPod + " is crash looping",
				Description: "Container app has restarted 14 times in the last hour.",
				Labels:      map[string]string{"alertname": "KubePodCrashLooping", "namespace": "acme", "pod": crashPod, "severity": "warning"},
				StartsAt:    ago(now, 41*time.Minute), SilencedBy: []string{}, InhibitedBy: []string{}},
			{Fingerprint: "a1b2c3d4e5f60002", Name: "PersistentVolumeFillingUp", Severity: "warning", State: "active",
				Summary:     "PVC monitoring/vmsingle-data is 83% full",
				Description: "At the current rate it fills up in about 14 days.",
				Labels:      map[string]string{"alertname": "PersistentVolumeFillingUp", "namespace": "monitoring", "persistentvolumeclaim": "vmsingle-data", "severity": "warning"},
				StartsAt:    ago(now, 3*time.Hour), SilencedBy: []string{}, InhibitedBy: []string{}},
			{Fingerprint: "a1b2c3d4e5f60003", Name: "Watchdog", Severity: "none", State: "suppressed",
				Summary: "Alerting pipeline is alive", Description: "Always firing.",
				Labels:   map[string]string{"alertname": "Watchdog", "severity": "none"},
				StartsAt: ago(now, 38*24*time.Hour), SilencedBy: []string{"6f1e3d2c-demo"}, InhibitedBy: []string{}},
		},
		Silences: []model.Silence{{ID: "6f1e3d2c-demo", Matchers: []model.Matcher{{Name: "alertname", Value: "Watchdog", IsEqual: true}},
			CreatedBy: "demo", Comment: "heartbeat goes to the dead-man switch, not the UI",
			StartsAt: ago(now, 2*24*time.Hour), EndsAt: ago(now, -5*24*time.Hour), State: "active"}},
	}
}
