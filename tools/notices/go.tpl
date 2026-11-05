{{- /*
`go-licenses report --template` input for the Makefile's notices target: one tab-separated line per
package and licence (name, SPDX id, licence file in the module cache), which
web/scripts/licenses.mjs renders into THIRD_PARTY_NOTICES.md. No version: a dependency update must
not change the notices unless a licence does.
*/ -}}
{{- range . }}{{ printf "%s\t%s\t%s\n" .Name .LicenseName .LicensePath }}{{ end -}}
