// Package health holds the badge rules the Kubernetes-backed plugins share: the thresholds of
// plan 3.5 for volumes and buckets, certificates, workloads and pods. Every badge and every
// attention item of those plugins uses these functions only.
//
// It is a copy of the rules in internal/topics, which keep serving the Manager until their last
// reader moves into a plugin; a plugin imports neither topics nor another plugin.
package health

import (
	"time"

	"github.com/TechDufus/dash/internal/model"
)

// The thresholds of plan 3.5.
const (
	ratioWarn, ratioCrit = 0.80, 0.90
	certWarn, certCrit   = 21 * 24 * time.Hour, 7 * 24 * time.Hour
	pendingGrace         = 5 * time.Minute
)

// RatioHealth is the PVC and bucket rule: >= 80% warn, >= 90% crit.
func RatioHealth(r float64) model.Health {
	switch {
	case r >= ratioCrit:
		return model.HealthCrit
	case r >= ratioWarn:
		return model.HealthWarn
	}
	return model.HealthOK
}

// CertHealth: expiring in less than 21 days warns, less than 7 days is critical.
func CertHealth(notAfter, now time.Time) model.Health {
	left := notAfter.Sub(now)
	switch {
	case left < certCrit:
		return model.HealthCrit
	case left < certWarn:
		return model.HealthWarn
	}
	return model.HealthOK
}

// WorkloadHealth: ready < desired warns when something is ready, is critical when nothing is.
func WorkloadHealth(ready, desired int) model.Health {
	switch {
	case desired <= 0 || ready >= desired:
		return model.HealthOK
	case ready > 0:
		return model.HealthWarn
	}
	return model.HealthCrit
}

var crashReasons = map[string]bool{
	"CrashLoopBackOff":           true,
	"ImagePullBackOff":           true,
	"ErrImagePull":               true,
	"CreateContainerConfigError": true,
	"CreateContainerError":       true,
}

// PodHealth: a crashing or unpullable container is critical; Pending for over five minutes warns.
func PodHealth(phase string, waitingReasons []string, createdAt, now time.Time) model.Health {
	for _, r := range waitingReasons {
		if crashReasons[r] {
			return model.HealthCrit
		}
	}
	if phase == "Pending" && now.Sub(createdAt) > pendingGrace {
		return model.HealthWarn
	}
	return model.HealthOK
}
