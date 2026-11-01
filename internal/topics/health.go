package topics

import (
	"time"

	"github.com/TechDufus/dash/internal/model"
)

// The thresholds of plan 3.5. Every badge and the Attention list use these functions only.
const (
	ratioWarn, ratioCrit = 0.80, 0.90
	certWarn, certCrit   = 21 * 24 * time.Hour, 7 * 24 * time.Hour
	tempWarnC, tempCritC = 85.0, 90.0
	pendingGrace         = 5 * time.Minute
)

func rank(h model.Health) int {
	switch h {
	case model.HealthCrit:
		return 3
	case model.HealthWarn:
		return 2
	case model.HealthUnknown:
		return 1
	}
	return 0
}

// Worst returns the most severe health; no arguments is ok.
func Worst(hs ...model.Health) model.Health {
	w := model.HealthOK
	for _, h := range hs {
		if rank(h) > rank(w) {
			w = h
		}
	}
	return w
}

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

// TempHealth is the CPU package temperature rule.
func TempHealth(c float64) model.Health {
	switch {
	case c >= tempCritC:
		return model.HealthCrit
	case c >= tempWarnC:
		return model.HealthWarn
	}
	return model.HealthOK
}

// IsTalosVM reports whether vmid is one of the Talos control-plane VMs (2020-2022).
func IsTalosVM(vmid int) bool { return vmid >= 2020 && vmid <= 2022 }

// VMHealth: a VM that is not running warns, critical for the Talos VMs; templates are skipped.
func VMHealth(vmid int, status string, template bool) model.Health {
	switch {
	case template, status == "running":
		return model.HealthOK
	case IsTalosVM(vmid):
		return model.HealthCrit
	}
	return model.HealthWarn
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
