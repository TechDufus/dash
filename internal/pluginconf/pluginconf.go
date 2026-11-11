// Package pluginconf holds the configuration sections of the plugins (plugins.<id>.*) and the
// top-level ui section, so that internal/config (the typed tree, its schema, environment names
// and validation) and every plugin read one definition.
//
// It sits below both: it imports only internal/secret and internal/model, never
// internal/config. That is what lets a plugin package import its section without a cycle
// (internal/kube imports internal/config, so a plugin built on it could not otherwise be
// embedded in the tree). A plugin imports pluginconf and never internal/config.
//
// The json and dash tags are the YAML keys and the generated environment names; internal/config
// reads them by reflection (see config.Tree for the grammar). A tag of an existing field never
// changes, because that would rename an operator's variable.
//
// A section implements the optional methods the rest of dash looks for:
//
//	Validate() error                 cross-field rules the schema cannot express; the error holds
//	                                 FieldErrors so the caller can name the setting and its layer
//	ActionsConfig() Actions          the action policy (plugins.<id>.actions.*) of a plugin that
//	                                 has actions
//	ReadDenyConfig() []string        the namespaces the plugin's read routes refuse
package pluginconf

import (
	"errors"
	"fmt"
)

// PluginBase is embedded in every plugin section. It adds the two settings every instance has,
// plugins.<id>.type and plugins.<id>.enabled (design A.2, B.2), whatever else the section holds.
type PluginBase struct {
	// Type is the plugin type. dash runs one instance per type, so it is empty or the section
	// name (design J.16).
	Type string `json:"type" doc:"The plugin type. dash runs one instance per type, so this is empty or the section name."`
	// Enabled turns the plugin on or off. Unset turns it on when it has its settings: a plugin without them is disabled.
	Enabled *bool `json:"enabled" doc:"false turns the plugin off: its topics are removed, and the web UI drops its rail entry, palette entries, overview tiles and panels and shows a switched-off page at its address. Unset turns it on when it has the settings it needs (rustfs is off unless enabled: true, because nothing in its settings says an object store exists); a plugin without them is disabled (\"not configured: set plugins.<id>.url\", naming the setting to make) and the web UI hides its pages. true with settings missing or invalid makes it unconfigured, which hides its pages too. This is how prometheus, alertmanager, gatus, victorialogs, proxmox, hardware, ssh, kubernetes, flux, cnpg, rustfs, grafana, headlamp and launcher behave, and a change to any of their settings applies live: that plugin restarts, and so do the plugins that use or require it (a prometheus change restarts proxmox, hardware, kubernetes, cnpg and rustfs; a gatus change restarts hardware and kubernetes; a proxmox change restarts hardware; a kubernetes change restarts proxmox, flux and cnpg; a grafana change restarts alertmanager)."`
}

// FieldError is one invalid setting of a section. Path is relative to the section
// ("auth.basic.password"), so a caller that knows the section prefixes it and looks up which
// layer set the value.
type FieldError struct {
	Path string
	Msg  string
}

func (e FieldError) Error() string { return e.Path + ": " + e.Msg }

// Problems lists the FieldErrors inside err, which is what a section's Validate returns: nil, one
// FieldError or several joined. An error of another kind comes back as a FieldError with an
// empty Path, so nothing a validator reported is lost.
func Problems(err error) []FieldError {
	if err == nil {
		return nil
	}
	if fe, ok := err.(FieldError); ok {
		return []FieldError{fe}
	}
	if j, ok := err.(interface{ Unwrap() []error }); ok {
		var out []FieldError
		for _, e := range j.Unwrap() {
			out = append(out, Problems(e)...)
		}
		return out
	}
	return []FieldError{{Msg: err.Error()}}
}

// problems collects the field errors a Validate finds and turns them into one error.
type problems []error

func (p *problems) add(path, format string, args ...any) {
	*p = append(*p, FieldError{Path: path, Msg: fmt.Sprintf(format, args...)})
}

func (p *problems) addAll(prefix string, err error) {
	for _, fe := range Problems(err) {
		if prefix != "" && fe.Path != "" {
			fe.Path = prefix + "." + fe.Path
		} else if prefix != "" {
			fe.Path = prefix
		}
		*p = append(*p, fe)
	}
}

func (p problems) err() error { return errors.Join(p...) }
