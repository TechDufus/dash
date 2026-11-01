package config

import (
	"fmt"
	"io"
	"reflect"
	"text/tabwriter"
)

// EnvVar describes one setting and the variable that sets it, for `dash config env`.
type EnvVar struct {
	Path    string // dotted path in the config file
	Name    string // generated environment variable
	Type    string // "string", or what the JSON value holds
	Secret  bool   // the value is a credential
	Restart bool   // a change needs a restart
}

// EnvVars lists every setting in schema order.
func EnvVars() []EnvVar {
	out := make([]EnvVar, 0, len(schema.leaves))
	for _, l := range schema.leaves {
		t := "string"
		switch {
		case l.typ == durationType:
			t = "duration (e.g. 30s)"
		case l.typ.Kind() == reflect.Int:
			t = "integer"
		case l.typ.Kind() == reflect.Slice && l.typ.Elem().Kind() == reflect.String:
			t = "list of strings (JSON)"
		case l.typ.Kind() == reflect.Slice:
			t = "list of objects (JSON)"
		}
		out = append(out, EnvVar{l.path, l.env, t, l.secret, l.restart})
	}
	return out
}

// WriteEnvTable prints EnvVars as an aligned table, plus the rules that apply to every name.
func WriteEnvTable(w io.Writer) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "PATH\tENV\tTYPE\tSECRET\tRESTART")
	for _, v := range EnvVars() {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", v.Path, v.Name, v.Type, yesNo(v.Secret), yesNo(v.Restart))
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	_, err := io.WriteString(w, `
Every name also takes a _FILE form (DASH_NAME_FILE=/path) that reads the value from a file; set
one of the two. The environment overrides the config file. Lists and objects are JSON. KUBECONFIG
is also honoured for plugins.kubernetes.kubeconfig; it has no _FILE form and the generated name
wins over it.

In this release every change to a setting other than a secret needs a restart, whatever the
RESTART column says: the column marks the settings that stay restart-only once the plugin
framework applies the rest live. A rotated secret (a changed file: reference) applies live.
`)
	return err
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}
