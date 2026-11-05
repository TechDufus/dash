package config

import "regexp"

// namespaceRE is a Kubernetes namespace name: an RFC 1123 label.
var namespaceRE = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

// validateSettings checks the keys of the settings section that the settings API adds (P4.7):
// settings.secretNamespaces. validate calls it; settings.ui is checked there.
func validateSettings(v *validator, s SettingsConfig) {
	// The entry is named by its position, never its text (design D16).
	for i, ns := range s.SecretNamespaces {
		if len(ns) > 63 || !namespaceRE.MatchString(ns) {
			v.fail("settings.secretNamespaces", "entry %d is not a Kubernetes namespace name", i+1)
		}
	}
}
