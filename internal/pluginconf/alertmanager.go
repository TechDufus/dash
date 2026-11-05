package pluginconf

import "slices"

// silenceDurations are the lengths a silence can have: the keys of the duration table of the
// silence.create action (internal/plugins/alertmanager). A plugin offers a subset of them in its
// dialog.
var silenceDurations = []string{"1h", "4h", "24h", "7d"}

// Alertmanager is plugins.alertmanager: alerts and silences.
type Alertmanager struct {
	PluginBase
	URL              string            `json:"url" dash:"url" doc:"Base URL of the service, e.g. http://host:9090. Empty turns the source off: dash makes no request and its panels show \"source not configured\"."`
	Auth             Auth              `json:"auth" doc:"How dash authenticates to Alertmanager. Neither basic nor bearerToken sends no credentials."`
	TLS              TLS               `json:"tls" doc:"How dash verifies Alertmanager's certificate."`
	IgnoreAlerts     []string          `json:"ignoreAlerts" doc:"Alert names dash drops everywhere: the alert list, the attention items and the firing count. A JSON list in the environment."`
	SeverityMap      map[string]string `json:"severityMap" doc:"How an alert's severity label value becomes dash's severity (crit or warn), as {label value: severity}. A JSON object in the environment."`
	SilenceDurations []string          `json:"silenceDurations" doc:"The silence lengths the dialog offers, from 1h, 4h, 24h and 7d. A JSON list in the environment."`
	ProtectedAlerts  []string          `json:"protectedAlerts" doc:"Alert names nobody can silence through dash, whatever the role. A JSON list in the environment."`
	Actions          ActionsToggle     `json:"actions" doc:"The silence actions."`
}

// DefaultAlertmanager is the section with nothing set.
func DefaultAlertmanager() Alertmanager {
	return Alertmanager{
		IgnoreAlerts:     []string{"Watchdog", "InfoInhibitor"},
		SeverityMap:      map[string]string{"critical": "crit", "warning": "warn"},
		SilenceDurations: slices.Clone(silenceDurations),
		ProtectedAlerts:  []string{"Watchdog"},
	}
}

// Validate checks what the schema cannot: the auth rules, that every silence duration is one
// the silence action accepts, and that every severity is one dash knows (a value it does not
// know would silently stop raising that alert).
func (a Alertmanager) Validate() error {
	var p problems
	p.addAll("auth", a.Auth.validate())
	for i, d := range a.SilenceDurations {
		if !slices.Contains(silenceDurations, d) {
			p.add("silenceDurations", "entry %d must be one of: 1h, 4h, 24h, 7d", i+1)
		}
	}
	bad := 0
	for _, sev := range a.SeverityMap {
		if sev != "crit" && sev != "warn" {
			bad++
		}
	}
	if bad > 0 {
		p.add("severityMap", "maps %d label(s) to a severity other than crit or warn: every value must be crit or warn", bad)
	}
	return p.err()
}

// ActionsConfig implements the action policy hook: only the toggle, because a silence has no
// namespace for a deny list to match.
func (a Alertmanager) ActionsConfig() Actions { return Actions{Enabled: a.Actions.Enabled} }
