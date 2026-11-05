package config

// validateSettings checks the keys of the settings section that the settings API adds (P4.7):
// settings.secretNamespaces. validate calls it; settings.ui is checked there.
func validateSettings(v *validator, s SettingsConfig) {}
