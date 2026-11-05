{{- /*
`go-licenses report --template` input for the Makefile's notices target: one tab-separated line per
package and licence (name, version, SPDX id, licence file in the module cache), which
web/scripts/licenses.mjs renders into THIRD_PARTY_NOTICES.md.
*/ -}}
{{- range . }}{{ printf "%s\t%s\t%s\t%s\n" .Name .Version .LicenseName .LicensePath }}{{ end -}}
